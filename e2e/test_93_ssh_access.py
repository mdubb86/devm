"""93: End-to-end SSH access to a devm VM via the emitted ssh_config.

Run against the bootstrapped devm-e2e install via `just e2e`.

Cold-starts a project, then pins the full SSH lifecycle:
  1. ssh_config file exists at RuntimeDir/ssh_config.
  2. One Host block for the running project is present.
  3. `ssh -F <path> devm-<vm-name> uname -a` returns 0 with Linux.
  4. `ssh -F <path> devm-<vm-name> whoami` returns 'devm'.
  5. Stopping the project removes the Host block.
  6. `devm teardown --yes` wipes the per-project ssh subtree.

Daemon-restart re-emission (does a crashed/restarted daemon rediscover
running VMs and restore ssh_config?) is NOT covered here — it would
need a real `devm service restart` (see test_73/test_100 for that
pattern), not a second cold-start of the same already-destroyed
project.
"""
from __future__ import annotations

import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


def _get_runtime_dir() -> Path:
    """The bootstrapped devm-e2e daemon's runtime directory."""
    return Path.home() / "Library/Application Support/devm-e2e"


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_ssh_access_end_to_end(devm, workspace):
    """Test SSH access lifecycle: emission, connection, stop/destroy/re-emission."""
    runtime_dir = _get_runtime_dir()
    ssh_config = runtime_dir / "ssh_config"

    # --- Step 1-2: Cold-start and verify ssh_config file + Host block exist ---
    workspace.write_devmyaml()

    # Cold-start via shell (no-op exit, triggers full provisioning)
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=300,
    )
    assert r.returncode == 0, (
        f"cold-start failed:\nstdout={r.stdout.decode()!r}\n"
        f"stderr={r.stderr.decode()!r}"
    )

    # Step 1: ssh_config file exists.
    assert ssh_config.is_file(), f"expected {ssh_config} to exist"

    # Step 2: Host block present.
    body = ssh_config.read_text()
    assert f"Host devm-{workspace.vm_name}" in body, (
        f"expected Host devm-{workspace.vm_name} in ssh_config, got:\n{body}"
    )
    assert f'IdentityFile         "{runtime_dir / "ssh" / "projects" / workspace.slug / "id_ed25519"}"' in body, (
        f"expected IdentityFile path in Host block, got:\n{body}"
    )

    # --- Step 3-4: SSH into VM and run commands ---
    # Step 3: `ssh -F <path> devm-<vm-name> uname -a` works.
    result = subprocess.run(
        ["ssh", "-F", str(ssh_config), f"devm-{workspace.vm_name}", "uname", "-a"],
        capture_output=True, text=True, timeout=30,
    )
    assert result.returncode == 0, (
        f"ssh uname failed:\nstdout={result.stdout!r}\nstderr={result.stderr!r}"
    )
    assert result.stdout.startswith("Linux"), (
        f"expected Linux uname, got {result.stdout!r}"
    )

    # Step 4: `ssh -F <path> devm-<vm-name> whoami` returns 'devm'.
    result = subprocess.run(
        ["ssh", "-F", str(ssh_config), f"devm-{workspace.vm_name}", "whoami"],
        capture_output=True, text=True, timeout=30,
    )
    assert result.returncode == 0, (
        f"ssh whoami failed:\nstdout={result.stdout!r}\nstderr={result.stderr!r}"
    )
    assert result.stdout.strip() == "devm", (
        f"expected devm user, got {result.stdout.strip()!r}"
    )

    # --- Step 5: Stop removes the Host block ---
    devm.stop(yes=True)
    # Small settle to ensure file is written
    time.sleep(1)
    body = ssh_config.read_text()
    assert f"Host devm-{workspace.vm_name}" not in body, (
        f"expected Host devm-{workspace.vm_name} to be removed after stop, but found it in:\n{body}"
    )

    # --- Step 6: Destroy wipes the per-project ssh subtree ---
    r = subprocess.run(
        [devm.path, "teardown", "--yes"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=60,
    )
    assert r.returncode == 0, (
        f"teardown failed:\nstdout={r.stdout.decode()!r}\n"
        f"stderr={r.stderr.decode()!r}"
    )
    project_dir = runtime_dir / "ssh" / "projects" / workspace.slug
    assert not project_dir.exists(), (
        f"expected {project_dir} to be removed by teardown, but it still exists"
    )
