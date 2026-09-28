"""End-to-end for gdevm passthrough / devm passthrough approve+deny.

Single VM, sequence:
  1. gdevm passthrough --reason "R1"           -> pending recorded
  2. devm passthrough deny                     -> pending cleared
  3. gdevm passthrough --reason "R2" --for 5m  -> pending recorded
  4. devm passthrough approve                  -> window opens; reason
                                                  echoed in output
  5. devm passthrough approve                  -> 400 no pending
  6. patch devm.yaml: guest.passthrough: false
  7. gdevm passthrough --reason "R3"           -> 403 (gate closed)
"""
import json
import os
import subprocess
from pathlib import Path

import pytest

from helpers.workspace import Workspace
from helpers.devm import Devm

pytestmark = pytest.mark.devm


def _pending_path(runtime_dir: Path, project: str) -> Path:
    return runtime_dir / project / "pending-passthrough.json"


def test_gdevm_passthrough_full_flow(workspace: Workspace, devm: Devm) -> None:
    workspace.write_devmyaml()
    devm.approve()
    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=180,
    )
    assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

    project = workspace.vm_name
    runtime_dir = Path(os.path.expanduser("~/Library/Application Support/devm-e2e"))
    pending = _pending_path(runtime_dir, project)

    # 1. Guest submits.
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "passthrough", "--reason", "R1"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"submit R1 failed: {r.stderr.decode()}"
    assert pending.exists()
    body = json.loads(pending.read_text())
    assert body["reason"] == "R1"

    # 2. Mac denies.
    r = subprocess.run(
        [devm.path, "passthrough", "deny"],
        cwd=str(workspace.path), capture_output=True, timeout=10,
    )
    assert r.returncode == 0, f"deny failed: {r.stderr.decode()}"
    assert not pending.exists(), "deny must clear pending file"

    # 3. Guest submits again with a duration.
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "passthrough", "--reason", "R2", "--for", "5m"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0

    # 4. Mac approves; window opens; reason echoed.
    r = subprocess.run(
        [devm.path, "passthrough", "approve"],
        cwd=str(workspace.path), capture_output=True, timeout=10,
    )
    assert r.returncode == 0, f"approve failed: {r.stderr.decode()}"
    combined = (r.stdout + r.stderr).decode()
    assert "PASSTHROUGH" in combined
    assert "R2" in combined, f"approve must echo guest reason:\n{combined}"
    assert not pending.exists(), "approve must clear pending file"

    # 5. Second approve with no pending -> 400.
    r = subprocess.run(
        [devm.path, "passthrough", "approve"],
        cwd=str(workspace.path), capture_output=True, timeout=10,
    )
    assert r.returncode != 0
    assert b"no pending" in (r.stdout + r.stderr).lower(), \
        f"expected no-pending error, got:\n{(r.stdout+r.stderr).decode()}"

    # 6. Patch devm.yaml to close the gate.
    workspace.write_devmyaml(guest={"passthrough": False})
    devm.approve()

    # 7. Guest submits under closed gate -> 403.
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "passthrough", "--reason", "R3"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 3, (
        f"gate-closed submit expected exit 3, got {r.returncode}\n"
        f"stderr: {r.stderr.decode()}"
    )
    assert b"guest.passthrough is disabled" in r.stderr
