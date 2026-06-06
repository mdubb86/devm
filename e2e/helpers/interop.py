"""Helpers for sbx_interop tests — building + running Go probe binaries.

The interop cohort each pins one Go-primitive ↔ sbx combination devm
depends on. Most tests follow the same skeleton:

  1. Build the Go probe binary inline (not checked into git).
  2. Run it with arguments specific to the test.
  3. Assert on exit code + captured output.

build_probe + run_probe factor out the build + run boilerplate so
the test bodies focus on what's being asserted, not subprocess
plumbing.
"""
from __future__ import annotations

import os
import subprocess
import tempfile
from dataclasses import dataclass

import pytest

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def build_probe(name: str, *, timeout: float = 60.0) -> str:
    """Build the Go probe binary at e2e/probes/<name>/. Returns its path.

    Compiles into a tempfile (not checked into git). Calls pytest.fail
    if the build fails — there's no useful test outcome past a build
    error.
    """
    src = os.path.join(REPO_ROOT, "e2e", "probes", name)
    if not os.path.isdir(src):
        pytest.fail(f"probe source missing: {src}")
    binpath = tempfile.mktemp(prefix=f"devm-{name}-")
    r = subprocess.run(
        ["go", "build", "-o", binpath, f"./e2e/probes/{name}/"],
        cwd=REPO_ROOT, capture_output=True, timeout=timeout,
    )
    if r.returncode != 0:
        pytest.fail(
            f"go build of {name} failed: rc={r.returncode}\n"
            f"stdout={r.stdout.decode()!r}\nstderr={r.stderr.decode()!r}"
        )
    return binpath


@dataclass
class ProbeResult:
    returncode: int
    stdout: bytes
    stderr: bytes


def run_probe(binpath: str, *args: str, timeout: float = 30.0) -> ProbeResult:
    """Run the probe binary with the given args; capture rc + stdout + stderr.

    Use the returned ProbeResult to assert on outcomes. Times out via
    subprocess (not pytest) so the failure message identifies the
    probe specifically rather than the surrounding test.
    """
    r = subprocess.run(
        [binpath, *args],
        capture_output=True, timeout=timeout,
    )
    return ProbeResult(
        returncode=r.returncode,
        stdout=r.stdout,
        stderr=r.stderr,
    )
