"""Combined install/uninstall lifecycle — one install + one uninstall,
verifying the daemon, helper, DNS, and LaunchDaemon are all in the
states `devm install` promises, then unwinding cleanly.

File named `test_zz_…` so pytest collects it LAST among install-marker
tests: its uninstall in the finally block would trip
`_daemon_matches_devm_bin`'s "daemon program path missing" abort for
any install-marker test that runs after it (autouse fixture in
conftest.py).

Bundling install/uninstall into a single test cuts macOS Touch ID
prompts from 6-9 per suite run to 3 — pre-clean uninstall, install,
final uninstall — all clustered up front while the user is watching
the install phase start. A prompt the user misses = suite failure, so
the batching matters.

Coverage (adapted to the devm-e2e identity: TLD `e2e.test`, DNS `:51154`,
LaunchDaemon labels `com.devm.e2e.service` / `com.devm.e2e.helper`):

  DNS + resolver file:
    - `devm install` writes /etc/resolver/e2e.test with the expected
      content (nameserver 127.0.0.1 + port 51154).
    - DNS listener actually answers on the declared port, via both a
      direct dig and the system resolver — verified via the reserved
      probe name `devm-health-check.e2e.test` (dns.go:handleTest exempts
      it from the post-B3 per-project NXDOMAIN default, so we don't need
      a live project to prove the listener is up).
    - `devm uninstall` removes the resolver file.

  LaunchDaemon + user:
    - Plist installed at /Library/LaunchDaemons/, not the old
      LaunchAgent path.
    - Daemon reaches `launchctl state = running` within 10s.
    - Daemon runs as the invoking user (not root — the LaunchDaemon
      is a per-user daemon by design, gated by UserName in the plist).
    - `devm uninstall` removes the plist.

  Root helper block (Task 6 / per-project bind isolation):
    - Helper LaunchDaemon plist at /Library/LaunchDaemons/, helper
      binary at /usr/local/bin/devm-e2e-helper, UDS at the helper
      socket path, `_devm-e2e` group present.
    - Uninstall removes the plist + helper socket + first lo0 pool
      alias (127.42.0.21).

Coverage of the actual per-project HTTP/HTTPS proxy roundtrip lives in
test_110_direct_cold_start.py and test_111_direct_live.py — post-B3,
iron-proxy binds per-project on the project's ProjectIP (softnet+helper
BindTCP), not on a globally socket-activated 127.0.0.1:80/:443, so a
proxy roundtrip requires a live project with an allocated ProjectIP.
Keeping that here would duplicate 110/111 without adding install-marker
coverage.

Runs against the bootstrapped devm-e2e identity throughout
(internal/identity.E2E) — distinct plist labels, runtime dir, TLD, pool
range and CA CN from prod's, so this lifecycle test never collides with
a real `devm install` on the same Mac.
"""
from __future__ import annotations

import os
import platform
import shutil
import socket
import subprocess
import time
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


@pytest.mark.slow
@pytest.mark.timeout(900)  # base image build can take up to 10 min
def test_install_uninstall_lifecycle(devm, workspace, sudo_capable):
    if platform.system() != "Darwin":
        pytest.skip("install/uninstall lifecycle runs on macOS only")
    for tool in ("dig",):
        if not shutil.which(tool):
            pytest.skip(f"{tool} not on PATH; needed for the lifecycle")

    # Pre-clean: 1st Touch ID prompt (idempotent no-op if already
    # uninstalled — still shells to sudo).
    subprocess.run([devm.path, "uninstall"], capture_output=True, timeout=30)

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

        # --- DNS block ---
        assert os.path.exists(_RESOLVER_FILE), "resolver file not created"
        with open(_RESOLVER_FILE) as f:
            contents = f.read()
        assert contents == "nameserver 127.0.0.1\nport 51154\n", (
            f"unexpected resolver file contents: {contents!r}"
        )
        # `devm-health-check.<TLD>` is the reserved probe name (dns.go's
        # handleTest exempts it from the post-B3 per-project NXDOMAIN
        # default and always answers 127.0.0.1). Perfect for "listener
        # is up and correctly wired" without needing a live project.
        probe_name = "devm-health-check.e2e.test"
        r = subprocess.run(
            ["dig", "@127.0.0.1", "-p", "51154", probe_name, "+short"],
            capture_output=True, timeout=10,
        )
        assert "127.0.0.1" in r.stdout.decode(), (
            f"direct DNS query failed: {r.stdout.decode()!r}"
        )
        time.sleep(1)  # macOS resolver cache settle
        ip = socket.gethostbyname(probe_name)
        assert ip == "127.0.0.1", (
            f"system resolver returned {ip!r}, expected 127.0.0.1"
        )

        # --- LaunchDaemon block ---
        assert _LAUNCH_DAEMON_PLIST.exists(), (
            "LaunchDaemon plist not installed at /Library/LaunchDaemons/"
        )
        assert not _LAUNCH_AGENT_PLIST.exists(), (
            "old LaunchAgent plist should not be present"
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

    finally:
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
        # NOTE: the helper binary itself isn't removed by `devm uninstall`
        # — buildUninstallScript only bootouts the LaunchDaemons and
        # cleans up state; binary removal is `just e2e-teardown`'s job.
        # The socket IS removed by uninstall (bootout SIGTERMs the
        # helper, which unlinks its UDS on SIGTERM).
        assert not _HELPER_SOCKET.exists(), (
            f"helper socket still present after uninstall: {_HELPER_SOCKET}"
        )
        # Aliases removed. E2E's pool is 127.42.0.21-40 (identity.E2E.PoolStart/
        # PoolEnd) — distinct from prod's 127.42.0.1-20 — so this checks the
        # e2e install's own first alias, not prod's.
        ifconfig = subprocess.check_output(["/sbin/ifconfig", "lo0"], text=True)
        assert "127.42.0.21" not in ifconfig, (
            f"loopback alias 127.42.0.21 still present after uninstall:\n{ifconfig}"
        )
