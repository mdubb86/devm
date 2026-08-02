"""140: a volume's contents survive `devm teardown` + cold-start.

Simplest end-to-end proof of the persistence contract. Cold-starts a
project with one declared volume, writes a sentinel file at the
declared guest path, tears down (which destroys the VM disk),
cold-starts again, and asserts the sentinel is still there.

If this fails: the daemon isn't rendering the --dir arg from
`volumes:` OR the guest mount script isn't binding it at the
declared target OR teardown is accidentally wiping the Mac-side
volume dir along with the VM.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_volume_persists_across_teardown(devm, workspace, sandbox_name):
    workspace.write_devmyaml(
        install=["true"],
        volumes={"scratch": "/var/lib/scratch"},
    )
    try:
        # First cold-start: write a sentinel into the volume mount.
        r = subprocess.run(
            [devm.path, "shell", "--", "sudo", "sh", "-c",
             "echo hello > /var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"first cold-start write failed:\n{r.stderr.decode()}"

        # Teardown: destroys VM disk. Volume dir on Mac must survive.
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )

        # Second cold-start: sentinel must reappear because the volume
        # was Mac-side, not on the destroyed VM disk.
        r = subprocess.run(
            [devm.path, "shell", "--", "cat", "/var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, (
            f"post-teardown read failed (rc={r.returncode}):\n"
            f"stderr: {r.stderr.decode()!r}\n"
            f"If the sentinel is gone, the volume dir was wiped along "
            f"with the VM disk — check that teardown leaves "
            f"~/Library/Application Support/devm/volumes/<project>/ alone."
        )
        assert r.stdout.decode().strip() == "hello", (
            f"sentinel content mismatch: got {r.stdout.decode()!r}, want 'hello\\n'"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
