"""181: `templates:` still work after the Mac-side `.devm/` scratch dir
was dropped (templates now render into daemon runtime storage).

Cold-starts a project with one service declaring a `templates:` entry
and verifies the rendered file appears at its declared `output:` path
in the guest, and that no `.devm/` directory reappears in the Mac cwd.
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_templates_render_without_mac_side_devm_dir(devm, workspace):
    tmpl_dir = workspace.path / "configs"
    tmpl_dir.mkdir()
    (tmpl_dir / "app.conf.tmpl").write_text("project={{.Project.Name}}\n")

    workspace.write_devmyaml(
        services={
            "tmplsvc": {
                "port": 8090,
                "templates": [
                    {"source": "configs/app.conf.tmpl",
                     "output": "/home/devm/app.conf"},
                ],
            },
        },
    )

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        r = subprocess.run(
            [devm.path, "shell", "--", "cat", "/home/devm/app.conf"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == f"project={workspace.vm_name}"

        assert not (workspace.path / ".devm").exists(), (
            ".devm must not reappear in the Mac cwd — templates render "
            "into daemon runtime storage now, not a project-local scratch dir"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
