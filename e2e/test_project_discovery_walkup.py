"""CLI verbs resolve the project by walking up cwd for devm.yaml, so
they work from any subdirectory of the project root -- not just the
root itself. Pins the stateless walk-up discovery mechanism
(cmd/devm/project_discovery.go's discoverProject) from
docs/superpowers/specs/2026-09-23-devm-yaml-back-in-repo-design.md.
"""
from __future__ import annotations

import json
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_status_resolves_project_from_subdir(workspace, devm):
    workspace.write_devmyaml()

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=180,
    )
    assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

    subdir = workspace.path / "a" / "b" / "c"
    subdir.mkdir(parents=True)

    out = subprocess.run(
        [devm.path, "status", "--json"],
        cwd=str(subdir), capture_output=True, timeout=30,
    )
    assert out.returncode == 0, f"devm status --json failed:\n{out.stderr.decode()}"

    doc = json.loads(out.stdout.decode())
    project = doc.get("project")
    assert project is not None, (
        f"status run from a subdirectory found no project at all: {doc}"
    )
    # The walked-up devm.yaml resolved to THIS workspace's project, not
    # some other one -- and the daemon recognizes it as the running
    # sandbox that `devm start` just brought up above.
    assert project["sandbox"] == workspace.vm_name, (
        f"expected walk-up to resolve project {workspace.vm_name!r}, "
        f"got {project['sandbox']!r}"
    )
    assert project["state"] == "running", (
        f"expected the walked-up project's sandbox to be running, "
        f"got {project['state']!r}"
    )
