"""139: iron-proxy survives SIGTERM to its shim (adopted path).

Adopted variant of test_138. After `devm service restart`, iron-proxy
is registered in supervisor.adopted (post-restart), not childPIDs.
supervisor.Stop's unified logic must still reach iron-proxy directly
via that map. The shim itself is unchanged across adoption — same
process, same ignore-SIGTERM contract. This test proves that the
adopted-path Stop still kills iron-proxy properly with the new shim
in place.

Also serves as coverage that the Stop UNIFICATION in supervisor.go
didn't regress the adopted case's kill-and-wait semantics that
apply-iron-proxy depends on (ports released before rebind).

Auto-marked `install` by conftest because source contains
`"service", "restart"`.
"""
from __future__ import annotations

import os
import signal
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _iron_proxy_pids_for(project_id: str) -> tuple[int | None, int | None]:
    r = subprocess.run(
        ["ps", "-axo", "pid=,command="],
        capture_output=True, text=True, check=True,
    )
    needle = f"/iron-proxy/{project_id}.yaml"
    shim_pid: int | None = None
    ip_pid: int | None = None
    for line in r.stdout.splitlines():
        if needle not in line:
            continue
        pid = int(line.strip().split(None, 1)[0])
        if "devm-setsid-shim" in line:
            shim_pid = pid
        else:
            ip_pid = pid
    return shim_pid, ip_pid


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_iron_proxy_survives_shim_sigterm_adopted(
    devm, workspace, sandbox_name, devm_installed,
):
    workspace.write_devmyaml(
        install=["true"],
        network={"allow": ["httpbin.org"]},
    )

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        _, ip_before = _iron_proxy_pids_for(workspace.slug)
        assert ip_before is not None, "iron-proxy should exist after cold-start"

        # Adopt via `devm service restart` — this SIGTERMs+respawns the
        # daemon; setsid keeps iron-proxy alive; new daemon adopts it
        # via DiscoverIronProxies. Iron-proxy PID must be unchanged.
        r = subprocess.run(
            [devm.path, "service", "restart"],
            capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"service restart failed:\n{r.stderr.decode()}"
        time.sleep(2)  # adoption settle window (same as test_44)

        shim_pid, ip_after = _iron_proxy_pids_for(workspace.slug)
        assert ip_after == ip_before, (
            f"iron-proxy PID changed across service restart: "
            f"{ip_before} → {ip_after}. Adoption failed; "
            f"can't exercise adopted-path Stop."
        )
        assert shim_pid, "shim should still be running post-adoption"

        # SIGTERM the shim — same probe as test_138.
        os.kill(shim_pid, signal.SIGTERM)

        deadline = time.monotonic() + 1.5
        while time.monotonic() < deadline:
            try:
                os.kill(shim_pid, 0)
                os.kill(ip_after, 0)
            except ProcessLookupError as e:
                pytest.fail(
                    f"process died on SIGTERM to shim (adopted path): {e}. "
                    f"shim_pid={shim_pid}, ip_pid={ip_after}"
                )
            time.sleep(0.1)

        # Adopted-path Stop: teardown must reach iron-proxy via
        # supervisor.adopted[k] and actually kill it.
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                os.kill(ip_after, 0)
            except ProcessLookupError:
                return
            time.sleep(0.2)
        pytest.fail(
            f"iron-proxy (pid={ip_after}) still alive 15s after `devm teardown` "
            f"following adoption. supervisor.Stop's adopted branch must signal "
            f"iron-proxy directly via s.adopted[k]."
        )
    finally:
        for pid in _iron_proxy_pids_for(workspace.slug):
            if pid:
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
