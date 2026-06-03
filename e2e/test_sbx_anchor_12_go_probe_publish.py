"""sbx-anchor 12: a stand-alone Go binary that mirrors devm's port-
reconcile sequence (spawn anchor → wait → publish → verify → snapshot
→ user-shell spawn) succeeds reliably under both `nohup` and plain
`sbx run` anchor wrappings, when invoked via pexpect (production
PTY shape).

This pins the negative finding from the 2026-06-02 test_07 debugging:
**Go itself, exec.Cmd, sbx's CLI surface, and the publish/verify
sequence are NOT the cause of devm's test_07 port phantom.** Pure-sbx
tests confirm sbx works. This test confirms a Go binary doing the
same flow works. devm's larger orchestrator does something extra
that destabilizes the port endpoint — see docs/sbx-quirks.md "Quirk
#6" (added 2026-06-02) for the open investigation.

If devm's test_07 starts passing AND this test starts failing, the
upstream behavior may have shifted in a way that affects both — re-
read the trace and update Quirk #6.

The probe binary itself lives at e2e/probes/probe-publish/main.go.
We build it inline in this test rather than checking the binary into
git.
"""
from __future__ import annotations
import os
import subprocess
import tempfile

import pexpect
import pytest

from helpers import sbx
from helpers.sbx_kit import materialize_kit


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
