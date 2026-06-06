"""Shared utilities for the sbx_contract test cohort.

Provides:

  * minimal_kit(...) — build a spec.yaml string with optional install,
    startup, network, volumes, environment, aiFilename. Each contract
    test composes only what it needs.

  * contract_sandbox(spec_yaml, name, *, workspace=None,
                     extra_positionals=None) — context manager that
    materializes a kit + workspace, spawns `sbx run`, waits until
    `sbx ls` reports running AND `sbx exec NAME true` succeeds, yields,
    then tears down sandbox + cleans up tmpdirs. Use for tests where
    the sandbox must come up.

  * sbx_run_until_exit(spec_yaml, name, *, timeout=90) → (rc, stderr) —
    spawns `sbx run` and waits for it to exit (without expecting
    success). Use for failure-mode tests (L2, L3).

  * sbx_exec(name, *args, timeout=30) → CompletedProcess — small wrapper
    around `subprocess.run(["sbx", "exec", name, *args])`.

Agent name is hardcoded to "probe" and image to docker/sandbox-
templates:shell so tests are fast and consistent.
"""
from __future__ import annotations
import os
import shutil
import subprocess
import tempfile
import time
from contextlib import contextmanager
from typing import Iterator

import yaml

from helpers import sbx as sbx_helper


def minimal_kit(
    install: list[str] | None = None,
    startup: list[dict] | None = None,
    network_allowed: list[str] | None = None,
    volumes: dict | None = None,
    extra_env: dict | None = None,
    ai_filename: str = "CLAUDE.md",
) -> str:
    """Build a kit spec.yaml string with the given customizations.

    install: list of shell command strings (each becomes one install: entry).
             Defaults to ["true"] (a no-op).
    startup: list of dicts with keys command (list[str]), user (str),
             description (str), background (bool — optional).
             Defaults to a single no-op step as user 1000.
    network_allowed: list of domains for network.allowedDomains.
    volumes: dict of {path: "size=N"} for the kit's volumes map.
    extra_env: extra entries for environment.variables (merged with IS_SANDBOX).
    ai_filename: agent.aiFilename value. Defaults to CLAUDE.md.
    """
    spec = {
        "schemaVersion": "1",
        "kind": "agent",
        "name": "probe",
        "displayName": "contract probe",
        "description": "contract cohort test kit",
        "agent": {
            "image": "docker/sandbox-templates:shell",
            "aiFilename": ai_filename,
            "entrypoint": {
                "run": ["sh", "-c", "exec sleep infinity </dev/null"],
            },
        },
        "environment": {
            "variables": {"IS_SANDBOX": "1", **(extra_env or {})},
        },
        "commands": {
            "install": [{"command": c} for c in (install or ["true"])],
            "startup": startup or [
                {"command": ["sh", "-c", "true"], "user": "1000", "description": "noop"}
            ],
        },
    }
    if network_allowed:
        spec["network"] = {"allowedDomains": list(network_allowed)}
    if volumes:
        spec["volumes"] = dict(volumes)
    return yaml.safe_dump(spec, sort_keys=False, default_flow_style=False)


def _wait_running(name: str, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if sbx_helper.sandbox_state(name) == "running":
            return
        time.sleep(0.5)
    raise AssertionError(f"sandbox {name} never reached running within {timeout}s")


def _wait_exec_ready(name: str, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        r = subprocess.run(
            ["sbx", "exec", name, "true"], capture_output=True, timeout=5,
        )
        if r.returncode == 0:
            return
        time.sleep(0.5)
    raise AssertionError(f"sandbox {name} not exec-ready within {timeout}s")


@contextmanager
def contract_sandbox(
    spec_yaml: str,
    name: str,
    *,
    workspace: str | None = None,
    extra_positionals: list[str] | None = None,
) -> Iterator[None]:
    """Materialize a kit, spawn sbx run, wait until exec-ready, yield,
    clean up on exit (stop+rm the sandbox, remove tmpdirs).

    `extra_positionals` are additional workspace paths appended after
    the primary workspace argument — used by the mount contract tests
    (M1, M2).
    """
    kit_dir = tempfile.mkdtemp(prefix="contract-kit-")
    cleanup_ws = workspace is None
    ws = workspace or tempfile.mkdtemp(prefix="contract-ws-")
    with open(os.path.join(kit_dir, "spec.yaml"), "w") as f:
        f.write(spec_yaml)

    argv = ["sbx", "run", "--kit", kit_dir, "--name", name, "probe", ws]
    if extra_positionals:
        argv.extend(extra_positionals)
    proc = subprocess.Popen(
        argv,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
    )
    try:
        _wait_running(name, timeout=90)
        _wait_exec_ready(name, timeout=30)
        yield
    finally:
        subprocess.run(["sbx", "stop", name], capture_output=True, timeout=15)
        subprocess.run(["sbx", "rm", "-f", name], capture_output=True, timeout=15)
        try:
            proc.kill()
            proc.wait(timeout=5)
        except Exception:
            pass
        shutil.rmtree(kit_dir, ignore_errors=True)
        if cleanup_ws:
            shutil.rmtree(ws, ignore_errors=True)


def sbx_run_until_exit(
    spec_yaml: str,
    name: str,
    *,
    timeout: float = 90.0,
) -> tuple[int, str]:
    """Spawn `sbx run` and wait until it exits. Returns (rc, stderr).

    Use for failure-mode tests (L2, L3) where the sandbox is NOT
    expected to come up. Cleans up tmpdirs and force-removes any
    stray sandbox name afterward.
    """
    kit_dir = tempfile.mkdtemp(prefix="contract-kit-")
    ws = tempfile.mkdtemp(prefix="contract-ws-")
    with open(os.path.join(kit_dir, "spec.yaml"), "w") as f:
        f.write(spec_yaml)
    try:
        p = subprocess.run(
            ["sbx", "run", "--kit", kit_dir, "--name", name, "probe", ws],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
        )
        return p.returncode, p.stderr.decode()
    finally:
        subprocess.run(["sbx", "rm", "-f", name], capture_output=True, timeout=15)
        shutil.rmtree(kit_dir, ignore_errors=True)
        shutil.rmtree(ws, ignore_errors=True)


def sbx_exec(name: str, *args: str, timeout: float = 30.0) -> subprocess.CompletedProcess:
    """sbx exec NAME args — convenience wrapper around subprocess.run."""
    return subprocess.run(
        ["sbx", "exec", name, *args], capture_output=True, timeout=timeout,
    )
