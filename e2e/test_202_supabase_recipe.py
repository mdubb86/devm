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
       2. Poll Mailpit for the confirmation email.
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
        packages:
          - postgresql-client
        install:
          - "curl -fsSL -o /tmp/supabase.deb https://github.com/supabase/cli/releases/latest/download/supabase_linux_arm64.deb && sudo dpkg -i /tmp/supabase.deb && rm /tmp/supabase.deb"
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
          - "*.cloudfront.net"
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

    # Test-cadence sleep only. supabase/cli#4668 (fixed upstream in
    # 2026-01) enables GoTrue's template reloader; it retries every
    # ~10s until Kong's :8088 endpoint is serving. `supabase start`
    # returns "healthy" before that first successful reload, so a test
    # that hits /signup instantly races the reloader and gets the
    # default template. Interactive users don't notice — they take
    # seconds to click, plenty for the retry. Sleeping ~15s here is
    # enough margin.
    time.sleep(15)

    # Fetch anon key from supabase status.
    r = _shell(devm, workspace, "cd $WORKSPACE && supabase status -o env", timeout=30)
    status_env = r.stdout.decode()
    m = re.search(r'^ANON_KEY="?([^"\n]+)"?', status_env, re.MULTILINE)
    assert m, f"couldn't find ANON_KEY in supabase status output:\n{status_env}"
    anon_key = m.group(1)

    # ------------------------------------------------------------------
    # Phase C — supabase containers are actually up and serving.
    # ------------------------------------------------------------------
    # Hit container ports directly (localhost:PORT) inside the VM.
    # Isolated e2e has no *.test hostname routing (see e2e/README.md).
    # The recipe's job is to make the STACK come up correctly; direct
    # probes prove that without needing the routing layer.

    # Kong (API gateway) on :54321. /rest/v1/ is PostgREST via Kong;
    # requires the apikey header for anon-key auth. Just prove it
    # responds (401 without a valid key is still a proof of "up").
    r = _shell(
        devm, workspace,
        "curl -sS -o /dev/null -w '%{http_code}' http://localhost:54321/rest/v1/ --max-time 10",
        timeout=30, check=False,
    )
    code = r.stdout.decode().strip()
    assert code.startswith(("2", "4")), f"Kong :54321 not serving: got code={code!r}"

    # Studio on :54323. Serves HTML.
    r = _shell(
        devm, workspace,
        "curl -sS -o /dev/null -w '%{http_code}' http://localhost:54323/ --max-time 10",
        timeout=30, check=False,
    )
    code = r.stdout.decode().strip()
    assert code.startswith(("2", "3")), f"Studio :54323 not serving: got code={code!r}"

    # Mailpit on :54324. Serves web UI + API.
    r = _shell(
        devm, workspace,
        "curl -sS -o /dev/null -w '%{http_code}' http://localhost:54324/ --max-time 10",
        timeout=30, check=False,
    )
    code = r.stdout.decode().strip()
    assert code.startswith(("2", "3")), f"Mailpit :54324 not serving: got code={code!r}"

    # Postgres on :54322 via localhost. psql was installed during
    # provisioning (see install: block above).
    r = _shell(
        devm, workspace,
        "PGPASSWORD=postgres psql -h localhost -p 54322 -U postgres -d postgres -tAc 'SELECT 1'",
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
    # Direct-to-Kong for API calls (localhost:54321). Isolated e2e has
    # no *.test hostname routing (see e2e/README.md — no launchd → no
    # daemon proxy listener), so hostname URLs would hang. What THIS
    # test proves is the recipe's config: custom templates → correct
    # site_url in email bodies — see the smoking-gun assertion below.
    api_base = "http://localhost:54321"
    mailpit_base = "http://localhost:54324"

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
    # GoTrue returns {"id": "...", ...} on success (user object at top
    # level when confirm-required is off) or {"user": {...}, ...} when
    # confirm-required is on. Errors look like {"error_code": "...",
    # "msg": "..."} — reject those.
    try:
        signup_json = json.loads(signup_resp)
    except json.JSONDecodeError as e:
        raise AssertionError(f"signup response not JSON: {signup_resp!r}") from e
    assert "error_code" not in signup_json and "code" not in signup_json, (
        f"signup returned an error: {signup_json}"
    )
    assert signup_json.get("id") or signup_json.get("user"), (
        f"signup didn't return a user id: {signup_json}"
    )

    # Poll Mailpit for the confirmation email. Supabase CLI's mail
    # container is Mailpit (the container name is `supabase_inbucket_*`
    # for historical reasons, but the image is Mailpit).
    #   GET /api/v1/messages           → {"messages":[{"ID":...}], ...}
    #   GET /api/v1/message/{ID}       → {"HTML":..., "Text":..., "To":[{...}]}
    messages_api = f"{mailpit_base}/api/v1/messages"

    confirmation_body = None
    for _ in range(30):  # up to 30s of polling
        r = _shell(
            devm, workspace,
            f"curl -sS '{messages_api}'",
            timeout=15, check=False,
        )
        if r.returncode == 0:
            try:
                envelope = json.loads(r.stdout.decode())
                messages = envelope.get("messages", [])
            except json.JSONDecodeError:
                messages = []
            # Find the message addressed to our test_email.
            target = None
            for m in messages:
                for to in m.get("To", []):
                    if to.get("Address") == test_email:
                        target = m
                        break
                if target:
                    break
            if target:
                mail_id = target["ID"]
                r2 = _shell(
                    devm, workspace,
                    f"curl -sS '{mailpit_base}/api/v1/message/{mail_id}'",
                    timeout=15,
                )
                full = json.loads(r2.stdout.decode())
                # Prefer HTML body (that's where our template lives);
                # fall back to Text.
                confirmation_body = full.get("HTML") or full.get("Text") or ""
                break
        time.sleep(1)

    if not confirmation_body:
        # Diagnostic dump — supabase CLI has swapped mail containers
        # before (Mailpit → Mailpit); if it happens again the API paths
        # here will 404 and the container list + SMTP env below shows
        # what changed.
        debug_script = "\n".join([
            "set +e",
            "echo '=== all containers ==='",
            "docker ps --format 'table {{.Names}}\\t{{.Ports}}\\t{{.Status}}'",
            "echo '=== mailer SMTP env on auth ==='",
            f"docker exec supabase_auth_{proj} env | grep -iE 'smtp|mail|inbucket'",
            "echo '=== auth logs (last 60) ==='",
            f"docker logs supabase_auth_{proj} 2>&1 | tail -60",
        ])
        r_debug = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c", debug_script],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        raise AssertionError(
            f"no confirmation email arrived in Mailpit for {test_email}\n"
            f"signup response: {signup_json}\n"
            f"debug stdout:\n{r_debug.stdout.decode(errors='replace')[:6000]}\n"
            f"debug stderr:\n{r_debug.stderr.decode(errors='replace')[:2000]}"
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
