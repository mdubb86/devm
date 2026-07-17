"""114: a templates-stage installer failure tears the VM down.

Templates install in the composed provisioning script's OPEN (unenforced)
egress window — before the enforce stage installs the real allowlist. A
VM kept alive on a templates failure would be sitting there unenforced,
so (unlike a services-stage failure, which legitimately keeps the VM for
debugging) a templates failure must be classified as teardown-worthy,
mirroring test_51's install-failure-teardown contract.

What this pins:
  - `devm shell` exits non-zero when a template's installer fails.
  - No VM is left behind after the failure (state == "absent").

What it doesn't cover (tested elsewhere):
  - Successful template cold-start / ordering / live reconcile -> test_19.
  - install:-step failure teardown -> test_51.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_templates_stage_failure_tears_down(devm, workspace):
    tmpl_dir = workspace.path / "configs"
    tmpl_dir.mkdir()
    (tmpl_dir / "broken.conf.tmpl").write_text("unreachable\n")

    # sudo:true template installers skip the `mkdir -p` a sudo:false
    # installer gets (see internal/render/templates.go's installerScript) —
    # `sudo install` into a directory that doesn't exist fails outright,
    # which is enough to fail the templates-stage dispatcher without
    # needing any other misconfiguration.
    workspace.write_devmyaml(
        services={
            "brokentemplatesvc": {
                "port": 8080,
                "templates": [
                    {"source": "configs/broken.conf.tmpl",
                     "output": "/no/such/dir/broken.conf",
                     "sudo": True},
                ],
            },
        },
    )

    p = subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path), timeout=120,
    )
    assert p.returncode != 0, (
        f"devm shell should exit non-zero when a template installer fails; "
        f"got rc={p.returncode}\nstderr={p.stderr.decode()}"
    )
    # No zombie VM should remain: a templates-stage failure runs in the
    # OPEN (unenforced) egress window, so keeping the VM alive would leave
    # an unenforced VM up.
    vm = TartSandbox(name=workspace.vm_name)
    current = vm.state()
    assert current == "absent", (
        f"failed templates-stage install must not leave a VM behind; "
        f"VM is still in state {current!r}"
    )
