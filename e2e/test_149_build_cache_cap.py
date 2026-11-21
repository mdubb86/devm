"""149: docker.buildCache cap applies on cold-start and via reconcile.

Pins:
  1. Cold-start writes /etc/buildkit/buildkitd.toml with maxUsedSpace
     computed from docker.buildCache (absolute or percent of disk).
  2. A reconcile to a different value rewrites the toml and restarts
     buildkit.service; the new cap is live.
  3. The cap is roughly honored: `du -sh /var/lib/buildkit` stays
     under (cap * 1.25) after repeated docker builds that would
     otherwise accumulate past the cap.
"""
from __future__ import annotations

import re
import subprocess

import pytest

pytestmark = pytest.mark.devm


def _guest_cat(devm_path: str, workspace_path: str, path: str) -> str:
    r = subprocess.run(
        [devm_path, "exec", "cat", path],
        cwd=workspace_path, capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"cat {path} failed: {r.stderr.decode()!r}"
    return r.stdout.decode()


def _buildkit_cache_bytes(devm_path: str, workspace_path: str) -> int:
    r = subprocess.run(
        [devm_path, "exec", "bash", "-c",
         "du -sb /var/lib/buildkit 2>/dev/null | cut -f1"],
        cwd=workspace_path, capture_output=True, timeout=60,
    )
    assert r.returncode == 0, f"du failed: {r.stderr.decode()!r}"
    return int(r.stdout.decode().strip() or "0")


def _maxUsedSpace_from_toml(toml_text: str) -> int:
    # Parse the `maxUsedSpace = <N>` line (bare integer; the oneshot in
    # internal/docker/install.go emits it unquoted).
    m = re.search(r"maxUsedSpace\s*=\s*(\d+)", toml_text)
    assert m, f"maxUsedSpace key missing from buildkitd.toml:\n{toml_text}"
    return int(m.group(1))


@pytest.mark.slow
@pytest.mark.timeout(1800)
def test_build_cache_cap_cold_start_and_reconcile(
    devm, workspace, sandbox_name, devm_installed,
):
    # Cold-start with disk: 32G (default) + docker.buildCache: "2G".
    # Allowlist: github.com for the buildkit release tarball (from
    # github.com/moby/buildkit) and the default fixture repo; debian
    # mirrors for the Dockerfile's apt steps during docker install;
    # download.docker.com is auto-added by the docker-enabled implicit
    # allowlist (internal/docker/allowlist.go) but listed here for
    # clarity.
    workspace.write_devmyaml(
        docker={"enabled": True, "buildCache": "2G"},
        network={
            "allow": [
                "github.com",
                "deb.debian.org",
                "security.debian.org",
                "download.docker.com",
            ],
        },
    )
    try:
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=600,
        )
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # 1. Toml has maxUsedSpace = 2 GiB (the setup script computes
        # bytes verbatim from "2G" = 2 * 1024^3).
        toml = _guest_cat(
            devm.path, str(workspace.path), "/etc/buildkit/buildkitd.toml",
        )
        expected = 2 * 1024 * 1024 * 1024
        assert _maxUsedSpace_from_toml(toml) == expected, (
            f"cold-start maxUsedSpace mismatch:\n{toml}"
        )
        # 2. minFreeSpace = 0 (explicit disable of buildkit's
        # wrong-disk free-space trigger).
        assert "minFreeSpace = 0" in toml, (
            f"minFreeSpace=0 missing:\n{toml}"
        )

        # 3. Reconcile to docker.buildCache: "20%" (of 32 GB disk
        # ≈ 6.4 GiB after the oneshot resolves against blockdev's
        # byte count of the root partition).
        workspace.patch_devmyaml(
            docker={"enabled": True, "buildCache": "20%"},
        )
        devm.approve()
        rec = devm.reconcile(yes=True, timeout=300)
        assert rec.returncode == 0, f"reconcile failed: {rec.stderr.decode()!r}"
        toml = _guest_cat(
            devm.path, str(workspace.path), "/etc/buildkit/buildkitd.toml",
        )
        got = _maxUsedSpace_from_toml(toml)
        # 20% of 32 GiB = 6.4 GiB; disk probing has ~1 GiB slack for
        # partition layout, so accept a 10% tolerance.
        want = int(0.20 * 32 * 1024 * 1024 * 1024)
        assert abs(got - want) <= want // 10, (
            f"reconcile maxUsedSpace: want ~{want}, got {got}"
        )

        # 4. buildkit service is active after the restart.
        r = subprocess.run(
            [devm.path, "exec", "systemctl", "is-active", "buildkit.service"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0 and r.stdout.decode().strip() == "active"

        # 5. The cap actually bites. Reconcile back to "2G" and run two
        # docker builds with distinct, incompressible layers; cache
        # size stays under (cap * 1.25) (GC slack). The point is "GC
        # runs before the cap is blown"; not "cache is exactly $cap."
        # Each build generates ~1.2-1.3 GiB of /dev/urandom bytes —
        # non-dedupable, non-compressible — so two builds would
        # accumulate ~2.5 GiB past the 2 GiB cap if GC were absent.
        workspace.patch_devmyaml(
            docker={"enabled": True, "buildCache": "2G"},
        )
        devm.approve()
        rec = devm.reconcile(yes=True, timeout=300)
        assert rec.returncode == 0

        build_cmd = (
            "set -e; "
            "mkdir -p /tmp/bld && cd /tmp/bld && "
            "printf 'FROM debian:bookworm\\nRUN dd if=/dev/urandom of=/blob bs=1M count=1200\\n' > Dockerfile && "
            "docker buildx build --builder devm --load -t devm-cap-a . && "
            "sed -i 's/count=1200/count=1300/' Dockerfile && "
            "docker buildx build --builder devm --load -t devm-cap-b ."
        )
        r = subprocess.run(
            [devm.path, "exec", "bash", "-c", build_cmd],
            cwd=str(workspace.path), capture_output=True, timeout=1200,
        )
        assert r.returncode == 0, (
            f"build sequence failed: {r.stderr.decode()!r}"
        )

        bytes_used = _buildkit_cache_bytes(devm.path, str(workspace.path))
        cap = 2 * 1024 * 1024 * 1024
        assert bytes_used < cap * 5 // 4, (
            f"buildkit cache exceeded cap * 1.25: "
            f"used={bytes_used}, cap={cap}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
