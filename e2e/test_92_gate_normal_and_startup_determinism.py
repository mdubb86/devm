"""92: a normal gated cold-start end to end, `startup:` determinism
across a restart, and a crashing service blocking `devm.target`.

Exercises the composed provisioning script's boot-integrity gate
behavior on the ordinary (non-adopt, non-daemon-less) path -- contrast
with test_90 (daemon-less boot floor) and test_91 (adopt-in-place):

  1. `test_normal_cold_start_and_startup_determinism`:
     - A project with `startup:`, a service, and `network.allow:
       [api.github.com]` cold-starts: services active, real ssh access
       works, a declared service's curl to a NON-allow-listed host is
       BLOCKED (enforcement is up before services start), and
       `startup:`'s curl to a non-allow-listed host SUCCEEDS (it runs
       inside the composed script's open-egress window, before
       `enforce`).
     - Editing `startup:` to append a new command, then a single `devm
       stop` + `devm shell`, must run the NEW command on THAT boot --
       no second restart needed. This pins the redesigned
       `BucketRestartVM` contract (schema.md/lifecycle.md, Task 8):
       the applying restart runs a freshly-composed script, so the
       edit takes effect deterministically, not "eventually".

  2. `test_service_crash_blocks_target_activation`:
     - A service whose `exec` always exits non-zero makes the composed
       script's `services` stage abort (`set -eo pipefail`) BEFORE
       `systemctl start devm.target` runs. `devm shell` must exit
       non-zero (loud) and `devm.target` must NOT be active -- no
       shell access granted on a broken boot.

What it doesn't cover (tested elsewhere):
  - The daemon-less boot floor itself -> test_90.
  - Adopt-in-place -> test_91.
  - install:/packages: first-boot-only gating -> test_88_install_once,
    test_76.
"""
from __future__ import annotations

import os
import subprocess
import time
from pathlib import Path

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm

NON_ALLOWLISTED_HOST = "https://example.com"
STARTUP_FETCH_FILE = "/home/devm/.startup-fetch"
SVC_FETCH_FILE = "/home/devm/.svc-fetch"
DETERMINISM_SENTINEL = "/home/devm/.startup-determinism-sentinel"


def _runtime_dir() -> Path:
    """Same resolution as test_93_ssh_access.py: isolated e2e mode
    points DEVM_RUNTIME_DIR at a private dir; otherwise the real one."""
    if os.environ.get("E2E_ISOLATE") == "1":
        isolated_dir = os.environ.get("DEVM_RUNTIME_DIR")
        if isolated_dir:
            return Path(isolated_dir)
    return Path.home() / "Library/Application Support/devm"


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_normal_cold_start_and_startup_determinism(devm, workspace, sandbox_name):
    vm = TartSandbox(name=sandbox_name)
    workspace.write_devmyaml(
        startup=[
            f"curl -sf -m 10 {NON_ALLOWLISTED_HOST} -o {STARTUP_FETCH_FILE} || true",
        ],
        services={
            "probe": {
                "exec": [
                    "sh", "-c",
                    f"curl -sf -m 10 {NON_ALLOWLISTED_HOST} -o {SVC_FETCH_FILE} || true; "
                    "sleep infinity",
                ],
                "restart": "always",
            },
        },
        network={"allow": ["api.github.com"]},
    )

    # ---- Cold-start. ----
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    # ---- Services active. ----
    svc_state = vm.exec("systemctl", "is-active", "probe.service").stdout.strip()
    assert svc_state == "active", f"expected probe.service active, got {svc_state!r}"

    # ---- SSH reachable: a real connection through the emitted ssh_config. ----
    ssh_config = _runtime_dir() / "ssh_config"
    assert ssh_config.is_file(), f"expected {ssh_config} to exist after cold-start"
    ssh_r = subprocess.run(
        ["ssh", "-F", str(ssh_config), f"devm-{sandbox_name}", "whoami"],
        capture_output=True, text=True, timeout=30,
    )
    assert ssh_r.returncode == 0 and ssh_r.stdout.strip() == "devm", (
        f"ssh access to the cold-started VM failed: rc={ssh_r.returncode} "
        f"stdout={ssh_r.stdout!r} stderr={ssh_r.stderr!r}"
    )

    def file_size(path: str) -> int:
        r = vm.exec("sh", "-c", f"wc -c < {path} 2>/dev/null || echo 0")
        return int(r.stdout.strip() or "0")

    # ---- A service's curl to a non-allow-listed host is BLOCKED: the
    # ---- service only starts after `enforce`, so it never sees open
    # ---- egress. ----
    assert file_size(SVC_FETCH_FILE) == 0, (
        "the service's curl to a non-allow-listed host produced non-empty "
        f"output at {SVC_FETCH_FILE} -- it should have been blocked by "
        "egress enforcement, which is applied before services start"
    )

    # ---- startup:'s curl to a non-allow-listed host SUCCEEDS: it runs
    # ---- inside the composed script's open-egress window. ----
    assert file_size(STARTUP_FETCH_FILE) > 0, (
        "startup:'s curl to a non-allow-listed host produced no/empty "
        f"output at {STARTUP_FETCH_FILE} -- startup: should run under "
        "the open-egress window, before `enforce`"
    )

    # ---- Startup determinism: edit startup: to append a NEW sentinel
    # ---- command; a SINGLE devm stop + devm shell must run it on that
    # ---- boot -- no extra restart needed. ----
    assert vm.exec("test", "-f", DETERMINISM_SENTINEL).exit_code != 0, (
        "determinism sentinel should not exist before the edit"
    )
    workspace.patch_devmyaml(
        startup=[
            f"curl -sf -m 10 {NON_ALLOWLISTED_HOST} -o {STARTUP_FETCH_FILE} || true",
            f"echo ran > {DETERMINISM_SENTINEL}",
        ],
    )
    devm.stop(yes=True)
    stopped = vm.wait_state("stopped", timeout=30.0)
    assert stopped == "stopped", f"expected VM stopped, got {stopped!r}"

    r2 = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert r2.returncode == 0, f"restart failed:\n{r2.stderr.decode()}"

    sentinel = vm.exec("cat", DETERMINISM_SENTINEL)
    assert sentinel.ok and sentinel.stdout.strip() == "ran", (
        "edited startup: command should have run on the single restart "
        f"that applied it (deterministic, not eventual): ok={sentinel.ok} "
        f"stdout={sentinel.stdout!r} stderr={sentinel.stderr!r}"
    )


@pytest.mark.timeout(300)
def test_service_crash_blocks_target_activation(devm, workspace, sandbox_name):
    vm = TartSandbox(name=sandbox_name)
    workspace.write_devmyaml()
    workspace.add_systemd_service(
        name="broken",
        exec=["/bin/sh", "-c", "echo intentional fail >&2; exit 1"],
        restart="no",
    )

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=180,
    )
    assert r.returncode != 0, (
        f"devm shell should exit non-zero when a declared service crashes; "
        f"got rc=0\nstdout={r.stdout.decode()!r}"
    )
    stderr = r.stderr.decode()
    assert 'provision stage "services"' in stderr, (
        f"expected the services-stage failure to be classified in the error "
        f"chain; got stderr:\n{stderr}"
    )
    assert "service broken failed" in stderr, (
        f"expected the composed script's health-check failure message; got "
        f"stderr:\n{stderr}"
    )

    # Loud AND no access: the script aborts (set -eo pipefail) before
    # `systemctl start devm.target` runs, so the gate never opens even
    # though the VM itself is kept up for in-place debugging (a
    # post-install-class failure -- see provision.stagesAfterInstall).
    target_state = vm.exec("systemctl", "is-active", "devm.target").stdout.strip()
    assert target_state != "active", (
        f"devm.target must not activate when a declared service crashes "
        f"during provisioning; got {target_state!r}"
    )
