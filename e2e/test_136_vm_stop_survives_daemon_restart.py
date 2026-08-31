"""136: `devm stop` after daemon restart cleanly reaps the VM (no tart orphan).

Companion to test_138 (ssh via softnet after install). Same install
cycle exercise, different assertion: after `devm install` (daemon reload
that per TODO section F does NOT re-adopt the RoleVM tart-run process),
does `devm stop` still leave the VM cleanly stopped in tart's view — or
does the orphan-tart-run pathology described in the TODO trigger?

TODO F says: daemon startup adopts iron-proxies and rehydrates softnet
but NOT the `tart run` (RoleVM) process. So `/vm/stop`'s
`sup.Stop(RoleVM)` returns ErrNotFound (treated as OK) and — if the
tart-run process doesn't exit naturally on guest poweroff — the VM
stays "running" in tart's view even after `devm stop` returns success.
Symptom: `devm stop` "succeeds" but `tart list` still shows the VM
running; manual recovery is `pkill -f 'tart run …<name>' && tart delete`.

If this test PASSES on current code, F is effectively resolved (likely
by `gracefulStopVM`'s vsock-agent probe making tart exit reliably on
guest poweroff). If it FAILS, we've reproduced F end-to-end and have a
regression net to fix against.

Sequence:
  1. Cold-start.
  2. Assert `tart list` shows the VM `running` (baseline).
  3. `devm install` — bootout daemon, reinstall, bootstrap. Same
     command `devm upgrade` runs after the binary swap.
  4. Short settle so daemon's adoption pass (or non-adoption of
     RoleVM, per TODO) completes.
  5. `devm stop -y`.
  6. Assert `tart list` shows the VM `stopped` — the crux. If it
     still shows `running`, F is real; recovery hint printed in the
     assertion message.
  7. Teardown.

Deliberately does NOT assert supervisor internals (is RoleVM adopted?
what PIDs?) — the user-facing contract is "stop leaves the VM
stopped", and that's what this test pins.

Note on Touch ID prompts: this test's `devm install` adds a prompt on
top of the harness-primed sudo credential. Same tradeoff as test_138.
"""
from __future__ import annotations

import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _tart_state_for(vm_name: str) -> str | None:
    """Return the tart `State` column value for a VM (`running` or
    `stopped`), or None if the VM isn't listed. Parses `tart list` in
    whitespace-columnar form — same shape used elsewhere in the e2e
    harness."""
    r = subprocess.run(
        ["tart", "list"],
        capture_output=True, text=True, check=True,
    )
    for line in r.stdout.splitlines():
        fields = line.split()
        if len(fields) >= 2 and fields[1] == vm_name:
            return fields[-1]
    return None


@pytest.mark.timeout(900)
@pytest.mark.slow
def test_vm_stop_survives_daemon_restart(devm, workspace, sandbox_name, devm_installed):
    workspace.write_devmyaml()

    # 1. Cold-start.
    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    # 2. Baseline: tart sees the VM as running.
    state = _tart_state_for(workspace.vm_name)
    assert state == "running", (
        f"baseline: expected tart state 'running' for {workspace.vm_name!r}, "
        f"got {state!r} — VM never actually cold-started per tart's view"
    )

    # 3. `devm install` — full lifecycle: bootout daemon, reinstall,
    # bootstrap. Same command `devm upgrade` runs after the binary
    # swap. Per TODO F, this reload does NOT re-adopt the RoleVM
    # process on the new daemon.
    r = subprocess.run(
        [devm.path, "install"],
        capture_output=True, timeout=780, check=False,
    )
    assert r.returncode == 0, (
        f"devm install failed:\n"
        f"stdout={r.stdout.decode()!r}\n"
        f"stderr={r.stderr.decode()!r}"
    )

    # 4. Settle: daemon adoption pass runs (though per TODO F it
    # doesn't touch RoleVM). Matches test_138's settle.
    time.sleep(2)

    # 5. `devm stop -y`. If TODO F is real, this returns success but
    # doesn't actually reap the tart-run process.
    r = subprocess.run(
        [devm.path, "stop", "-y"],
        cwd=str(workspace.path), capture_output=True, timeout=90,
    )
    assert r.returncode == 0, (
        f"devm stop failed after daemon restart:\n"
        f"stderr={r.stderr.decode()!r}"
    )

    # 6. The crux: tart's view of the VM. `devm stop` claims success —
    # tart MUST agree, otherwise we have an orphan tart-run process
    # holding the VM disk. Reproduces TODO F end-to-end if `stopped`
    # doesn't appear.
    state = _tart_state_for(workspace.vm_name)
    if state != "stopped":
        raise AssertionError(
            f"`devm stop` returned success but tart still shows VM in "
            f"state {state!r} after daemon restart — this reproduces "
            f"TODO F (`devm stop`/`teardown` can't stop a softnet VM "
            f"after a daemon restart, because daemon startup doesn't "
            f"re-adopt RoleVM). Manual recovery on affected systems: "
            f"pkill -f 'tart run .*{workspace.vm_name}' && tart delete "
            f"{workspace.vm_name}. See docs/superpowers/TODO.md §F for "
            f"the mechanism and fix sketch."
        )

    # No teardown call — VM is already stopped and cleanup fixtures
    # handle the workspace dir. Force-teardown just to be thorough
    # against fixture assumptions.
    subprocess.run(
        [devm.path, "teardown", "--yes"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
