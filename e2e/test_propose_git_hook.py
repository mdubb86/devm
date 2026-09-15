"""Guest-side pre-commit hook: refuses `git commit devm.yaml` in a
project repo, steering the user toward `propose`; `--no-verify`
bypasses it.

Exercises internal/serviceapi/precommit_hook.go's InstallPreCommitHook,
installed into the cloned repo's shared git hooks dir during
SetupReposPhase at cold start.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_propose_git_hook_refuses_direct_commit(workspace, devm):
    workspace.write_devmyaml()  # default repos.main -> Hello-World
    label = workspace.bare_repo_label()
    guest_dir = f"/home/devm/{label}"

    try:
        cold = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert cold.returncode == 0, f"start failed: {cold.stderr.decode()!r}"

        # Guest-side edit to a devm.yaml inside the repo, staged for commit.
        stage = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c",
             f"cd {guest_dir} && echo '# tweak' >> devm.yaml && git add devm.yaml"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert stage.returncode == 0, f"staging devm.yaml failed: {stage.stderr.decode()!r}"

        # `git commit` is refused by the devm-managed pre-commit hook.
        refused = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c",
             f"cd {guest_dir} && git -c user.email=t@t -c user.name=t commit -m x"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert refused.returncode != 0, "direct commit of devm.yaml must be refused"
        stderr = refused.stderr.decode()
        assert "refusing to commit devm.yaml" in stderr, stderr
        assert "propose" in stderr, stderr

        # `--no-verify` bypasses the hook; the staged commit succeeds.
        bypassed = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c",
             f"cd {guest_dir} && git -c user.email=t@t -c user.name=t commit --no-verify -m x"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert bypassed.returncode == 0, (
            f"--no-verify commit should bypass the hook: {bypassed.stderr.decode()!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
