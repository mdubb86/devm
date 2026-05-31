"""Shared fixtures for the devm e2e suite.

The fixtures register cleanup intent in the env-shared registry file
BEFORE creating any resource. If a fixture's finalizer doesn't run
(pytest SIGKILL, wedged worker), the bash wrapper's EXIT trap sweeps
the registry. See docs/superpowers/specs/2026-05-30-e2e-pexpect-rewrite-design.md.
"""
from __future__ import annotations
import os
import re
import secrets
import shutil
import tempfile
import time
from pathlib import Path
from typing import Callable, Iterator

import pytest

from helpers import Devm, Workspace, registry, sbx


# --- session ---

@pytest.fixture(scope="session")
def devm() -> Devm:
    """Built devm binary (run.sh exports DEVM_BIN)."""
    return Devm.from_env()


# --- per-test ---

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
        sbx.sandbox_rm(name)
        registry.remove("sandbox", name)


@pytest.fixture
def workspace(request, sandbox_name) -> Iterator[Workspace]:
    """Temp workspace dir bound to the test's sandbox_name.

    Derives a per-test `port_offset` from the sandbox name's random
    suffix so parallel tests can't collide on host ports. With
    canonical ports ≤ ~10000, 100-port buckets in [50000, 57000]
    give ~70 disjoint slots — plenty of room for our ~14 tests.
    """
    slug = _slug_from_node(request.node.name)
    path = Path(tempfile.mkdtemp(prefix=f"devm-e2e-{slug}-"))
    registry.append("workspace", str(path))
    try:
        # sandbox_name shape: "e2e-<slug>-<rand4hex>"; last 4 hex chars are the suffix.
        suffix_hex = sandbox_name.rsplit("-", 1)[-1]
        port_offset = 50000 + (int(suffix_hex, 16) % 70) * 100
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
            sbx.policy_remove(domain)
            registry.remove("policy", domain)


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
