"""install failure: a file written to $WORKSPACE_DIR by a failing install:
step persists on the host after sbx tears the sandbox down.

Smoke-tests the viability of devm's "wrapper mirrors failure record to
the workspace mount" approach for surviving install failures. Per
contract_02, a failing install: makes sbx run exit non-zero and removes
the sandbox — the VM's tmpfs (/tmp/.devm-install/...) is gone with it.
But files written to $WORKSPACE_DIR ARE the workspace mount, which
lives on the host.

The probe: install step is
  sh -c 'echo HELLO > $WORKSPACE_DIR/probe.out; exit 1'
Sbx runs install, the step writes the file, then exits 1, sbx tears
down the sandbox. After sbx run exits, we read the file on the host
side at <workspace>/probe.out. If "HELLO" is there, the inversion is
viable.

Devm dependency: docs/superpowers/specs/2026-06-07-startup-supervision-
design.md (refinement after R1) plans to have wrap-fg.sh mirror failure
records to $WORKSPACE_DIR/.devm/failures/ before exiting with the user's
rc, so devm can read them post-teardown on the host. This contract
locks in the foundational property.
"""
from __future__ import annotations

import os
import shutil
import tempfile

import pytest

from helpers import sbx
from helpers.contract import minimal_kit, sbx_run_until_exit

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_install_failure_workspace_write_persists_on_host(sandbox_name):
    ws = tempfile.mkdtemp(prefix="probe-c32-ws-")
    try:
        # Install: write a marker to $WORKSPACE_DIR then fail.
        # sbx run exits non-zero per contract_02; the workspace mount
        # write should be flushed to host before teardown.
        spec = minimal_kit(
            install=[
                'sh -c \'echo HELLO > "$WORKSPACE_DIR/probe.out"; exit 1\'',
            ],
        )

        # We need to run with a fixed workspace dir so we can read it
        # post-teardown. Custom replacement for sbx_run_until_exit
        # that allows passing our own workspace path.
        import subprocess
        kit_dir = tempfile.mkdtemp(prefix="contract-kit-c32-")
        try:
            with open(os.path.join(kit_dir, "spec.yaml"), "w") as f:
                f.write(spec)
            proc = subprocess.run(
                ["sbx", "run", "--kit", kit_dir, "--name", sandbox_name, "probe", ws],
                capture_output=True, timeout=90,
            )
            rc = proc.returncode
            # We expect non-zero (contract_02).
            assert rc != 0, (
                f"sbx run should exit non-zero on failing install; got rc=0\n"
                f"stderr={proc.stderr.decode()!r}"
            )
            assert not sbx.sandbox_exists(sandbox_name), (
                f"failed install must not leave a sandbox behind"
            )

            # The viability pin: $WORKSPACE_DIR/probe.out must exist on the host.
            host_path = os.path.join(ws, "probe.out")
            assert os.path.exists(host_path), (
                f"VM-side write to $WORKSPACE_DIR did NOT persist on host. "
                f"The wrapper-writes-failure-record-to-mount approach is NOT "
                f"viable on this sbx version. Captured sbx output:\n"
                f"{proc.stdout.decode()}{proc.stderr.decode()}"
            )
            with open(host_path) as f:
                content = f.read()
            assert content.rstrip() == "HELLO", (
                f"host file content mismatch: got {content!r}"
            )
        finally:
            subprocess.run(["sbx", "rm", "-f", sandbox_name],
                           capture_output=True, timeout=15)
            shutil.rmtree(kit_dir, ignore_errors=True)
    finally:
        shutil.rmtree(ws, ignore_errors=True)
