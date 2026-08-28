"""203: the generated mutagen session config yaml has the right shape.

SetupPhase (internal/serviceapi/mutagen_sessions.go) writes one config
yaml per entity via mutagen.WriteConfigFile at
`<RuntimeDir>/mutagen/sessions/<projectID>/<label>.yml`
(internal/mutagen/config_file.go's ConfigFilePath). Its content is
hand-rendered (renderYAML): a fixed `mode: two-way-resolved` /
`scanMode: accelerated` header, then `ignores:` with
internal/mutagen.DefaultIgnores first, the entry's own `ignore:` list
appended after.

Pins:
  - The file exists after `devm start` at the expected path.
  - It contains `mode: two-way-resolved` and `scanMode: accelerated`.
  - internal/mutagen.DefaultIgnores entries (e.g. "**/node_modules/")
    are present.
  - The user's own `ignore:` entries are present too, after the
    defaults.
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import session_config_path

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_session_config_file(devm, workspace):
    label = workspace.bare_repo_label()
    workspace.write_devmyaml(
        repos={
            "main": {
                "url": workspace.bare_repo_url(),
                "primary": True,
                "ignore": ["scratch/"],
            },
        },
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

        cfg_path = session_config_path(workspace.vm_name, label)
        assert cfg_path.exists(), f"session config yaml missing at {cfg_path}"

        content = cfg_path.read_text()
        assert "mode: two-way-resolved" in content, content
        assert "scanMode: accelerated" in content, content

        # A sample of internal/mutagen.DefaultIgnores.
        for default_ignore in ("**/node_modules/", "**/.next/cache/", ".git/objects/pack/"):
            assert default_ignore in content, (
                f"expected default ignore {default_ignore!r} in config:\n{content}"
            )

        # The user's own ignore entry, composed after the defaults.
        assert "scratch/" in content, f"expected user ignore 'scratch/' in config:\n{content}"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
