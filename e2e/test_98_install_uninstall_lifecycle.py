"""39: combined install/uninstall lifecycle — one install + one uninstall,
three verification blocks against the single instance.

This test replaces the former test_39 (DNS), test_40 (HTTPS proxy), and
test_41 (Tart + LaunchDaemon). Splitting them into three separate tests
gave each its own install/uninstall cycle, and each such cycle fires a
macOS Touch ID prompt on the privileged sudo call (security
add-trusted-cert + launchctl bootstrap + resolver write, all batched
into one `sudo bash -c …` block). Touch ID never caches — the timestamp
does, but certain sudo invocations touching restricted entitlements
always require a fresh authentication regardless. Three separate
lifecycles = 6-9 Touch ID prompts per suite run, spread across ~10min
of unattended runtime; a prompt the user misses = suite failure.

Collapsing to one lifecycle costs 3 Touch ID prompts (pre-clean
uninstall + install + final uninstall) all clustered up front while the
user is watching phase 2a start.

Coverage preserved (adapted to the devm-e2e identity: TLD `e2e.test`,
DNS `:51154`) from the three former tests:

  DNS (former test_39):
    - `devm install` writes /etc/resolver/e2e.test with the expected content.
    - `dig @127.0.0.1 -p 51154 anything.e2e.test` returns 127.0.0.1.
    - System-resolver path: socket.gethostbyname("*.e2e.test") → 127.0.0.1.
    - `devm uninstall` removes /etc/resolver/e2e.test.

  HTTPS proxy (former test_40):
    - CA install lets curl trust https://<hostname>.e2e.test.
    - `devm route local` binds the hostname to a Mac-side backend.
    - HTTP (:80) and HTTPS (:443) both proxy through with 200 body.
    - Backend killed → 502 with "no service listening" diagnostic.

  LaunchDaemon (former test_41 — subset):
    - LaunchDaemon plist at /Library/LaunchDaemons/, not the old
      LaunchAgent path (Ship 4.2 pin).
    - `launchctl print system/com.devm.e2e.service` reaches
      `state = running` within 10s, daemon runs as the invoking user
      (not root).
    - Ports 80/443 are TCP-bindable (LaunchDaemon socket activation
      pins them — Ship 4.2 fix for the Ship 3/4 unbound-fd bug).
    - `devm uninstall` removes the LaunchDaemon plist and runtime dir.

Former test_41's cold-start-VM + teardown block is dropped here; that
coverage is redundant with test_50 (cold-start brings VM to running,
also guest identity after cold-start) and test_53 (teardown destroys
VM).

Runs against the bootstrapped devm-e2e identity throughout
(internal/identity.E2E): TLD `e2e.test`, DNS `:51154`, group
`_devm-e2e`, LaunchDaemon labels `com.devm.e2e.service` /
`com.devm.e2e.helper` — distinct from prod's so this lifecycle test
never collides with a real `devm install` on the same Mac.
"""
from __future__ import annotations

import os
import platform
import shutil
import socket
import subprocess
import time
import urllib.request
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


_RESOLVER_FILE = "/etc/resolver/e2e.test"
_LAUNCH_DAEMON_PLIST = Path("/Library/LaunchDaemons/com.devm.e2e.service.plist")
_LAUNCH_AGENT_PLIST = Path("~/Library/LaunchAgents/com.devm.e2e.service.plist").expanduser()

# Task 6 (per-project bind isolation): the root helper LaunchDaemon
# installed/removed alongside the main service — grants the lo0-alias
# / loopback-bind privileges devm itself doesn't run as root for.
_HELPER_PLIST = Path("/Library/LaunchDaemons/com.devm.e2e.helper.plist")
_HELPER_BINARY = Path("/usr/local/bin/devm-e2e-helper")
_HELPER_SOCKET = Path("/var/run/devm-e2e-helper.sock")


def _runtime_dir() -> str:
    return os.path.expanduser("~/Library/Application Support/devm-e2e")


def _alloc_port() -> int:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _spawn_backend(port: int, body: str) -> subprocess.Popen:
    """Tiny HTTP server returning `body` for every GET."""
    script = (
        "from http.server import HTTPServer, BaseHTTPRequestHandler\n"
        f"class H(BaseHTTPRequestHandler):\n"
        f"    def do_GET(self):\n"
        f"        self.send_response(200)\n"
        f"        self.send_header('Content-Type','text/plain')\n"
        f"        self.end_headers()\n"
        f"        self.wfile.write({body!r}.encode())\n"
        f"    def log_message(self,*a,**k): pass\n"
        f"HTTPServer(('127.0.0.1',{port}),H).serve_forever()\n"
    )
    return subprocess.Popen(["python3", "-c", script])


def _write_route_yaml(workspace_path: str, project_id: str, hostname: str, port: int) -> None:
    path = os.path.join(workspace_path, "devm.yaml")
    with open(path, "w") as f:
        f.write(
            f"project:\n"
            f"  name: {project_id}\n"
            f"services:\n"
            f"  api:\n"
            f"    hostname: {hostname}\n"
            f"    port: {port}\n"
        )


@pytest.mark.slow
@pytest.mark.timeout(900)  # base image build can take up to 10 min
def test_install_uninstall_lifecycle(devm, workspace, sudo_capable):
    if platform.system() != "Darwin":
        pytest.skip("install/uninstall lifecycle runs on macOS only")
    for tool in ("dig", "python3", "curl"):
        if not shutil.which(tool):
            pytest.skip(f"{tool} not on PATH; needed for the combined lifecycle")

    # Pre-clean: 1st Touch ID prompt (idempotent no-op if already
    # uninstalled — still shells to sudo).
    subprocess.run([devm.path, "uninstall"], capture_output=True, timeout=30)

    backend_port = _alloc_port()
    hostname = f"e2e-{backend_port}.e2e.test"
    backend = _spawn_backend(backend_port, "hello from backend")

    try:
        # --- INSTALL (2nd Touch ID prompt) ---
        # Builds devm-base (cached on subsequent runs), writes resolver
        # file + trusts CA + registers LaunchDaemon + starts service.
        r = subprocess.run(
            [devm.path, "install"],
            capture_output=True, timeout=780, check=False,
        )
        assert r.returncode == 0, (
            f"install failed:\nstdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )

        # --- Root helper block (Task 6: per-project bind isolation) ---
        assert _HELPER_PLIST.exists(), (
            f"helper LaunchDaemon plist not installed at {_HELPER_PLIST}"
        )
        assert _HELPER_BINARY.exists(), (
            f"helper binary not installed at {_HELPER_BINARY}"
        )
        # The helper's UDS is created by the running daemon; give it a beat.
        time.sleep(1)
        assert _HELPER_SOCKET.exists(), (
            f"helper UDS not present at {_HELPER_SOCKET} after install"
        )
        # _devm-e2e group exists.
        r_group = subprocess.run(
            ["dscl", ".", "-read", "/Groups/_devm-e2e"],
            capture_output=True, timeout=10,
        )
        assert r_group.returncode == 0, (
            f"_devm-e2e group not created: {r_group.stderr.decode()!r}"
        )

        # --- DNS block (former test_39) ---
        assert os.path.exists(_RESOLVER_FILE), "resolver file not created"
        with open(_RESOLVER_FILE) as f:
            contents = f.read()
        assert contents == "nameserver 127.0.0.1\nport 51154\n", (
            f"unexpected resolver file contents: {contents!r}"
        )
        r = subprocess.run(
            ["dig", "@127.0.0.1", "-p", "51154", "anything.e2e.test", "+short"],
            capture_output=True, timeout=10,
        )
        assert "127.0.0.1" in r.stdout.decode(), (
            f"direct DNS query failed: {r.stdout.decode()!r}"
        )
        time.sleep(1)  # macOS resolver cache settle
        ip = socket.gethostbyname("anything-system-probe.e2e.test")
        assert ip == "127.0.0.1", (
            f"system resolver returned {ip!r}, expected 127.0.0.1"
        )

        # --- LaunchDaemon block (former test_41) ---
        assert _LAUNCH_DAEMON_PLIST.exists(), (
            "LaunchDaemon plist not installed at /Library/LaunchDaemons/"
        )
        assert not _LAUNCH_AGENT_PLIST.exists(), (
            "old LaunchAgent plist should not be present after Ship 4.2 install"
        )

        # Poll for `state = running` — install returns as soon as launchd
        # accepts the load, but the process transitions through
        # `state = xpcproxy` before reaching `running`.
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            r = subprocess.run(
                ["launchctl", "print", "system/com.devm.e2e.service"],
                capture_output=True, text=True, timeout=10,
            )
            if r.returncode == 0 and "state = running" in r.stdout:
                break
            time.sleep(0.25)
        assert r.returncode == 0, f"launchctl print failed: {r.stderr}"
        assert "state = running" in r.stdout, (
            f"daemon didn't reach `state = running` within 10s:\n{r.stdout}"
        )
        pid_line = [l for l in r.stdout.splitlines() if "pid = " in l]
        assert pid_line, f"no pid in launchctl print output:\n{r.stdout}"
        pid = pid_line[0].split("=")[1].strip()
        ps = subprocess.run(
            ["ps", "-o", "user=", "-p", pid],
            capture_output=True, text=True, timeout=5,
        )
        assert ps.stdout.strip() == os.environ.get("USER"), (
            f"daemon running as {ps.stdout.strip()!r}, "
            f"expected user {os.environ.get('USER')!r}"
        )

        # LaunchDaemon socket activation actually binds :80 and :443.
        # Ship 3/4 gap: LaunchAgent socket activation returned unbound
        # file descriptors. Ship 4.2's LaunchDaemon makes them genuinely
        # bound. Verify with a plain TCP connect.
        for port in (80, 443):
            try:
                s = socket.create_connection(("127.0.0.1", port), timeout=5)
                s.close()
            except ConnectionRefusedError:
                pytest.fail(
                    f"nothing listening on 127.0.0.1:{port} — proxy not "
                    f"bound. Ship 3/4-era LaunchAgent socket-activation bug."
                )
            except OSError as e:
                pytest.fail(f"unexpected error connecting to :{port}: {e}")

        # --- HTTPS proxy roundtrip (former test_40) ---
        # Push the route in local mode (backend on Mac, not in VM).
        _write_route_yaml(str(workspace.path), "e2e-proxy", hostname, backend_port)
        r = subprocess.run(
            [devm.path, "route", "local"],
            capture_output=True, cwd=str(workspace.path), timeout=10,
        )
        assert r.returncode == 0, (
            f"route local: {r.stdout.decode()!r} | {r.stderr.decode()!r}"
        )
        time.sleep(1)  # macOS resolver cache settle

        # HTTP through :80.
        try:
            resp = urllib.request.urlopen(f"http://{hostname}/", timeout=5)
            body = resp.read()
            assert resp.status == 200
            assert body == b"hello from backend", f"HTTP body: {body!r}"
        except Exception as e:
            pytest.fail(f"HTTP request failed: {e}")

        # HTTPS through :443 — curl uses macOS's System Keychain, which
        # includes our devm Local CA (trusted by `devm install`).
        r = subprocess.run(
            ["curl", "-sS", "--max-time", "5", f"https://{hostname}/"],
            capture_output=True,
        )
        assert r.returncode == 0, (
            f"curl https failed: code={r.returncode}, "
            f"stderr={r.stderr.decode()!r}"
        )
        assert r.stdout == b"hello from backend", (
            f"HTTPS body: {r.stdout!r}"
        )

        # Kill backend; expect 502 with friendly diagnostic.
        backend.terminate()
        try:
            backend.wait(timeout=5)
        except subprocess.TimeoutExpired:
            backend.kill()
        backend = None
        time.sleep(0.5)

        r = subprocess.run(
            ["curl", "-sS", "--max-time", "5", "-o", "-",
             "-w", "%{http_code}", f"http://{hostname}/"],
            capture_output=True,
        )
        out = r.stdout.decode()
        assert out.endswith("502"), f"unexpected status: {out}"
        assert "no service listening" in out, f"no diagnostic body: {out}"

        # NOTE: former test_41's cold-start-VM + teardown block dropped
        # from this test. That coverage is redundant with:
        #   test_50 — cold-start brings VM to running, guest identity
        #   test_53 — teardown destroys VM
        # Keeping the LaunchDaemon block above IS the unique contribution
        # from former test_41 (plist location, socket-bound ports,
        # daemon-runs-as-user).

    finally:
        if backend is not None:
            backend.terminate()
            try:
                backend.wait(timeout=5)
            except subprocess.TimeoutExpired:
                backend.kill()

        # --- UNINSTALL (3rd Touch ID prompt) ---
        r = subprocess.run(
            [devm.path, "uninstall"],
            capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"uninstall failed: {r.stderr.decode()!r}"
        )
        assert not os.path.exists(_RESOLVER_FILE), (
            "resolver file not removed by uninstall"
        )
        assert not _LAUNCH_DAEMON_PLIST.exists(), (
            "LaunchDaemon plist still present after uninstall"
        )
        assert not os.path.exists(_runtime_dir()), (
            "devm runtime dir still present after uninstall"
        )

        # --- Root helper teardown (Task 6: per-project bind isolation) ---
        assert not _HELPER_PLIST.exists(), (
            f"helper plist still present after uninstall: {_HELPER_PLIST}"
        )
        assert not _HELPER_BINARY.exists(), (
            f"helper binary still present after uninstall: {_HELPER_BINARY}"
        )
        # Aliases removed. E2E's pool is 127.42.0.21-40 (identity.E2E.PoolStart/
        # PoolEnd) — distinct from prod's 127.42.0.1-20 — so this checks the
        # e2e install's own first alias, not prod's.
        ifconfig = subprocess.check_output(["/sbin/ifconfig", "lo0"], text=True)
        assert "127.42.0.21" not in ifconfig, (
            f"loopback alias 127.42.0.21 still present after uninstall:\n{ifconfig}"
        )

        # NOTE: no reinstall here — run.sh does one restore between
        # phase 2a and 2b.
