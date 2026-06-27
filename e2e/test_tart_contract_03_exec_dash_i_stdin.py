"""Pin: `tart exec -i NAME bash -s` reads stdin; without `-i`, it doesn't.

The provisioner pipes provision-base.sh via `tart exec -i sudo bash -s
< script`. If `-i` ever stops forwarding stdin, the provisioner breaks
silently (bash starts with no script to read).
"""
import subprocess

import pytest


@pytest.mark.devm
def test_tart_exec_dash_i_forwards_stdin(inspector_vm):
    r = subprocess.run(
        ["tart", "exec", "-i", inspector_vm.name, "bash", "-s"],
        input=b"echo from-stdin\n",
        capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"exec failed: {r.stderr.decode()}"
    assert "from-stdin" in r.stdout.decode(), \
        f"stdin not forwarded; stdout={r.stdout.decode()!r}"


@pytest.mark.devm
def test_tart_exec_without_dash_i_drops_stdin(inspector_vm):
    # Without -i, stdin is closed: bash -s reads nothing and exits 0
    # with no output.
    r = subprocess.run(
        ["tart", "exec", inspector_vm.name, "bash", "-s"],
        input=b"echo from-stdin\n",
        capture_output=True, timeout=30,
    )
    assert r.returncode == 0
    assert "from-stdin" not in r.stdout.decode(), \
        "stdin WAS forwarded without -i (contract changed?)"
