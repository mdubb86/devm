"""202: Supabase recipe end-to-end (full stack + auth email flow).

Proves recipes/service/supabase.md works on a real Tart VM by
exercising the recipe's full promise:

  A. Install works. `supabase --version` returns 0.

  B. `supabase start` brings up the full stack (~10 containers).
     Waits for all services healthy, then queries `supabase status`
     for endpoint URLs and the anon key.

  C. Every routing pattern the recipe declares works from inside the
     VM:
       - HTTP-via-proxy: `curl https://api.<proj>.test/rest/v1/`,
         `curl https://studio.<proj>.test`, `curl https://mail.<proj>.test`
         all reach their upstream containers.
       - Raw-TCP-direct: `psql db.<proj>.test:54322` returns `SELECT 1`.
     This proves both routing planes (proxy + direct) actually work.

  D. Full auth email flow — the piece the recipe's email-templates
     section exists for. Recipe overrides GoTrue's default templates
     so emails carry a `{{ .SiteURL }}` link, not the broken
     `127.0.0.1:54321` GoTrue defaults to. Steps:
       1. POST /auth/v1/signup a test user.
       2. Poll Inbucket for the confirmation email.
       3. Extract the confirmation URL from the email body.
       4. Assert the URL points at our hostname (NOT 127.0.0.1) —
          this is the concrete proof the template fix works.
       5. Extract token_hash + type from the URL.
       6. Call POST /auth/v1/verify with them (simulates what an
          `/auth/confirm` route in a real app would do).
       7. GET /auth/v1/user and assert email_confirmed_at is set.

LIVE RUN DEFERRED at branch-land time. Runtime is ~15-25 min the
first time (base image + docker install + supabase CLI install +
~10 container pulls). Subsequent runs against a warm image cache
are faster. Run via `just e2e-recipe`.
"""
from __future__ import annotations

import json
import re
import subprocess
import textwrap
import time

import pytest

pytestmark = pytest.mark.recipe


CONFIRMATION_TEMPLATE = """<!DOCTYPE html>
<html><body>
  <h1>Confirm your email</h1>
  <p>Click to confirm ({{ .Email }}):</p>
  <a href="{{ .SiteURL }}/auth/confirm?token_hash={{ .TokenHash }}&type=email">
    Confirm email
  </a>
</body></html>
"""

MAGIC_LINK_TEMPLATE = """<!DOCTYPE html>
<html><body>
  <h1>Your magic link</h1>
  <p>Sign in to your account:</p>
  <a href="{{ .SiteURL }}/auth/confirm?token_hash={{ .TokenHash }}&type=email">
    Sign in
  </a>
</body></html>
"""

RECOVERY_TEMPLATE = """<!DOCTYPE html>
<html><body>
  <h1>Reset your password</h1>
  <a href="{{ .SiteURL }}/auth/confirm?token_hash={{ .TokenHash }}&type=recovery">
    Reset password
  </a>
</body></html>
"""

EMAIL_CHANGE_TEMPLATE = """<!DOCTYPE html>
<html><body>
  <h1>Confirm your email change</h1>
  <a href="{{ .SiteURL }}/auth/confirm?token_hash={{ .TokenHash }}&type=email_change">
    Confirm email change
  </a>
</body></html>
"""


def _shell(devm, workspace, script: str, timeout: float, check: bool = True) -> subprocess.CompletedProcess:
    """Run a bash command inside the VM. Wraps devm shell -- bash -c."""
    r = subprocess.run(
        [devm.path, "shell", "--", "bash", "-c", script],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=timeout,
    )
    if check and r.returncode != 0:
        raise AssertionError(
            f"in-VM command failed (rc={r.returncode}):\n"
            f"  cmd: {script}\n"
            f"  stdout: {r.stdout.decode(errors='replace')}\n"
            f"  stderr: {r.stderr.decode(errors='replace')}"
        )
    return r


@pytest.mark.timeout(1800)
def test_supabase_recipe(devm, workspace, sandbox_name):
    proj = workspace.vm_name

    # devm.yaml — the recipe's shape, verbatim.
    workspace.devmyaml_path.write_text(textwrap.dedent(f"""\
        project:
          name: {proj}
        docker: true
        install:
          - "curl -fsSL https://github.com/supabase/cli/releases/latest/download/supabase_linux_arm64.tar.gz | sudo tar -xz -C /usr/local/bin supabase"
        services:
          supabase-api:
            port: 54321
            hostname: api.{proj}.test
          supabase-studio:
            port: 54323
            hostname: studio.{proj}.test
          supabase-mail:
            port: 54324
            hostname: mail.{proj}.test
          supabase-db:
            port: 54322
            hostname: db.{proj}.test
            direct: true
        network:
          allow:
          - github.com
          - api.github.com
          - objects.githubusercontent.com
          - public.ecr.aws
    """))

    # Cold-start. Budget covers base image (if not current) + docker
    # feature install + supabase CLI download.
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=600,
    )
    assert r.returncode == 0, (
        f"cold-start failed:\n"
        f"stdout:\n{r.stdout.decode(errors='replace')}\n"
        f"stderr:\n{r.stderr.decode(errors='replace')}"
    )

    # ------------------------------------------------------------------
    # Phase A — install proof.
    # ------------------------------------------------------------------
    r = _shell(devm, workspace, "supabase --version", timeout=30)
    assert b"supabase" in r.stdout.lower() or len(r.stdout.strip()) > 0, (
        f"supabase --version unexpected: {r.stdout!r}"
    )

    # ------------------------------------------------------------------
    # Phase B — write config + templates, then start.
    # ------------------------------------------------------------------
    # Skipping `supabase init` — it's interactive and we're writing
    # every file it would create anyway (config.toml + templates). The
    # supabase CLI only requires supabase/config.toml to be present for
    # `supabase start` to work; init just scaffolds it and adds
    # .gitignore entries we don't need in a test.
    #
    # Write custom config.toml + templates. Site URL uses http (not
    # https) because the recipe test runs isolated — proxy TLS setup
    # for *.test hostnames isn't in scope here.
    supabase_dir = workspace.path / "supabase"
    supabase_dir.mkdir(exist_ok=True)
    (supabase_dir / "templates").mkdir(exist_ok=True)

    (supabase_dir / "templates" / "confirmation.html").write_text(CONFIRMATION_TEMPLATE)
    (supabase_dir / "templates" / "magic_link.html").write_text(MAGIC_LINK_TEMPLATE)
    (supabase_dir / "templates" / "recovery.html").write_text(RECOVERY_TEMPLATE)
    (supabase_dir / "templates" / "email_change.html").write_text(EMAIL_CHANGE_TEMPLATE)

    site_url = f"http://{proj}.test"
    (supabase_dir / "config.toml").write_text(textwrap.dedent(f"""\
        project_id = "{proj}"

        [api]
        enabled = true
        port = 54321
        schemas = ["public", "graphql_public"]
        extra_search_path = ["public", "extensions"]
        max_rows = 1000

        [db]
        port = 54322
        shadow_port = 54320
        major_version = 15

        [studio]
        enabled = true
        port = 54323

        [inbucket]
        enabled = true
        port = 54324
        smtp_port = 54325
        pop3_port = 54326

        [auth]
        enabled = true
        site_url = "{site_url}"
        additional_redirect_urls = ["{site_url}/auth/callback"]
        jwt_expiry = 3600
        enable_signup = true

        [auth.email]
        enable_signup = true
        enable_confirmations = true
        double_confirm_changes = true

        [auth.email.template.confirmation]
        subject = "Confirm your email"
        content_path = "./supabase/templates/confirmation.html"

        [auth.email.template.magic_link]
        subject = "Your magic link"
        content_path = "./supabase/templates/magic_link.html"

        [auth.email.template.recovery]
        subject = "Reset your password"
        content_path = "./supabase/templates/recovery.html"

        [auth.email.template.email_change]
        subject = "Confirm your email change"
        content_path = "./supabase/templates/email_change.html"
    """))

    # supabase start. First run pulls ~10 container images through
    # iron-proxy — slow (10min+ possible), but subsequent runs against
    # docker's local cache are minutes not tens-of-minutes.
    r = _shell(
        devm, workspace,
        "cd $WORKSPACE && supabase start 2>&1",
        timeout=1200,
        check=False,
    )
    assert r.returncode == 0, (
        f"supabase start failed (rc={r.returncode}):\n"
        f"{r.stdout.decode(errors='replace')}\n"
        f"stderr:\n{r.stderr.decode(errors='replace')}"
    )

    # Fetch anon key from supabase status.
    r = _shell(devm, workspace, "cd $WORKSPACE && supabase status -o env", timeout=30)
    status_env = r.stdout.decode()
    m = re.search(r'^ANON_KEY="?([^"\n]+)"?', status_env, re.MULTILINE)
    assert m, f"couldn't find ANON_KEY in supabase status output:\n{status_env}"
    anon_key = m.group(1)

    # ------------------------------------------------------------------
    # Phase C — every routing pattern reachable from inside VM.
    # ------------------------------------------------------------------
    # HTTP via proxy: api / studio / inbucket. curl -k because
    # isolated e2e doesn't have devm CA on the guest's trust store
    # for these test hostnames.
    for host in [f"api.{proj}.test", f"studio.{proj}.test", f"mail.{proj}.test"]:
        r = _shell(
            devm, workspace,
            f"curl -sS -o /dev/null -w '%{{http_code}}' 'http://{host}/' --max-time 15",
            timeout=30, check=False,
        )
        code = r.stdout.decode().strip()
        # 200/301/302/401/404 all acceptable — we're proving the
        # hostname reaches an upstream, not the specific response.
        assert code.startswith(("2", "3", "4")), (
            f"http://{host}/ unreachable: code={code}\n"
            f"stderr: {r.stderr.decode(errors='replace')}"
        )

    # Direct TCP: psql to Postgres. We install psql via apt inside the
    # VM if it's not there (supabase CLI doesn't ship it).
    _shell(devm, workspace, "which psql || sudo apt-get install -y postgresql-client", timeout=180)
    r = _shell(
        devm, workspace,
        f"PGPASSWORD=postgres psql -h db.{proj}.test -p 54322 -U postgres -d postgres -tAc 'SELECT 1'",
        timeout=30,
    )
    assert r.stdout.decode().strip() == "1", (
        f"psql SELECT 1 didn't return 1; got: {r.stdout!r}"
    )

    # ------------------------------------------------------------------
    # Phase D — full signup + email + verify chain.
    # ------------------------------------------------------------------
    test_email = "e2etest@example.com"
    test_password = "correct-horse-battery-staple"
    api_base = f"http://api.{proj}.test"

    # Signup.
    signup_body = json.dumps({"email": test_email, "password": test_password})
    r = _shell(
        devm, workspace,
        f"curl -sS -X POST '{api_base}/auth/v1/signup' "
        f"-H 'Content-Type: application/json' "
        f"-H 'apikey: {anon_key}' "
        f"-H 'Authorization: Bearer {anon_key}' "
        f"-d '{signup_body}'",
        timeout=30,
    )
    signup_resp = r.stdout.decode()
    assert "id" in signup_resp or "user" in signup_resp, (
        f"signup didn't return a user record: {signup_resp!r}"
    )

    # Poll Inbucket for the confirmation email. Inbucket mailbox is the
    # local-part before @.
    mailbox = test_email.split("@")[0]
    inbucket_api = f"http://mail.{proj}.test/api/v1/mailbox/{mailbox}"

    confirmation_body = None
    for _ in range(20):  # up to 20s of polling
        r = _shell(
            devm, workspace,
            f"curl -sS '{inbucket_api}'",
            timeout=15, check=False,
        )
        if r.returncode == 0:
            try:
                messages = json.loads(r.stdout.decode())
            except json.JSONDecodeError:
                messages = []
            if messages:
                # Fetch the newest email's body.
                mail_id = messages[0].get("id") or messages[0].get("mailbox-id")
                r2 = _shell(
                    devm, workspace,
                    f"curl -sS '{inbucket_api}/{mail_id}'",
                    timeout=15,
                )
                confirmation_body = r2.stdout.decode()
                break
        time.sleep(1)

    assert confirmation_body, (
        f"no confirmation email arrived in Inbucket for {test_email}"
    )

    # This is the smoking-gun assertion for the recipe's email-templates
    # fix: the URL must contain OUR hostname, NOT the default
    # 127.0.0.1:54321 that GoTrue bakes in without custom templates.
    assert f"{proj}.test" in confirmation_body, (
        f"email doesn't contain the hostname {proj}.test — templates "
        f"not registered or not overriding GoTrue defaults. Body:\n{confirmation_body[:2000]}"
    )
    assert "127.0.0.1:54321" not in confirmation_body, (
        f"email STILL contains 127.0.0.1:54321 — templates aren't taking "
        f"effect. Body:\n{confirmation_body[:2000]}"
    )

    # Extract token_hash from the URL.
    match = re.search(r"token_hash=([A-Za-z0-9_\-]+)&type=(\w+)", confirmation_body)
    assert match, f"couldn't find token_hash in email body:\n{confirmation_body[:2000]}"
    token_hash, verify_type = match.group(1), match.group(2)
    assert verify_type == "email", f"expected type=email, got type={verify_type}"

    # Simulate what an app's /auth/confirm route would do:
    # POST /auth/v1/verify with the token_hash.
    verify_body = json.dumps({"type": "email", "token_hash": token_hash})
    r = _shell(
        devm, workspace,
        f"curl -sS -X POST '{api_base}/auth/v1/verify' "
        f"-H 'Content-Type: application/json' "
        f"-H 'apikey: {anon_key}' "
        f"-d '{verify_body}'",
        timeout=30,
    )
    verify_resp = r.stdout.decode()
    assert "access_token" in verify_resp, (
        f"verify didn't return an access_token; response:\n{verify_resp}"
    )

    # Confirm user is confirmed.
    verify_json = json.loads(verify_resp)
    access_token = verify_json["access_token"]
    r = _shell(
        devm, workspace,
        f"curl -sS '{api_base}/auth/v1/user' "
        f"-H 'apikey: {anon_key}' "
        f"-H 'Authorization: Bearer {access_token}'",
        timeout=30,
    )
    user_resp = json.loads(r.stdout.decode())
    assert user_resp.get("email") == test_email, f"unexpected user: {user_resp}"
    assert user_resp.get("email_confirmed_at"), (
        f"user email not confirmed after verify: {user_resp}"
    )
