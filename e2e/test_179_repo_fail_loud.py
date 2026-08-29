"""179: repo hydration fails loud — cold-start aborts, no VM survives.

Four independent failure kinds, each its own fresh workspace (via
pytest parametrization over function-scoped fixtures):

  - bad_secret: `repo.secret` names a secret never registered with
    `devm secret set`. The same name is also bound via `env:` +
    `network.allow[].secrets` (a realistic pattern — one secret used
    both to clone AND to authenticate guest-side tooling), which is
    what actually trips the existing "missing secrets" preflight
    before any VM is even created.
  - missing_origin: no explicit `repo.url:` and the Mac cwd isn't a
    git repo at all, so URL derivation has nothing to read.
  - wrong_url: `repo.url:` points at a `file://` path that doesn't
    exist.
  - network_fail: `repo.url:` is an https:// URL to a host that
    cannot resolve.

Each must exit non-zero with a clear, failure-specific error, and must
not leave the sandbox running.
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = [
    pytest.mark.devm,
    pytest.mark.skip(reason="Task 28: pending mutagen-volumes fixture migration"),
]


@pytest.mark.timeout(300)
@pytest.mark.parametrize(
    "case", ["bad_secret", "missing_origin", "wrong_url", "network_fail"]
)
def test_repo_hydration_fails_loud(devm, workspace, sandbox_name, case):
    if case == "bad_secret":
        # yaml.safe_dump can't emit the `!secret` tag, so this one is
        # written directly (same technique as test_74).
        workspace.devmyaml_path.write_text(f"""\
project:
  name: {workspace.vm_name}
repo:
  url: {workspace.bare_repo_url()}
  secret: unregistered_repo_secret
env:
  DUMMY: !secret unregistered_repo_secret
network:
  allow:
  - host: 127.0.0.1
    secrets:
    - unregistered_repo_secret
""")
        expect_substr = "missing secrets"
    elif case == "missing_origin":
        # No `git init` in workspace.path, and no explicit repo.url:.
        workspace.write_devmyaml(repo={"secret": "e2e_default"})
        expect_substr = "not a git repository"
    elif case == "wrong_url":
        workspace.write_devmyaml(
            repo={"url": "file:///nonexistent-path-xyz.git", "secret": "e2e_default"},
        )
        expect_substr = "git clone"
    elif case == "network_fail":
        workspace.write_devmyaml(
            repo={
                "url": "https://this-host-will-not-resolve.invalid/foo.git",
                "secret": "e2e_default",
            },
        )
        expect_substr = "git clone"
    else:
        raise AssertionError(case)

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=180,
        )
        assert r.returncode != 0, (
            f"[{case}] cold-start should have aborted; stdout="
            f"{r.stdout.decode()!r}"
        )
        stderr = r.stderr.decode()
        assert expect_substr in stderr, (
            f"[{case}] expected {expect_substr!r} in stderr, got: {stderr!r}"
        )

        vm = TartSandbox(name=sandbox_name)
        final_state = vm.wait_state("absent", timeout=30)
        assert final_state == "absent", (
            f"[{case}] sandbox must not survive a failed cold-start; "
            f"state={final_state!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
