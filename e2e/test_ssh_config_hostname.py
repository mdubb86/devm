"""Verify the emitted ssh_config Host block uses `HostName <proj>.test`
and `Port 22` (not a raw loopback IP / picked host port) — the B3
retirement of `SSHHostPort` in favor of a fixed `:22` on the project's
allocated ProjectIP, DNS-resolved (internal/serviceapi/sshconfig/
sshconfig.go's `blockTmpl`; see docs/superpowers/specs/
2026-07-19-per-project-bind-isolation-design.md's SSH section). Also
verifies `ssh` actually connects using that config.

Adapted from the task-7 brief's sketch:
  - The brief's draft read the ssh_config from a hardcoded
    `~/Library/Application Support/devm/ssh_config`, which breaks
    under the isolated e2e lane (E2E_ISOLATE=1, DEVM_RUNTIME_DIR
    elsewhere) — this reuses test_93_ssh_access.py's already-proven
    `_get_runtime_dir()` helper for that.
  - The brief's connectivity check ran bare `ssh devm-<proj>`, which
    only works if the user's own `~/.ssh/config` already `Include`s
    devm's ssh_config (a one-time manual step, not guaranteed in a
    test environment). Uses `ssh -F <ssh_config> devm-<proj>` instead,
    matching test_93_ssh_access.py's proven pattern — points ssh at the
    file directly rather than assuming it's been Include'd.
  - Uses the real `devm`/`workspace` fixtures (path/cwd-bound
    `Devm`/`Workspace` instances) rather than the brief's zero-arg
    `Devm()`/`Workspace("proj_ssh")`, which don't match the actual
    constructors in helpers/devm.py and helpers/workspace.py.
"""
from __future__ import annotations

import os
import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


def _get_runtime_dir() -> Path:
    """Get the devm runtime directory.

    In isolated mode (E2E_ISOLATE=1), uses DEVM_RUNTIME_DIR from env.
    Otherwise uses the default ~/Library/Application Support/devm.
    Mirrors test_93_ssh_access.py's helper of the same name.
    """
    if os.environ.get("E2E_ISOLATE") == "1":
        isolated_dir = os.environ.get("DEVM_RUNTIME_DIR")
        if isolated_dir:
            return Path(isolated_dir)
    return Path.home() / "Library" / "Application Support" / "devm"


@pytest.mark.timeout(400)
def test_ssh_config_uses_hostname_and_port_22(devm, workspace):
    runtime_dir = _get_runtime_dir()
    ssh_config = runtime_dir / "ssh_config"

    workspace.write_devmyaml()

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()!r}"

    # ssh_config is (re-)emitted as part of VM lifecycle events; give a
    # short settle in case the write races the shell's exit.
    deadline = time.monotonic() + 10
    text = ""
    while time.monotonic() < deadline:
        if ssh_config.is_file():
            text = ssh_config.read_text()
            if f"Host devm-{workspace.vm_name}" in text:
                break
        time.sleep(0.5)

    assert f"Host devm-{workspace.vm_name}" in text, (
        f"expected Host devm-{workspace.vm_name} in {ssh_config}, got:\n{text}"
    )
    assert f"HostName             {workspace.vm_name}.test" in text, (
        f"expected HostName {workspace.vm_name}.test, got:\n{text}"
    )
    assert "Port                 22" in text, (
        f"expected fixed Port 22, got:\n{text}"
    )
    assert "127.0.0.1" not in text, (
        f"no raw loopback IP should appear in ssh_config, got:\n{text}"
    )

    # Connectivity via the emitted config, pointed at directly (not
    # relying on the user's own ~/.ssh/config Include-ing it).
    r = subprocess.run(
        ["ssh", "-F", str(ssh_config), f"devm-{workspace.vm_name}", "true"],
        capture_output=True, timeout=30,
    )
    assert r.returncode == 0, (
        f"ssh -F {ssh_config} devm-{workspace.vm_name} failed: "
        f"{r.stderr.decode(errors='replace')!r}"
    )
