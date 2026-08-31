"""202: Supabase recipe end-to-end (full stack + browser auth flow).

Proves recipes/service/supabase.md works on a real Tart VM by
exercising every promise the recipe makes:

  A. Install works. `supabase --version` returns 0.

  B. `supabase start` brings up the full stack (~10 containers), then
     the anon key is read from `supabase status`.

  C. Per-service HOSTNAME routing (recipe's core promise) actually
     reaches each container. Mac-side curl to each declared hostname
     resolves through the daemon proxy and back to the right
     container:
       - https://api.<proj>.e2e.test/          → Kong (via TLS chain)
       - http://studio.<proj>.e2e.test/        → Studio HTML
       - http://mail.<proj>.e2e.test/          → Mailpit UI
       - psql db.<proj>.e2e.test:54322         → direct-mode Postgres
     Validates `services:` routing + `direct: true` + TLS chain.

  D. Full auth email flow via API. Signup, poll Mailpit, extract link,
     assert URL is HOSTNAME (not 127.0.0.1) — smoking gun for the
     recipe's custom-template fix. Then verify → user confirmed.

  E. Browser click-through via Playwright — Chromium in the VM
     navigates the emailed HTTPS confirmation link, lands on the
     app's /auth/confirm handler (a tiny http.server this test spins
     up as a `services:` exec), and receives the token_hash. Proves
     the full recipe-shape flow the way a real user would experience
     it: NSS trust (v0.15.1 install.sh seed) + hostname routing +
     custom-template landing convention.

Playwright is used ONLY as a validation probe here — the recipe
doesn't teach Playwright itself (that lives in
recipes/tool/playwright.md); this test uses it to prove the
supabase recipe's browser-facing promises hold end-to-end.

Runtime is ~15-25 min the first time (base image + docker install +
supabase CLI install + ~10 container pulls + pip install playwright
+ chromium browser download). Subsequent runs against a warm cache
are faster. Run via `just e2e test_202_supabase_recipe`.
"""
from __future__ import annotations

import json
import re
import subprocess
import textwrap
import time

import pytest

pytestmark = pytest.mark.recipe


# --- Custom email templates: link points at the APP's /auth/confirm
# --- (built from {{ .SiteURL }} + {{ .TokenHash }}), not GoTrue's
# --- default {{ .ConfirmationURL }} which bakes in 127.0.0.1:54321.
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

# --- Minimal in-VM /auth/confirm handler. A real app would call
# --- supabase.auth.verifyOtp; here we just prove the emailed link
# --- reaches something on our hostname, so Playwright can observe
# --- title + path.
CONFIRM_HANDLER = '''
from http.server import HTTPServer, BaseHTTPRequestHandler

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.end_headers()
        body = (
            "<!DOCTYPE html><html><head><title>Auth Confirm</title></head>"
            "<body><p>PATH:" + self.path + "</p></body></html>"
        )
        self.wfile.write(body.encode("utf-8"))
    def log_message(self, *args, **kwargs):
        pass

HTTPServer(("0.0.0.0", 5173), Handler).serve_forever()
'''

# --- Playwright probe: launch chromium in the VM, navigate the
# --- emailed link, print status + title + final URL so the outer
# --- assertions can key off them. No ignoreHTTPSErrors — the whole
# --- point is to prove the devm CA is trusted by NSS. If Chromium
# --- rejects the cert, the test fails and we've learned something.
PLAYWRIGHT_PROBE = '''
import sys
from playwright.sync_api import sync_playwright

url = sys.argv[1]
with sync_playwright() as p:
    browser = p.chromium.launch()
    page = browser.new_page()
    response = page.goto(url, wait_until="load", timeout=30000)
    print("STATUS", response.status)
    print("TITLE", page.title())
    print("URL", page.url)
    print("BODY", page.content()[:500])
    browser.close()
'''


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


def _mac_curl(url: str, extra: list[str] | None = None, timeout: float = 15) -> tuple[int, str]:
    """Run curl on the Mac (not in the VM). Returns (http_code, body)."""
    cmd = [
        "curl", "-sS", "-o", "-", "-w", "\n__HTTP_CODE__:%{http_code}",
        "--max-time", "10",
    ]
    if extra:
        cmd += extra
    cmd.append(url)
    r = subprocess.run(cmd, capture_output=True, timeout=timeout)
    out = r.stdout.decode(errors="replace")
    m = re.search(r"\n__HTTP_CODE__:(\d+)\s*$", out)
    if not m:
        return -1, out
    return int(m.group(1)), out[: m.start()]


@pytest.mark.timeout(1800)
def test_supabase_recipe(devm, workspace, sandbox_name):
    proj = workspace.vm_name
    site_url = f"https://{proj}.e2e.test"
    api_ext = f"https://api.{proj}.e2e.test/auth/v1"

    # devm.yaml — the recipe's shape, verbatim, plus a tiny http.server
    # for the /auth/confirm landing page and Playwright's runtime deps.
    workspace.devmyaml_path.write_text(textwrap.dedent(f"""\
        project:
          name: {proj}
        repo:
          url: {workspace.bare_repo_url()}
          secret: e2e_default
        docker: true
        env:
          # Recipe §1: add devm var BEFORE referencing it from config.toml.
          # An unset env(NAME) is a fatal parse error for the whole stack.
          PUBLIC_SITE_URL: {site_url}
          PUBLIC_SUPABASE_AUTH_EXTERNAL_URL: {api_ext}
        packages:
          - postgresql-client
          - chromium                     # Playwright runtime libs (per playwright recipe)
          - python3-venv                 # Playwright install into an isolated venv
        scripts:
          install-supabase:
            - TAG=$(curl -sIL -o /dev/null -w '%{{url_effective}}' https://github.com/supabase/cli/releases/latest | xargs basename)
            - curl -fsSL -o /tmp/supabase.deb "https://github.com/supabase/cli/releases/download/${{TAG}}/supabase_${{TAG#v}}_linux_arm64.deb"
            - sudo dpkg -i /tmp/supabase.deb
            - rm /tmp/supabase.deb
        install:
          - ">install-supabase"
        services:
          # App: a tiny http.server serving /auth/confirm on 5173. Bound
          # to 0.0.0.0 — a loopback bind returns 502 through the proxy
          # (recipe Notes).
          app:
            port: 5173
            hostname: {proj}.e2e.test
            exec: ["python3", "{workspace.path}/confirm_handler.py"]
            restart: always
          supabase-api:
            port: 54321
            hostname: api.{proj}.e2e.test
          supabase-studio:
            port: 54323
            hostname: studio.{proj}.e2e.test
          supabase-mail:
            port: 54324
            hostname: mail.{proj}.e2e.test
          supabase-db:
            port: 54322
            hostname: db.{proj}.e2e.test
            direct: true
        network:
          allow:
          - github.com
          - objects.githubusercontent.com
          - public.ecr.aws
          # The recipe defaults to `public.ecr.aws` alone and opens a
          # `devm passthrough` window for the CloudFront layer blobs.
          # That's a supervised, human-in-the-loop step, so this test
          # takes the recipe's documented alternative — the standing
          # wildcard — to stay unattended.
          - "*.cloudfront.net"
          # Playwright CDN + mirror (recipes/tool/playwright.md).
          - cdn.playwright.dev
          - playwright.download.prss.microsoft.com
          # pip install playwright (PyPI + wheels).
          - pypi.org
          - files.pythonhosted.org
    """))

    # Handler script — served via the app service above. Written into the
    # primary volume's Mac-side storage (workspace.path is just the Mac
    # cwd holding devm.yaml, not shared with the guest) so the guest sees
    # it at $WORKSPACE via the live virtiofs share.
    workspace.volume_path().mkdir(parents=True, exist_ok=True)
    (workspace.volume_path() / "confirm_handler.py").write_text(CONFIRM_HANDLER)
    (workspace.volume_path() / "pw_probe.py").write_text(PLAYWRIGHT_PROBE)

    # Cold-start.
    r = subprocess.run(
        [devm.path, "start"],
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
    # every file it would create anyway. Written into the primary
    # volume's Mac-side storage — see confirm_handler.py note above.
    supabase_dir = workspace.volume_path() / "supabase"
    supabase_dir.mkdir(exist_ok=True)
    (supabase_dir / "templates").mkdir(exist_ok=True)

    (supabase_dir / "templates" / "confirmation.html").write_text(CONFIRMATION_TEMPLATE)
    (supabase_dir / "templates" / "magic_link.html").write_text(MAGIC_LINK_TEMPLATE)
    (supabase_dir / "templates" / "recovery.html").write_text(RECOVERY_TEMPLATE)
    (supabase_dir / "templates" / "email_change.html").write_text(EMAIL_CHANGE_TEMPLATE)

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

        [local_smtp]
        enabled = true
        port = 54324
        smtp_port = 54325
        pop3_port = 54326

        [auth]
        enabled = true
        site_url = "env(PUBLIC_SITE_URL)"
        external_url = "env(PUBLIC_SUPABASE_AUTH_EXTERNAL_URL)"
        additional_redirect_urls = ["env(PUBLIC_SITE_URL)"]
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

    # supabase start.
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

    # supabase/cli#4668 (fixed 2026-01): GoTrue's template reloader
    # retries every ~10s until Kong's :8088 endpoint is serving.
    # `supabase start` returns "healthy" before the first successful
    # reload — sleeping ~15s here is enough margin.
    time.sleep(15)

    # Fetch anon key from supabase status.
    r = _shell(devm, workspace, "cd $WORKSPACE && supabase status -o env", timeout=30)
    status_env = r.stdout.decode()
    m = re.search(r'^ANON_KEY="?([^"\n]+)"?', status_env, re.MULTILINE)
    assert m, f"couldn't find ANON_KEY in supabase status output:\n{status_env}"
    anon_key = m.group(1)

    # ------------------------------------------------------------------
    # Phase C — hostname routing (recipe's core promise).
    # ------------------------------------------------------------------
    # Mac-side probes. Each hostname must route via the daemon proxy
    # into the corresponding container. Kong on HTTPS also validates
    # the TLS chain (proxy re-signs with devm-e2e CA; curl trusts it
    # via the OpenSSL system bundle installed at devm install-time).
    code, body = _mac_curl(f"https://api.{proj}.e2e.test/")
    assert code in (200, 401, 404), (
        f"https://api.{proj}.e2e.test/ (Kong) unreachable: code={code}, body={body[:300]!r}"
    )
    code, body = _mac_curl(f"http://studio.{proj}.e2e.test/")
    assert code in (200, 301, 302, 307), (
        f"http://studio.{proj}.e2e.test/ (Studio) unreachable: code={code}, body={body[:300]!r}"
    )
    code, body = _mac_curl(f"http://mail.{proj}.e2e.test/")
    assert code == 200, (
        f"http://mail.{proj}.e2e.test/ (Mailpit) unreachable: code={code}, body={body[:300]!r}"
    )
    # Direct-mode Postgres: TCP passthrough, not HTTP. Reachable from
    # the Mac at db.<proj>.e2e.test:54322. Probed via TCP + a minimal
    # Postgres SSLRequest handshake (avoids requiring psql on the Mac
    # PATH); a Postgres server responds with a single 'S' or 'N' byte.
    import socket
    import struct
    try:
        with socket.create_connection((f"db.{proj}.e2e.test", 54322), timeout=5) as s:
            # SSLRequest: length=8, code=80877103 (0x04D2162F).
            s.sendall(struct.pack("!II", 8, 80877103))
            resp = s.recv(1)
    except OSError as e:
        raise AssertionError(
            f"direct: true routing broken: TCP connect to db.{proj}.e2e.test:54322 "
            f"from Mac failed: {e}"
        )
    assert resp in (b"S", b"N"), (
        f"direct: true reached something, but not Postgres: got byte {resp!r} "
        f"(expected 'S' or 'N' in reply to SSLRequest)"
    )

    # ------------------------------------------------------------------
    # Phase D — signup, poll Mailpit, extract link, assert hostname.
    # ------------------------------------------------------------------
    test_email = "e2etest@example.com"
    test_password = "correct-horse-battery-staple"
    # Hit Kong via the routed hostname now that Phase C proved it works.
    api_base = f"https://api.{proj}.e2e.test"
    mailpit_base = f"http://mail.{proj}.e2e.test"

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

    # Poll Mailpit for the confirmation email.
    messages_api = f"{mailpit_base}/api/v1/messages"
    confirmation_body = None
    for _ in range(30):
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
                confirmation_body = full.get("HTML") or full.get("Text") or ""
                break
        time.sleep(1)

    if not confirmation_body:
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

    # Smoking-gun: URL must be OUR hostname, not GoTrue's default.
    assert f"{proj}.e2e.test" in confirmation_body, (
        f"email doesn't contain the hostname {proj}.e2e.test — templates "
        f"not registered or not overriding GoTrue defaults. Body:\n{confirmation_body[:2000]}"
    )
    assert "127.0.0.1:54321" not in confirmation_body, (
        f"email STILL contains 127.0.0.1:54321 — templates aren't taking "
        f"effect. Body:\n{confirmation_body[:2000]}"
    )

    # Extract the confirmation URL. HTML-unescape first because raw
    # HTML uses &amp; between query params (per feedback §6).
    import html
    email_unescaped = html.unescape(confirmation_body)
    link_match = re.search(
        rf"(https://{re.escape(proj)}\.e2e\.test/auth/confirm\?token_hash=[A-Za-z0-9_\-]+&type=\w+)",
        email_unescaped,
    )
    assert link_match, (
        f"couldn't find confirmation URL in email body:\n{email_unescaped[:2000]}"
    )
    confirm_url = link_match.group(1)
    tok_match = re.search(r"token_hash=([A-Za-z0-9_\-]+)&type=(\w+)", confirm_url)
    token_hash, verify_type = tok_match.group(1), tok_match.group(2)
    assert verify_type == "email", f"expected type=email, got type={verify_type}"

    # API-level verify (mirrors what an /auth/confirm handler does
    # server-side). Confirms the token GoTrue baked in is usable.
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

    # ------------------------------------------------------------------
    # Phase E — browser click-through via Playwright.
    # ------------------------------------------------------------------
    # Sign up a second user so the confirmation URL is fresh (the one
    # from Phase D is single-use and already consumed by the verify
    # call above). Then have Chromium in the VM navigate the emailed
    # link and land on our /auth/confirm handler over HTTPS.
    pw_email = "e2epw@example.com"
    signup_body = json.dumps({"email": pw_email, "password": test_password})
    r = _shell(
        devm, workspace,
        f"curl -sS -X POST '{api_base}/auth/v1/signup' "
        f"-H 'Content-Type: application/json' "
        f"-H 'apikey: {anon_key}' "
        f"-H 'Authorization: Bearer {anon_key}' "
        f"-d '{signup_body}'",
        timeout=30,
    )
    assert r.returncode == 0 and "error_code" not in r.stdout.decode(), (
        f"second signup failed: {r.stdout.decode()!r}"
    )

    # Poll for the second user's email.
    pw_confirm_url = None
    for _ in range(30):
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
            target = None
            for m in messages:
                for to in m.get("To", []):
                    if to.get("Address") == pw_email:
                        target = m
                        break
                if target:
                    break
            if target:
                r2 = _shell(
                    devm, workspace,
                    f"curl -sS '{mailpit_base}/api/v1/message/{target['ID']}'",
                    timeout=15,
                )
                full = json.loads(r2.stdout.decode())
                body2 = html.unescape(full.get("HTML") or full.get("Text") or "")
                m2 = re.search(
                    rf"(https://{re.escape(proj)}\.e2e\.test/auth/confirm\?token_hash=[A-Za-z0-9_\-]+&type=\w+)",
                    body2,
                )
                if m2:
                    pw_confirm_url = m2.group(1)
                    break
        time.sleep(1)
    assert pw_confirm_url, "no Playwright-target confirmation URL arrived"

    # Install Playwright in an isolated venv (PEP 668 blocks system
    # pip on Debian 13). Then download the pinned Chromium build via
    # the two allowlisted CDN hosts.
    _shell(
        devm, workspace,
        "python3 -m venv $HOME/pw-venv && "
        "$HOME/pw-venv/bin/pip install --quiet playwright && "
        "$HOME/pw-venv/bin/playwright install chromium",
        timeout=600,
    )

    # Run the probe. No --ignoreHTTPSErrors: this proves the devm CA
    # is trusted by NSS (v0.15.1 seeded $HOME/.pki/nssdb via install.sh).
    r = _shell(
        devm, workspace,
        f"$HOME/pw-venv/bin/python {workspace.path}/pw_probe.py '{pw_confirm_url}'",
        timeout=60,
    )
    probe_out = r.stdout.decode()

    status_m = re.search(r"^STATUS (\d+)$", probe_out, re.MULTILINE)
    title_m = re.search(r"^TITLE (.+)$", probe_out, re.MULTILINE)
    url_m = re.search(r"^URL (.+)$", probe_out, re.MULTILINE)
    body_m = re.search(r"^BODY (.+)$", probe_out, re.MULTILINE)
    assert status_m and title_m and url_m and body_m, (
        f"playwright probe didn't emit expected fields:\n{probe_out}"
    )
    assert status_m.group(1) == "200", (
        f"playwright hit non-200 on confirmation URL: STATUS={status_m.group(1)}\n{probe_out}"
    )
    assert title_m.group(1).strip() == "Auth Confirm", (
        f"landing page wasn't our handler: TITLE={title_m.group(1)!r}\n{probe_out}"
    )
    assert re.search(r"token_hash=([A-Za-z0-9_\-]+)", url_m.group(1)), (
        f"final URL missing token_hash: URL={url_m.group(1)!r}\n{probe_out}"
    )
    # The handler echoes the request PATH into the body — proves it
    # actually ran, not just any 200 from something in the chain.
    assert "PATH:/auth/confirm" in body_m.group(1), (
        f"handler didn't process the request path: BODY={body_m.group(1)!r}\n{probe_out}"
    )
