"""62: embedded static s6-log binary executes in the VM and handles rotation.

Pins that the s6-log binary embedded by devm (via go:embed from
internal/scripts/s6-log.linux-arm64) is present on the host, copied
to the VM workspace, and executes correctly inside the Tart VM.

What this pins:
  - The embedded s6-log binary exists at the expected host path.
  - The binary executes inside the VM without missing-library errors
    (static linkage: no libc/libskarnet dependency at runtime).
  - Basic write: piping into `s6-log <dir>` produces a `current` file.
  - Size-bounded rotation: writing >4096 bytes produces archive files.

Devm dependency: devm embeds s6-log at .devm/scripts/s6-log (rendered
into the VM workspace via internal/render). The wrap-bg.sh supervision
wrapper uses it for rotated background daemon logs.

The binary is always arm64 (Tart VMs are Apple Silicon). We copy it
from the host's internal/scripts/ into the workspace so it's visible
inside the VM at the same absolute path via virtio-fs.
"""
from __future__ import annotations

import os
import shutil

import pytest

pytestmark = pytest.mark.devm


def _embedded_s6_log_path() -> str:
    """Return the host path for the arm64 embedded s6-log binary."""
    here = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.dirname(here)
    # Tart VMs are always Apple Silicon (arm64).
    return os.path.join(repo_root, "internal", "scripts", "s6-log.linux-arm64")


@pytest.mark.timeout(180)
def test_embedded_s6_log_executes_and_rotates(workspace, devm, tart_sandbox):
    s6log_host = _embedded_s6_log_path()
    assert os.path.exists(s6log_host), (
        f"embedded s6-log.linux-arm64 not found at {s6log_host}; "
        f"check internal/scripts/ for the go:embed source"
    )

    # Copy into workspace so the binary is visible inside the VM at
    # the same absolute path via virtio-fs.
    s6log_in_ws = workspace.path / "s6-log"
    shutil.copy(s6log_host, s6log_in_ws)
    s6log_in_ws.chmod(0o755)

    s6log_vm = str(s6log_in_ws)

    assert tart_sandbox.state() == "running", (
        f"expected VM running; got {tart_sandbox.state()!r}"
    )

    # 1. The binary executes — no missing-library errors.
    #    s6-log has no --version; running it without args prints usage to
    #    stderr and exits non-zero. That's fine; we check for the absence
    #    of dynamic-linker failure messages.
    r = tart_sandbox.exec_shell(
        f"{s6log_vm} --help 2>&1 || {s6log_vm} -h 2>&1 || true"
    )
    combined = r.stdout + r.stderr
    assert "error while loading shared libraries" not in combined, (
        f"embedded s6-log failed to load (dynamic-linker error): {combined!r}"
    )
    assert "No such file or directory" not in combined or "s6-log" not in combined, (
        f"s6-log binary not found in VM at {s6log_vm}: {combined!r}"
    )

    # 2. Basic write: pipe into a logdir, find content in current.
    r = tart_sandbox.exec_shell(
        f"rm -rf /tmp/probe-s6 && mkdir -p /tmp/probe-s6 && "
        f"printf 'hello\\nworld\\n' | {s6log_vm} -b n3 s4096 /tmp/probe-s6 && "
        f"cat /tmp/probe-s6/current"
    )
    assert r.ok, f"basic write to s6-log logdir failed: {r.stderr!r}"
    assert "hello" in r.stdout and "world" in r.stdout, (
        f"expected 'hello' and 'world' in /tmp/probe-s6/current; got {r.stdout!r}"
    )

    # 3. Rotation: write >4096 bytes, expect at least one archive file.
    r = tart_sandbox.exec_shell(
        f"rm -rf /tmp/probe-s6-rot && mkdir -p /tmp/probe-s6-rot && "
        f"yes 'x' | head -c 20000 | {s6log_vm} -b n5 s4096 /tmp/probe-s6-rot && "
        f"ls /tmp/probe-s6-rot"
    )
    assert r.ok, f"rotation probe failed: {r.stderr!r}"
    archived = [
        line for line in r.stdout.splitlines()
        if line.startswith("@") and line.endswith(".s")
    ]
    assert archived, (
        f"expected at least one rotated archive in /tmp/probe-s6-rot; "
        f"ls output:\n{r.stdout}"
    )
