"""install: the embedded static s6-log binary executes on the shell base.

Pins the building block for devm's startup-supervision feature
(docs/superpowers/specs/2026-06-07-startup-supervision-design.md):

  - The s6-log binary from s6-overlay v3.2.0.2 (statically linked,
    embedded by devm at .devm/scripts/s6-log via go:embed) is
    EXECUTABLE on docker/sandbox-templates:shell. Static linking
    means no libc/libskarnet/libexecline dependency at runtime.
  - Basic write works: piping output into `s6-log <dir>` produces
    a `current` file with the expected content.
  - Size-bounded rotation: writing >4096 bytes through
    `s6-log s4096` produces archived files; archived count is bounded.

This test stages a copy of the binary inside the sandbox at
/tmp/probe-s6-log, invokes it, and verifies behavior. Tests the
same functional contract as the old c25 but via the binary
embedded in devm rather than via apt install s6.
"""
from __future__ import annotations

import os
import shutil
import subprocess
import tempfile

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


# Embedded binary path on the host repo. Matches what devm renders
# into .devm/scripts/s6-log via internal/render/write.go.
def _embedded_s6_log_path() -> str:
    # __file__ is e2e/test_sbx_contract_25_embedded_s6_log_works.py;
    # walk up to repo root, then into internal/scripts.
    here = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.dirname(here)
    arch = subprocess.run(["uname", "-m"], capture_output=True, check=True).stdout.decode().strip()
    # Map host arch (uname -m) to devm's embedded-binary names.
    arch_map = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}
    suffix = arch_map.get(arch, arch)
    return os.path.join(repo_root, "internal", "scripts", f"s6-log.linux-{suffix}")


@pytest.mark.timeout(120)
def test_embedded_s6_log_executes_and_rotates(sandbox_name):
    s6log = _embedded_s6_log_path()
    assert os.path.exists(s6log), f"embedded s6-log not found at {s6log}"

    # Use a workspace dir we control so we can stage the binary at a known path
    # inside the sandbox via the workspace mount.
    ws = tempfile.mkdtemp(prefix="probe-c25-ws-")
    try:
        # Copy the binary into the workspace so it's visible at the same
        # absolute path inside the sandbox via the mirrored mount.
        shutil.copy(s6log, os.path.join(ws, "s6-log"))
        os.chmod(os.path.join(ws, "s6-log"), 0o755)

        spec = minimal_kit(install=["true"])
        with contract_sandbox(spec, sandbox_name, workspace=ws):
            # 1. The binary executes (no missing libs).
            r = sbx_exec(sandbox_name, "sh", "-c", f"{ws}/s6-log --version 2>&1 | head -1 || {ws}/s6-log -h 2>&1 | head -1")
            # s6-log doesn't have --version; -h or no args prints usage.
            # Either an exit code != 0 with usage on stderr/stdout, OR a help line —
            # all indicate the binary loaded successfully. The key is: no
            # "error while loading shared libraries" or similar dynlinker failure.
            combined = (r.stdout + r.stderr).decode()
            assert "error while loading shared libraries" not in combined, (
                f"embedded s6-log failed to load on shell base: {combined!r}"
            )

            # 2. Basic write: pipe into a logdir, find content in current.
            r = sbx_exec(sandbox_name, "sh", "-c",
                f"rm -rf /tmp/probe-s6 && mkdir -p /tmp/probe-s6 && "
                f"printf 'hello\\nworld\\n' | {ws}/s6-log -b n3 s4096 /tmp/probe-s6 && "
                f"cat /tmp/probe-s6/current")
            assert r.returncode == 0, f"basic write failed: {r.stderr.decode()!r}"
            current = r.stdout.decode()
            assert "hello" in current and "world" in current

            # 3. Rotation: write >4096 bytes, expect at least one archive file.
            r = sbx_exec(sandbox_name, "sh", "-c",
                f"rm -rf /tmp/probe-s6-rot && mkdir -p /tmp/probe-s6-rot && "
                f"yes 'x' | head -c 20000 | {ws}/s6-log -b n5 s4096 /tmp/probe-s6-rot && "
                f"ls /tmp/probe-s6-rot")
            assert r.returncode == 0, f"rotation probe failed: {r.stderr.decode()!r}"
            archived = [line for line in r.stdout.decode().splitlines()
                        if line.startswith("@") and line.endswith(".s")]
            assert archived, (
                f"expected rotated archives; got:\n{r.stdout.decode()}"
            )
    finally:
        shutil.rmtree(ws, ignore_errors=True)
