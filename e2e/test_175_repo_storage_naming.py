"""175: label-based Mac mirror naming, and label-collision rejection
across two `volumes:` entries.

Under the mutagen-volumes model every mirrored entity (the primary
repo and every `volumes:`/`repos:` entry) is keyed by its resolved
mutagen sync LABEL, not by its `volumes:` map key or repo name --
mirrorMacDir (internal/serviceapi/volumes.go) is
`<RuntimeDir>/<projectID>/<label>/`. A `volumes:` entry's default
label is the leaf dir of its guest `path:` (resolveVolumeLabel);
`devm volume ls` itself is not yet updated for this model (still
prints the `volumes/<project>/<name>` shape -- see its
TODO(Task 17) comment), so this test inspects mirror paths directly
rather than through that command.

The label namespace is flat and shared with `repos:`
(schema.validateLabels) -- two `volumes:` entries whose paths share a
leaf name collide the same way test_227 pins for two `repos:` entries,
resolved the same way (an explicit `label:`).
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_storage_naming_primary_and_secondary(devm, workspace):
    primary_label = f"{workspace.path.name}-repo"  # BareCloneName of bare_repo_url()
    workspace.write_devmyaml(
        volumes={"mydata": "/mnt/mydata"},
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        r = subprocess.run(
            [devm.path, "shell", "--", "cat", f"/home/devm/{primary_label}/README.md"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "bare"

        # Primary: mirror keyed by its resolved label (BareCloneName),
        # not the Mac cwd basename or the `repos:` map key ("main").
        primary_mirror = mirror_path(workspace.vm_name, primary_label)
        assert (primary_mirror / "README.md").exists(), (
            f"primary Mac mirror missing README.md at {primary_mirror}"
        )

        # Named volume: mirror keyed by the leaf dir of its guest path
        # ("mydata"), not the `volumes:` map key -- here they happen to
        # match, but the naming rule is path-leaf-derived, not key-derived.
        secondary_mirror = mirror_path(workspace.vm_name, "mydata")
        assert secondary_mirror.is_dir(), (
            f"secondary volume mirror missing at {secondary_mirror}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )


@pytest.mark.timeout(60)
def test_volume_label_collision_rejected_then_resolved(devm, workspace):
    # Two `volumes:` entries whose paths share a leaf name ("data")
    # collide on the default label.
    colliding = {"a": "/mnt/data", "b": "/srv/data"}
    workspace.write_devmyaml(no_repo=True, volumes=colliding)

    r = subprocess.run(
        [devm.path, "reconcile"], cwd=str(workspace.path),
        capture_output=True, timeout=30,
    )
    assert r.returncode != 0, (
        "reconcile should reject two volumes: entries that derive the "
        f"same label; got exit 0. stdout={r.stdout.decode()!r}"
    )
    err = r.stderr.decode()
    assert "label" in err, f"error should mention 'label'; got:\n{err}"
    assert "volumes.a" in err and "volumes.b" in err, (
        f"error should name both colliding entries (volumes.a, volumes.b); got:\n{err}"
    )

    # Explicit label: on one entry resolves the collision.
    resolved = {"a": "/mnt/data", "b": {"path": "/srv/data", "label": "data-b"}}
    workspace.write_devmyaml(no_repo=True, volumes=resolved)

    r = subprocess.run(
        [devm.path, "reconcile"], cwd=str(workspace.path),
        capture_output=True, timeout=30,
    )
    assert r.returncode == 0, (
        "reconcile should accept the config once the label collision is "
        f"resolved: stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
    )
