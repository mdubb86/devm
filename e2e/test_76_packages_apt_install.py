"""75: `packages:` installs the listed apt packages at cold-start.

The composed provisioning script runs `apt-get update` then
`apt-get install -y <pkg>...` in its `packages` stage (first boot only,
inside the open-egress window). The list comes from cfg.Packages in the
schema. iron-proxy must allow deb.debian.org and security.debian.org for
the install to reach upstream.

Package choice: `jq` — small, universally present in Debian's default
repos, and verifiable via `command -v jq` (no config-dependent behavior).

What this pins:
  - `packages: [jq]` yields `jq` on the guest's PATH after cold-start.
  - The composed script surfaces `::devm:stage:packages::` as a distinct
    stage (not silently rolled into install: commands).

What it doesn't cover (tested elsewhere):
  - Failure of apt install with a blocked upstream — separately
    covered by iron-proxy egress tests.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_packages_installs_apt_binary(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        packages=["jq"],
        network={
            "allow": [
                "deb.debian.org",
                "security.debian.org",
            ],
        },
    )

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
    stderr = r.stderr.decode()
    # The composed script emits the packages stage marker on stderr (the
    # raw `::devm:stage:packages::` marker is forwarded to the diagnostic
    # writer alongside the reporter's spinner line). Its presence proves
    # the stage ran rather than being skipped for an empty package list.
    assert "::devm:stage:packages::" in stderr, (
        f"expected the composed script's packages stage marker in stderr; got:\n{stderr}"
    )

    tart_sandbox = TartSandbox(name=sandbox_name)
    r = tart_sandbox.exec_shell("command -v jq && jq --version")
    assert r.ok, f"jq not installed on guest: exit={r.exit_code} stderr={r.stderr!r}"
    assert r.stdout.strip().startswith("/") and "jq-" in r.stdout, (
        f"unexpected jq check output: {r.stdout!r}"
    )
