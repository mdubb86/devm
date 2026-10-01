"""265: after devm daemon restart, the files.<project>.e2e.test reserved
route is re-registered by recoverProjectState — same shape as the
_devm.<project>.e2e.test recover test we already have (test_204's
mutagen-session sibling), verified here with a real Mac-side HTTPS
probe against the bundled filestash service (test_260 proved the
guest-internal side; this proves the Mac-side route survives restart).

Mutates the shared e2e daemon's launchd registration -> `install`
marker (single-process phase, `just e2e-install`).
"""
from __future__ import annotations
import subprocess
import time

import pytest

pytestmark = pytest.mark.install


def _wait_daemon_up(devm_path: str, timeout: float = 30.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        r = subprocess.run([devm_path, "status"], capture_output=True, timeout=10)
        if r.returncode == 0:
            return
        time.sleep(0.5)
    raise AssertionError(f"devm-e2e daemon never came back up within {timeout}s")


@pytest.mark.timeout(240)
def test_filestash_reserved_route_survives_daemon_restart(devm, devm_path, workspace):
    workspace.write_devmyaml(no_repo=True)
    try:
        r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                            capture_output=True, timeout=180)
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # Restart the daemon (SIGTERM the launchd job; launchd re-spawns).
        subprocess.run(["sudo", "launchctl", "kickstart", "-k",
                        "system/com.devm.e2e.service"], check=True, timeout=15)
        _wait_daemon_up(devm_path)

        # Mac-side probe: the reserved files route resolves and reaches
        # filestash. GET (not HEAD) because filestash returns 405 on
        # HEAD /; `-f` fails on 4xx/5xx, `-o /dev/null` discards body.
        # `devm status` returning OK doesn't guarantee the mac-side TLS
        # listener is bound yet, so retry a few times — the listener
        # comes up a beat after the daemon's socket accepts.
        hostname = f"files.{workspace.vm_name}.e2e.test"
        last_probe = None
        for _ in range(10):
            last_probe = subprocess.run(
                ["curl", "-sf", "-o", "/dev/null", "-m", "5", f"https://{hostname}/"],
                capture_output=True, timeout=10,
            )
            if last_probe.returncode == 0:
                break
            time.sleep(1)
        assert last_probe.returncode == 0, (
            f"post-restart mac-side https://{hostname}/ probe never succeeded "
            f"within 10s of retries (exit {last_probe.returncode}): "
            f"{last_probe.stdout!r} {last_probe.stderr!r}"
        )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                        capture_output=True, timeout=60)
