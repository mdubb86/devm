"""80: template with default `sudo: false` renders to a guest-user-writable
path and lands devm-owned.

Companion to test_19 (which pins the `sudo: true` + /etc case). Default
sudo:false is the ergonomic path for the common case — a rendered config
under $HOME or $WORKSPACE that the guest user needs to read AND edit.

If the installer accidentally escalated even when sudo:false, the file
would land root-owned and confuse users (a `sed -i` from inside the VM
would fail). If it accidentally failed on a writable path (over-strict
filesystem check), templating would break entirely.

What this pins:
  - `templates: [{source, output, sudo: false}]` writes to
    /home/devm/foo — a guest-user-writable path — and succeeds.
  - The resulting file is owned by devm (uid 1000), not root.
  - Content is rendered from the source (not the source verbatim).

What it doesn't cover (tested elsewhere):
  - sudo:true + /etc — test_19_template_cold_start.
  - Cold-start ordering vs services — test_17.
  - Live re-render on source edit — test_20.
"""
from __future__ import annotations

import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_template_sudo_false_writes_devm_owned(workspace, devm, sandbox_name):
    tmpl_dir = workspace.path / "configs"
    tmpl_dir.mkdir()
    (tmpl_dir / "user.conf.tmpl").write_text(
        "PROJECT={{.Project.Name}}\n"
        "PORT={{.Service.probe.Port}}\n"
    )

    workspace.write_devmyaml(
        services={
            "probe": {
                "port": 9090,
                "templates": [
                    {
                        "source": "configs/user.conf.tmpl",
                        "output": "/home/devm/user.conf",
                        # sudo omitted — pin defaults to false.
                    },
                ],
            },
        },
    )

    # Owns cold-start: template install is a provisioner step.
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=120)

        # File exists at the declared path.
        sh.run_check("test -f /home/devm/user.conf", expect_zero=True, timeout=10)

        # File is devm-owned (not root). Emit a distinctive marker so
        # the assertion doesn't collide with the echoed command line.
        sh.send("printf 'DEVM_OWNER=%s\\n' \"$(stat -c '%U' /home/devm/user.conf)\"")
        sh.expect_text(r"DEVM_OWNER=devm", timeout=10)
        sh.expect_prompt(timeout=10)

        # Content was rendered (template variables replaced).
        sh.send("cat /home/devm/user.conf")
        sh.expect_text(rf"PROJECT={workspace.slug}", timeout=10)
        sh.expect_text(r"PORT=9090", timeout=10)
        sh.expect_prompt(timeout=10)

        # As devm, we can rewrite the file — no sudo needed.
        sh.run_check("echo overwritten > /home/devm/user.conf",
                     expect_zero=True, timeout=10)

        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
