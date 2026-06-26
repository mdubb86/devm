"""Shared fixtures for the devm e2e suite.

The fixtures register cleanup intent in the env-shared registry file
BEFORE creating any resource. If a fixture's finalizer doesn't run
(pytest SIGKILL, wedged worker), the bash wrapper's EXIT trap sweeps
the registry. See internal design notes.
"""
from __future__ import annotations
import os
import re
import secrets
import shutil
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Callable, Iterator

import pytest

from helpers import Devm, Workspace, registry




# --- session ---

@pytest.fixture(scope="session")
def devm_path() -> str:
    """Path to the freshly-built devm binary (run.sh exports DEVM_BIN)."""
    p = os.environ.get("DEVM_BIN")
    if not p:
        raise RuntimeError("DEVM_BIN not set (run.sh sets it; check the wrapper)")
    return p


# --- per-test ---

# (workspace is below; declared as a forward-ref dependency of `devm`.)
@pytest.fixture
def devm(devm_path, workspace) -> Devm:
    """Devm CLI wrapper bound to the test's workspace as cwd, so
    `devm.reconcile()` / `stop()` / `teardown()` read the test's
    devm.yaml (not e2e/'s)."""
    return Devm(devm_path, cwd=str(workspace.path))


def _slug_from_node(name: str) -> str:
    """`test_01_cold_start` -> `cold-start` (within 20 chars, alnum + hyphens)."""
    s = re.sub(r"^test_", "", name)
    s = re.sub(r"^\d+_", "", s)
    s = s.replace("_", "-")
    s = re.sub(r"[^a-z0-9-]", "", s.lower())
    return s[:20].rstrip("-") or "x"


@pytest.fixture
def sandbox_name(request) -> Iterator[str]:
    """Unique sandbox name; registered for cleanup before yield."""
    slug = _slug_from_node(request.node.name)
    name = f"e2e-{slug}-{secrets.token_hex(2)}"
    registry.append("sandbox", name)
    try:
        yield name
    finally:
        registry.remove("sandbox", name)


def _port_offset_from_file(filename: str) -> int:
    """Derive a deterministic, collision-free port_offset from the test
    file's NN_ prefix (e.g., `test_08_reconcile_live_port.py` -> 50800).

    Hash-of-sandbox-name with 70 buckets has a ~73% chance of collision
    across 14 parallel tests (birthday paradox), which makes the suite
    flaky. Using the test number from the filename gives every test its
    own bucket. Canonical ports we use are all coprime-mod-100 with each
    other (3000, 8080, 8081, 9090) so 100-port spacing rules out cross-
    canonical host-port collisions too.

    Note: take the filename, not request.node.name — the latter is the
    function name (e.g., `test_reconcile_live_port`), which has no digits.
    """
    m = re.match(r"^test_(\d+)_", filename)
    if not m:
        return 50000
    return 50000 + int(m.group(1)) * 100


@pytest.fixture
def workspace(request, sandbox_name) -> Iterator[Workspace]:
    """Temp workspace dir bound to the test's sandbox_name."""
    slug = _slug_from_node(request.node.name)
    # Resolve symlinks so the path matches what devm (Go) sees inside
    # the spawned shell. On macOS, tempfile.mkdtemp returns
    # `/var/folders/...` but pexpect.spawn(cwd=...) → chdir → Go
    # os.Getwd surfaces the canonical `/private/var/folders/...`. If we
    # yield the unresolved form, every test that compares against
    # workspace.path (CLAUDE_CONFIG_DIR, $WORKSPACE, …) trips on the
    # symlink. Linux is a no-op (mkdtemp already canonical).
    path = Path(tempfile.mkdtemp(prefix=f"devm-e2e-{slug}-")).resolve()
    registry.append("workspace", str(path))
    try:
        port_offset = _port_offset_from_file(Path(request.node.fspath).name)
        ws = Workspace(path, slug=slug, sandbox_name=sandbox_name, port_offset=port_offset)
        ws.write_devmyaml()  # minimal config; tests can call write_devmyaml again with extras
        yield ws
    finally:
        shutil.rmtree(path, ignore_errors=True)
        registry.remove("workspace", str(path))


@pytest.fixture
def policy_registrar() -> Iterator[Callable[[str], None]]:
    """Call `register(domain)` for any global sbx network policy a test adds.

    Registered domains are removed on test teardown (and the registry
    line is unregistered). sbx policies are GLOBAL and persist across
    sandboxes / sbx rm, so this is critical for test isolation.
    """
    added: list[str] = []

    def register(domain: str) -> None:
        added.append(domain)
        registry.append("policy", domain)

    try:
        yield register
    finally:
        for domain in added:
            registry.remove("policy", domain)


@pytest.fixture
def sudo_capable():
    """Skips the test if sudo can't realistically prompt the user.

    Skipped conditions:
      - non-macOS (sudo-required tests in this suite are macOS-only)
      - /dev/tty unavailable (CI / no controlling terminal — sudo
        would hang on a prompt it can't deliver)

    If sudo IS capable but not cached, the test will trigger Touch ID
    (or password) prompts naturally during its install/uninstall
    calls — sudo opens /dev/tty directly for the prompt, independent
    of pytest's capture of stdin/stdout. No priming machinery: on
    macOS, Touch ID doesn't share the sudo timestamp cache anyway,
    so priming just adds an extra interaction.
    """
    import platform as _platform

    if _platform.system() != "Darwin":
        pytest.skip("sudo-required test runs on macOS only")
    try:
        open("/dev/tty").close()
    except OSError:
        pytest.skip("no /dev/tty — sudo can't prompt, skipping interactive test")


@pytest.fixture
def phase():
    """Helper for sub-test phase timing.

        phase("cold-start-1")
        ...
        phase("reconcile")

    Prints `[phase] <label>  Xs (total Ys)` to stdout. Opt-in per
    test; intended for the slow recreate tests where breakdowns matter.
    """
    start = [time.monotonic()]
    last = [time.monotonic()]

    def mark(label: str) -> None:
        now = time.monotonic()
        delta = now - last[0]
        total = now - start[0]
        print(f"[phase] {label:<24} {delta:6.1f}s   (total {total:.1f}s)")
        last[0] = now

    return mark
