"""interop: Go exec.Command + sbx run/publish/verify polling works.

Stand-alone Go binary that mirrors devm's port-reconcile sequence
(spawn anchor -> wait -> publish -> verify -> snapshot -> user-shell
spawn) using plain exec.Command (no interface wrappers, no
ticker+select). Locks the baseline Go-primitive ↔ sbx combination
devm orchestrates with.

Counterpart: test_sbx_interop_02_publish_triggered.py — same shape
PLUS a Spawner-interface wrapper + ticker+select waitForRunning.
That pair historically reproduced Quirk #6's publish phantom; sbx
0.31 fixed it. Both probes stay green; if only baseline stays green
and triggered goes red, that's the sbx regression signal.

The probe binary itself lives at e2e/probes/probe-publish/main.go.
We build it inline in this test rather than checking the binary into
git.

If this baseline goes red, the Go-primitive layer (exec.Command +
nohup + sbx CLI surface) has broken against sbx — signals the bug
is in sbx, not in devm's higher-level orchestration.
"""
from __future__ import annotations
import os
import subprocess
import tempfile

import pexpect
import pytest

from helpers import sbx
from helpers.sbx_kit import materialize_kit

pytestmark = pytest.mark.sbx_interop


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PROBE_SRC = os.path.join(REPO_ROOT, "e2e", "probes", "probe-publish")


def _build_probe() -> str:
    """Build the probe binary into a temp file. Returns its path."""
    binpath = os.path.join(tempfile.gettempdir(), "devm-probe-publish")
    r = subprocess.run(
        ["go", "build", "-o", binpath, "./e2e/probes/probe-publish/"],
        cwd=REPO_ROOT, capture_output=True, timeout=60,
    )
    if r.returncode != 0:
        pytest.fail(
            f"go build of probe failed: stdout={r.stdout!r} stderr={r.stderr!r}"
        )
    return binpath


@pytest.mark.timeout(180)
@pytest.mark.parametrize("nohup", [True, False], ids=["nohup", "plain"])
def test_go_probe_publish_is_stable(nohup, sandbox_name):
    """The probe binary's publish + verify + observation flow returns
    rc=0 under both anchor shapes when driven via pexpect."""
    binpath = _build_probe()
    kit = materialize_kit()
    args = [binpath]
    if nohup:
        args.append("--nohup")
    args += [kit.kit_dir, kit.workspace, sandbox_name]

    child = pexpect.spawn(
        args[0], args[1:], encoding="utf-8", timeout=180, dimensions=(40, 200),
    )
    try:
        child.expect(pexpect.EOF, timeout=180)
        out = child.before or ""
        print("\n=== probe output ===")
        print(out)
        print("=== END ===\n", flush=True)
        child.close()
        assert child.exitstatus == 0, (
            f"probe exit={child.exitstatus} (nohup={nohup}). Output above."
        )
    finally:
        try:
            child.close(force=True)
        except Exception:
            pass
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()
