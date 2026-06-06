"""kit: volumes: {path: "size=N"} provisions an ext4 volume.

When the kit declares `volumes: {/data: "size=100M"}`, sbx must
create a dedicated ext4 filesystem mounted at /data inside the
sandbox. `findmnt -ln -t ext4 -o TARGET` should list it.

Devm dependency: devm.yaml's `services.X.masks` renders into this
volumes map. internal/scripts/init-volumes.sh walks `findmnt -ln -t
ext4` looking for these mounts and chowns them to the agent user.
If sbx changed the volume format, init-volumes wouldn't find them
and devm's masks feature would silently degrade to overlayfs.
"""
from __future__ import annotations

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_kit_volumes_creates_ext4_mount(sandbox_name):
    spec = minimal_kit(volumes={"/data": "size=100M"})
    with contract_sandbox(spec, sandbox_name):
        r = sbx_exec(sandbox_name, "findmnt", "-ln", "-t", "ext4", "-o", "TARGET")
        assert r.returncode == 0, f"findmnt failed: {r.stderr.decode()}"
        targets = r.stdout.decode().split()
        assert "/data" in targets, (
            f"/data should be a separate ext4 mount; findmnt targets: {targets}"
        )
