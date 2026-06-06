"""mounts: positional workspace + extra positional paths mirror at same path inside.

`sbx run --kit X --name Y AGENT WS EXTRA1 EXTRA2` mirrors WS at WS
(inside the sandbox), EXTRA1 at EXTRA1, EXTRA2 at EXTRA2 — same
absolute paths as on the host.

We plant marker files in both the primary workspace and an extra
mount, then verify they're readable inside at the exact same paths.

Devm dependency: devm.yaml's `mounts:` field renders into additional
positional paths after the primary workspace. internal/orchestrator/
shell.go appends them when sb.Exists() == false.
"""
from __future__ import annotations

import os
import shutil
import tempfile

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_extra_positional_mount_mirrored_at_same_path(sandbox_name):
    primary = tempfile.mkdtemp(prefix="contract-M1-primary-")
    extra = tempfile.mkdtemp(prefix="contract-M1-extra-")
    try:
        with open(os.path.join(primary, "PRIMARY_MARK"), "w") as f:
            f.write("primary-ok\n")
        with open(os.path.join(extra, "EXTRA_MARK"), "w") as f:
            f.write("extra-ok\n")

        with contract_sandbox(
            minimal_kit(), sandbox_name,
            workspace=primary,
            extra_positionals=[extra],
        ):
            r1 = sbx_exec(sandbox_name, "cat", os.path.join(primary, "PRIMARY_MARK"))
            assert r1.returncode == 0, (
                f"primary mount not visible at {primary}: {r1.stderr.decode()}"
            )
            assert r1.stdout.decode().strip() == "primary-ok"

            r2 = sbx_exec(sandbox_name, "cat", os.path.join(extra, "EXTRA_MARK"))
            assert r2.returncode == 0, (
                f"extra mount not visible at {extra}: {r2.stderr.decode()}"
            )
            assert r2.stdout.decode().strip() == "extra-ok"
    finally:
        shutil.rmtree(primary, ignore_errors=True)
        shutil.rmtree(extra, ignore_errors=True)
