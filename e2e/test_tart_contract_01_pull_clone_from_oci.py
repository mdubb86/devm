"""Pin: `tart pull` + `tart clone` of an OCI template work.

This is the foundation of Ship 4.1's base-image build. Regressions
in either CLI shape would break `image/build.sh`.
"""
import secrets
import subprocess

import pytest

from helpers import registry


@pytest.mark.devm
def test_tart_pull_then_clone_from_oci():
    template = "ghcr.io/cirruslabs/debian:latest"
    name = f"contract-clone-{secrets.token_hex(2)}"
    registry.append("sandbox", name)
    try:
        r = subprocess.run(["tart", "pull", template],
                           capture_output=True, timeout=300)
        assert r.returncode == 0, f"tart pull failed: {r.stderr.decode()}"

        r = subprocess.run(["tart", "clone", template, name],
                           capture_output=True, timeout=60)
        assert r.returncode == 0, f"tart clone failed: {r.stderr.decode()}"

        r = subprocess.run(["tart", "list", "--format=json"],
                           capture_output=True, timeout=10)
        assert r.returncode == 0
        assert name in r.stdout.decode(), f"{name} not in tart list"
    finally:
        subprocess.run(["tart", "delete", name],
                       capture_output=True, timeout=10)
        registry.remove("sandbox", name)
