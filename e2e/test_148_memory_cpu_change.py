"""148: memory:/cpu: overrides + base-image swap apply on cold-start and
via reconcile-restart.

Pins two features end-to-end in one round-trip:

  1. `memory:` and `cpu:` overrides applied by /vm/start via
     `tart set --memory` / `tart set --cpu` (Kind{Memory,Cpu}Change
     → BucketRestartVM).
  2. Base-image swap (image/provision-base.sh's devm-swap.service):
     `/swapfile` sized at 50% of MemTotal on every boot, with
     vm.swappiness=10.

Flow:

  1. Cold-start with `memory: 8G, cpu: 6` — guest sees ~8GB RAM,
     exactly 6 CPUs, swap ≈ mem/2, swappiness == 10.
  2. Edit devm.yaml to `memory: 4G, cpu: 4` + `devm reconcile --yes`
     — BucketRestartVM stops → re-applies tart flags → starts.
     Guest sees ~4GB RAM, 4 CPUs, swap resized to new ~mem/2
     (devm-swap.service re-runs on the restart and recomputes).

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


def _guest_swap_mb(devm_path: str, workspace_path: str) -> int:
    """Return SwapTotal in MB as reported by `free -m` inside the guest."""
    r = subprocess.run(
        [devm_path, "exec", "bash", "-c",
         "free -m | awk '/^Swap:/ {print $2}'"],
        cwd=workspace_path, capture_output=True, timeout=60,
    )
    assert r.returncode == 0, (
        f"free -m (swap) failed:\n"
        f"stdout={r.stdout.decode()!r}\n"
        f"stderr={r.stderr.decode()!r}"
    )
    return int(r.stdout.decode().strip())


def _guest_swappiness(devm_path: str, workspace_path: str) -> int:
    """Return vm.swappiness as reported by sysctl inside the guest."""
    r = subprocess.run(
        [devm_path, "exec", "bash", "-c",
         "cat /proc/sys/vm/swappiness"],
        cwd=workspace_path, capture_output=True, timeout=60,
    )
    assert r.returncode == 0, (
        f"read swappiness failed:\n"
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
            [devm.path, "start"],
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

        # 2b. Swap ≈ mem/2 and swappiness == 10.
        # devm-swap.service ran on boot and sized /swapfile to half
        # of MemTotal. free -m reports MemTotal and SwapTotal both in
        # MiB, so the ratio should be exact within a few MB of
        # rounding. ±20 MB tolerance is comfortable.
        swap = _guest_swap_mb(devm.path, str(workspace.path))
        assert abs(swap - mb // 2) <= 20, (
            f"expected swap ≈ MemTotal/2 ({mb // 2} MB) with `memory: 8G`, "
            f"got swap={swap} MB (MemTotal={mb} MB) — devm-swap.service "
            f"didn't create /swapfile at the expected size, or its "
            f"mem_kb/2048 formula drifted"
        )
        swappiness = _guest_swappiness(devm.path, str(workspace.path))
        assert swappiness == 10, (
            f"expected vm.swappiness=10, got {swappiness} — "
            f"/etc/sysctl.d/60-devm-swap.conf didn't apply"
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

        # 4b. Swap resized to ~mem/2 of the NEW memory. This is the
        # key invariant devm-swap.service's "recompute every boot"
        # logic exists to hold — a stale 4G swapfile from the 8G past
        # would fail this check.
        swap = _guest_swap_mb(devm.path, str(workspace.path))
        assert abs(swap - mb // 2) <= 20, (
            f"expected swap ≈ MemTotal/2 ({mb // 2} MB) after reconcile "
            f"to `memory: 4G`, got swap={swap} MB (MemTotal={mb} MB) — "
            f"devm-swap.service didn't resize /swapfile on the restart"
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
