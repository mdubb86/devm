"""devm.yaml (and devm.me.yaml) never live inside the project's own
cwd anymore -- they live under the daemon's per-project state dir
(~/Library/Application Support/devm-e2e/<name>/). The `workspace`
fixture already runs `devm init` and writes the seed config during
setup, so by the time a test body runs, this is simply an assertion
about where things landed.
"""
from __future__ import annotations

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(20)
def test_devm_yaml_not_in_project_cwd(workspace):
    assert not (workspace.path / "devm.yaml").exists()
    assert not (workspace.path / "devm.me.yaml").exists()
    assert workspace.devmyaml_path.exists()
    assert workspace.devmyaml_path.parent != workspace.path
