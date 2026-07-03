"""75: `packages:` installs the listed apt packages at cold-start.

devm's provisioner runs `apt-get update` then `apt-get install -y <pkg>...`
during the "apt-get install packages" step. The list comes from cfg.Packages
in the schema. iron-proxy must allow deb.debian.org and security.debian.org
for the install to reach upstream.

Package choice: `jq` — small, universally present in Debian's default
repos, and verifiable via `command -v jq` (no config-dependent behavior).

What this pins:
  - `packages: [jq]` yields `jq` on the guest's PATH after cold-start.
  - The provisioner surfaces "apt-get install packages" as a distinct
    step (not silently rolled into install: commands).

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
    assert "[step: apt-get install packages]" in r.stdout.decode(), (
        f"expected provisioner step marker in stdout; got:\n{r.stdout.decode()}"
    )
    # If apt-get itself surfaces an error the step exits non-zero — the
    # returncode check above catches that; still assert we didn't skip.
    assert "(no packages declared)" not in r.stdout.decode(), (
        "provisioner reported no packages even though jq was declared"
    )

    tart_sandbox = TartSandbox(name=sandbox_name)
    r = tart_sandbox.exec_shell("command -v jq && jq --version")
    assert r.ok, f"jq not installed on guest: exit={r.exit_code} stderr={r.stderr!r}"
    assert r.stdout.strip().startswith("/") and "jq-" in r.stdout, (
        f"unexpected jq check output: {r.stdout!r}"
    )
