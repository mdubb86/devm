"""272: softnet contract-drift surfaces in `devm status` and daemon log.

Walks the real failure mode we built the drift feature for: a daemon
upgrade left a running VM's softnet subprocess on the previous build.
Steps:

  1. Build bin/devm-e2e-drift (one comment appended to contract.go →
     different softnet.ContractSHA).
  2. Install the drift variant over /usr/local/bin/devm-e2e; restart
     the LaunchDaemon so it loads the drift binary.
  3. `devm start` a scratch VM — softnet is spawned by the drift
     daemon and so reports the drift SHA.
  4. Reinstall the CURRENT bin/devm-e2e; restart the LaunchDaemon.
     The daemon now runs the current ContractSHA; the still-running
     softnet subprocess keeps its mmap of the drift binary (macOS
     unlink-on-install → new inode for the file, old inode lives on
     in the running process).
  5. Assert `devm status` prints the drift block (project name + the
     restart hint `devm stop && devm start`).
  6. Assert the daemon out log carries the one-line `softnet-drift:`
     notice emitted during daemon-startup VM rediscovery.

Marked `install` (auto-applied by conftest via the `service restart`
hint): this test mutates /usr/local/bin/devm-e2e twice and restarts the
LaunchDaemon between, same class of global-state churn as test_204 /
test_205.
"""
from __future__ import annotations
import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.install

REPO_ROOT = Path(__file__).resolve().parent.parent
DEVM_E2E_BIN = REPO_ROOT / "bin" / "devm-e2e"
DRIFT_VARIANT_BIN = REPO_ROOT / "bin" / "devm-e2e-drift"
INSTALLED_BIN = Path("/usr/local/bin/devm-e2e")
DAEMON_OUT_LOG = Path.home() / "Library" / "Logs" / "com.devm.e2e.service.out.log"
DAEMON_SOCK = Path.home() / "Library" / "Application Support" / "devm-e2e" / "devm.sock"


def _wait_daemon_socket(timeout: float = 30.0) -> None:
    """Daemon-restart handoff: the LaunchDaemon tears down the old
    process and brings up a new one. During that window the socket
    file briefly disappears. `devm status` returns rc=0 even then
    (test_205 documents the same quirk), so poll the socket directly.
    """
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if DAEMON_SOCK.exists():
            return
        time.sleep(0.1)
    raise AssertionError(f"devm-e2e socket never appeared at {DAEMON_SOCK} within {timeout}s")


def _tail_log(path: Path, start_offset: int) -> str:
    with path.open("rb") as f:
        f.seek(start_offset)
        return f.read().decode(errors="replace")


@pytest.mark.timeout(300)
def test_softnet_drift_surfaces_in_status(devm, workspace):
    assert DEVM_E2E_BIN.exists(), (
        f"bin/devm-e2e missing — run `just e2e-install test_272_softnet_contract_drift_surfaces` "
        f"(the just recipe builds it) rather than invoking pytest directly."
    )
    assert DAEMON_OUT_LOG.exists(), f"daemon out log missing at {DAEMON_OUT_LOG}"

    # 1. Build the drift variant.
    r = subprocess.run(
        ["just", "e2e-build-drift-variant"],
        cwd=str(REPO_ROOT), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, (
        f"e2e-build-drift-variant failed:\n"
        f"stdout={r.stdout.decode()!r}\nstderr={r.stderr.decode()!r}"
    )
    assert DRIFT_VARIANT_BIN.exists(), f"drift variant missing at {DRIFT_VARIANT_BIN}"

    try:
        # 2. Install drift variant + restart daemon → softnet will spawn
        #    with the drift ContractSHA on the next `devm start`.
        r = subprocess.run(
            ["sudo", "install", "-m", "755", str(DRIFT_VARIANT_BIN), str(INSTALLED_BIN)],
            capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"install drift variant failed:\n{r.stderr.decode()!r}"

        r = subprocess.run(
            [devm.path, "service", "restart"],
            capture_output=True, timeout=90,
        )
        assert r.returncode == 0, (
            f"service restart (drift) failed:\n{r.stderr.decode()!r}"
        )
        _wait_daemon_socket()

        # 3. Start the VM. Softnet carries the drift ContractSHA.
        workspace.write_devmyaml(no_repo=True)
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=240,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()!r}"

        # 4. Reinstall CURRENT bin/devm-e2e over the drift variant +
        #    restart daemon. Softnet keeps its drift-binary mmap; the
        #    daemon now has the current ContractSHA.
        r = subprocess.run(
            ["sudo", "install", "-m", "755", str(DEVM_E2E_BIN), str(INSTALLED_BIN)],
            capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"reinstall current failed:\n{r.stderr.decode()!r}"

        log_cursor = DAEMON_OUT_LOG.stat().st_size
        r = subprocess.run(
            [devm.path, "service", "restart"],
            capture_output=True, timeout=90,
        )
        assert r.returncode == 0, (
            f"service restart (current) failed:\n{r.stderr.decode()!r}"
        )
        _wait_daemon_socket()
        # Daemon runs discoverSoftnet per project on startup; give the
        # per-project goroutine a beat to probe + log drift.
        time.sleep(3)

        # 5. `devm status` prints the drift block.
        r = subprocess.run(
            [devm.path, "status"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        out = r.stdout.decode()
        assert r.returncode == 0, (
            f"devm status failed:\nstdout={out!r}\nstderr={r.stderr.decode()!r}"
        )
        assert f"softnet({workspace.vm_name})" in out, (
            f"devm status missing drift block for project {workspace.vm_name}:\n{out}"
        )
        assert "devm stop && devm start" in out, (
            f"devm status drift block missing restart hint:\n{out}"
        )

        # 6. Daemon startup drift notice landed in the out log.
        time.sleep(0.5)  # log buffer flush
        new_log = _tail_log(DAEMON_OUT_LOG, log_cursor)
        assert "softnet-drift" in new_log, (
            f"daemon out log missing softnet-drift notice after restart.\n"
            f"got (tail):\n{new_log[-2000:]}"
        )
        assert workspace.vm_name in new_log, (
            f"daemon out log drift line doesn't name the drifted project.\n"
            f"got (tail):\n{new_log[-2000:]}"
        )

    finally:
        # Teardown the VM, then restore the current devm-e2e so later
        # tests see a non-drifted environment. Best-effort — a teardown
        # failure here shouldn't mask the earlier assertion result.
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=90,
        )
        subprocess.run(
            ["sudo", "install", "-m", "755", str(DEVM_E2E_BIN), str(INSTALLED_BIN)],
            capture_output=True, timeout=30,
        )
        subprocess.run(
            [devm.path, "service", "restart"],
            capture_output=True, timeout=90,
        )
        try:
            DRIFT_VARIANT_BIN.unlink()
        except FileNotFoundError:
            pass
