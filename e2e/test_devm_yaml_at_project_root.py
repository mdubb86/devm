"""After `devm start`, devm.yaml stays at the workspace root (not moved
to any daemon state-dir), and the guest's /home/devm/devm.yaml is kept
in sync with it. Pins the storage layout from
docs/superpowers/specs/2026-09-23-devm-yaml-back-in-repo-design.md.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_devm_yaml_stays_at_workspace_root(workspace, devm):
    workspace.write_devmyaml()

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=180,
    )
    assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

    # File is still at the workspace root -- never relocated to a
    # daemon-owned state directory.
    assert workspace.devm_yaml_path.exists(), (
        "devm.yaml missing from workspace root after devm start"
    )
    assert workspace.devm_yaml_path == workspace.path / "devm.yaml"

    # Guest sees /home/devm/devm.yaml, byte-identical to the Mac-side file.
    cat = subprocess.run(
        [devm.path, "exec", "cat", "/home/devm/devm.yaml"],
        cwd=str(workspace.path), capture_output=True, timeout=15,
    )
    assert cat.returncode == 0, f"guest cat failed:\n{cat.stderr.decode()}"
    assert cat.stdout.decode() == workspace.devm_yaml_path.read_text(), (
        "guest-side /home/devm/devm.yaml diverges from the Mac-side "
        "workspace-root copy"
    )
