"""mounts: :ro suffix on a positional makes the mount read-only.

`sbx run --kit X --name Y AGENT WS EXTRA:ro` mirrors EXTRA at EXTRA
inside but as read-only. Writing inside fails with permission error.
Without :ro the same mount is writable.

Devm dependency: devm.yaml's `mounts: [PATH:ro]` syntax renders the
:ro suffix into the positional arg. internal/schema/mount.go parses
and forwards it. If sbx ever changed :ro semantics, devm.yaml's
:ro promise would be silently broken.
"""
from __future__ import annotations

import os
import shutil
import tempfile

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_ro_suffix_makes_mount_read_only(sandbox_name):
    primary = tempfile.mkdtemp(prefix="contract-M2-primary-")
    ro_mount = tempfile.mkdtemp(prefix="contract-M2-ro-")
    try:
        with open(os.path.join(ro_mount, "SOURCE"), "w") as f:
            f.write("ro-source\n")

        with contract_sandbox(
            minimal_kit(), sandbox_name,
            workspace=primary,
            extra_positionals=[f"{ro_mount}:ro"],
        ):
            r_read = sbx_exec(sandbox_name, "cat", os.path.join(ro_mount, "SOURCE"))
            assert r_read.returncode == 0, (
                f"read from :ro mount should work: {r_read.stderr.decode()}"
            )
            assert r_read.stdout.decode().strip() == "ro-source"

            r_write = sbx_exec(
                sandbox_name, "sh", "-c",
                f"echo trying > {ro_mount}/SHOULD_NOT_EXIST 2>&1; echo rc=$?",
            )
            assert r_write.returncode == 0, "the sh -c itself should run"
            out = r_write.stdout.decode()
            assert "rc=0" not in out, (
                f"write to :ro mount should fail with non-zero exit code; got: {out!r}"
            )
    finally:
        shutil.rmtree(primary, ignore_errors=True)
        shutil.rmtree(ro_mount, ignore_errors=True)
