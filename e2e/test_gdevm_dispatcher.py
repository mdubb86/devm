"""Smoke test for the gdevm dispatcher: verifies the binary is
installed inside the guest and every subcommand's --help is wired.
No daemon interaction; single VM cold-start."""

import subprocess

import pytest

from helpers.workspace import Workspace
from helpers.devm import Devm

pytestmark = pytest.mark.devm


def test_gdevm_dispatcher(workspace: Workspace, devm: Devm) -> None:
    workspace.write_devmyaml()
    devm.approve()
    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=180,
    )
    assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

    # `gdevm --help` prints usage.
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "--help"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"gdevm --help failed: {r.stderr.decode()}"
    assert b"guest-side devm dispatcher" in r.stdout + r.stderr

    # Unknown subcommand exits 2.
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "bogus-subcommand"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 2, f"unknown subcommand expected exit 2, got {r.returncode}"

    # Each subcommand is dispatched. For subcommands that require
    # an arg (pop, propose, passthrough, run, upgrade doesn't), a
    # zero-arg invocation returns 2 (usage error) — proving the
    # dispatcher routes into the subcommand's own handler.
    for sub in ("pop", "propose", "run", "passthrough"):
        r = subprocess.run(
            [devm.path, "exec", "gdevm", sub],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 2, (
            f"gdevm {sub} with no args expected exit 2 (usage), got {r.returncode} "
            f"— dispatcher probably not routing"
        )

    # gdevm upgrade takes no args; without a daemon-side pipe path
    # ready in tests, we accept either 0 (refresh happened) or 1
    # (transport error from the endpoint being unreachable) — the
    # point is the dispatcher REACHED upgradeMain, not a specific
    # exit code.
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "upgrade"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    assert r.returncode in (0, 1), (
        f"gdevm upgrade dispatch expected exit 0 or 1, got {r.returncode}"
    )
