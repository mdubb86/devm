"""184: guest .gitconfig mirrors the Mac-side git user identity
(user.name + user.email) — the v0.16.3 follow-on to v0.16.2's
credential-only provisioning (internal/render/gitcredentials.go).

Provisioning now reads MacCwd's effective git identity (git's own
precedence: repo-local .git/config > ~/.gitconfig > /etc/gitconfig, via
internal/provision.Provisioner.gitIdentity) and, when both user.name
and user.email resolve non-empty, appends a [user] block to
`/home/devm/.gitconfig`. Before this, a fresh guest VM had
credential.helper wired up for HTTPS auth but no identity — `git
commit` inside the guest would fail/prompt on a bare VM.

Sets a repo-local git identity in the workspace dir itself (not relying
on the e2e machine's own global config, which is out of this test's
control) so the assertion is deterministic regardless of what identity
happens to be configured on the machine running the suite.
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm

_TEST_NAME = "Devm E2E Bot"
_TEST_EMAIL = "devm-e2e-bot@example.com"


@pytest.mark.timeout(240)
def test_guest_git_identity_mirrored(devm, workspace):
    # Repo-local (not global) identity for the workspace dir devm.yaml
    # lives in -- this is exactly the directory `devm start` reads as
    # MacCwd, and a repo-local config here is deterministic regardless
    # of the e2e machine's own ~/.gitconfig.
    subprocess.run(["git", "init", "-q", str(workspace.path)], check=True)
    subprocess.run(
        ["git", "-C", str(workspace.path), "config", "user.name", _TEST_NAME], check=True,
    )
    subprocess.run(
        ["git", "-C", str(workspace.path), "config", "user.email", _TEST_EMAIL], check=True,
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

        name = subprocess.run(
            [devm.path, "exec", "--", "git", "config", "user.name"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        ).stdout.decode().strip()
        assert name == _TEST_NAME, f"guest user.name mismatch: got {name!r}, want {_TEST_NAME!r}"

        email = subprocess.run(
            [devm.path, "exec", "--", "git", "config", "user.email"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        ).stdout.decode().strip()
        assert email == _TEST_EMAIL, f"guest user.email mismatch: got {email!r}, want {_TEST_EMAIL!r}"

        # End-to-end proof: a commit inside the guest's hydrated primary
        # workspace succeeds with no TTY and no identity prompt -- the
        # actual gap this closes (pre-fix: git commit fails/prompts on a
        # fresh guest with no [user] block).
        commit = subprocess.run(
            [devm.path, "exec", "--", "git", "commit", "--allow-empty", "-m", "e2e identity check"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        combined = (commit.stdout + commit.stderr).decode(errors="replace")
        assert commit.returncode == 0, (
            f"guest git commit failed -- identity mirroring did not take effect:\n{combined}"
        )
        assert "Please tell me who you are" not in combined, (
            f"guest git still prompted for identity:\n{combined}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
