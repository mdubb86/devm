"""180: hydration never re-clones a volume whose Mac-side storage is
already non-empty.

Pre-seeds the primary volume's storage dir with a canary file before
ever cold-starting devm. If hydration ran anyway, `git clone` into a
non-empty destination fails outright — so a green cold-start here is
itself partial proof, and the absence of any cloned content
(README.md) plus the canary's survival closes the loop.
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_never_reclone_non_empty_volume(devm, workspace):
    workspace.write_devmyaml()

    storage = workspace.volume_path()
    storage.mkdir(parents=True)
    (storage / "canary.txt").write_text("keep-me\n")

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Canary survived, untouched.
        assert (storage / "canary.txt").read_text() == "keep-me\n"

        # No clone occurred: the bare repo's README.md never appeared.
        assert not (storage / "README.md").exists(), (
            "hydration cloned into a non-empty volume — should have been skipped"
        )

        # Mount still succeeds and guest sees the canary at $WORKSPACE.
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c", 'cat "$WORKSPACE/canary.txt"'],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "keep-me"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
