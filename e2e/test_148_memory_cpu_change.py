"""148: memory:/cpu: overrides apply on cold-start and via reconcile-restart.

Pins the full memory/cpu spec flow end-to-end:

  1. Cold-start with `memory: 8G, cpu: 6` in devm.yaml — guest sees
     ~8GB RAM (7800-8100 MB `free -m`, allowing for kernel reserve)
     and exactly 6 CPUs (`nproc`).
  2. Edit devm.yaml to `memory: 4G, cpu: 4` + `devm reconcile --yes`
     — reconcile detects KindMemoryChange + KindCpuChange, routes
     to BucketRestartVM (stop → tart set --memory + --cpu → start),
     guest sees ~4GB RAM and 4 CPUs after the restart completes.

Uses `config_lock: false` in the devm.yaml so the mid-test edit
doesn't need `devm unlock`; config_lock's own coverage is
test_120_config_lock.py.

Sudo-gated (install-marker family): full VM cold-start + reconcile
restart both need the installed daemon and Touch ID.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


def _guest_memory_mb(devm_path: str, workspace_path: str) -> int:
    """Return MemTotal in MB as reported by `free -m` inside the guest."""
    r = subprocess.run(
        [devm_path, "exec", "bash", "-c",
         "free -m | awk '/^Mem:/ {print $2}'"],
        cwd=workspace_path, capture_output=True, timeout=60,
    )
    assert r.returncode == 0, (
        f"free -m failed:\n"
        f"stdout={r.stdout.decode()!r}\n"
        f"stderr={r.stderr.decode()!r}"
    )
    return int(r.stdout.decode().strip())


def _guest_cpu_count(devm_path: str, workspace_path: str) -> int:
    """Return CPU count as reported by `nproc` inside the guest."""
    r = subprocess.run(
        [devm_path, "exec", "nproc"],
        cwd=workspace_path, capture_output=True, timeout=60,
    )
    assert r.returncode == 0, (
        f"nproc failed:\n"
        f"stdout={r.stdout.decode()!r}\n"
        f"stderr={r.stderr.decode()!r}"
    )
    return int(r.stdout.decode().strip())


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_memory_cpu_change_round_trip(devm, workspace, sandbox_name, devm_installed):
    # config_lock: false so the mid-test devm.yaml edit doesn't fight
    # host-immutability. config_lock's own coverage is test_120.
    workspace.write_devmyaml(memory="8G", cpu=6, config_lock=False)

    try:
        # 1. Cold-start.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, (
            f"cold-start failed:\n"
            f"stdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )

        # 2. Guest reports ~8GB / 6 CPU.
        mb = _guest_memory_mb(devm.path, str(workspace.path))
        assert 7400 <= mb <= 8300, (
            f"expected ~8192 MB with `memory: 8G` (allowing kernel "
            f"reserve), got {mb} MB — tart set --memory didn't apply, "
            f"or the free -m parser broke"
        )
        cpus = _guest_cpu_count(devm.path, str(workspace.path))
        assert cpus == 6, (
            f"expected 6 CPUs with `cpu: 6`, got {cpus} — "
            f"tart set --cpu didn't apply"
        )

        # 3. Downsize: edit devm.yaml, reconcile (BucketRestartVM).
        workspace.patch_devmyaml(memory="4G", cpu=4)
        rec = devm.reconcile(yes=True, timeout=300)
        assert rec.returncode == 0, (
            f"reconcile --yes failed:\n"
            f"stdout={rec.stdout.decode()!r}\n"
            f"stderr={rec.stderr.decode()!r}"
        )

        # 4. Guest now reports ~4GB / 4 CPU.
        mb = _guest_memory_mb(devm.path, str(workspace.path))
        assert 3400 <= mb <= 4300, (
            f"expected ~4096 MB after reconcile to `memory: 4G`, "
            f"got {mb} MB — BucketRestartVM didn't re-apply "
            f"tart set --memory on the warm-restart path"
        )
        cpus = _guest_cpu_count(devm.path, str(workspace.path))
        assert cpus == 4, (
            f"expected 4 CPUs after reconcile to `cpu: 4`, got {cpus} "
            f"— BucketRestartVM didn't re-apply tart set --cpu"
        )
    finally:
        # The workspace fixture's autouse teardown handles cleanup,
        # but a belt-and-suspenders teardown here means a mid-test
        # failure doesn't leave a running VM if the fixture's
        # cleanup ever gets skipped.
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
