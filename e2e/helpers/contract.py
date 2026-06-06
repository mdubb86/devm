"""Shared utilities for the sbx_contract test cohort.

Provides:

  * minimal_kit(...) — build a spec.yaml string with optional install,
    startup, network, volumes, environment, aiFilename. Each contract
    test composes only what it needs.

  * contract_sandbox(spec_yaml, name, *, workspace=None,
                     extra_positionals=None) — context manager that
    materializes a kit + workspace, spawns `sbx run` UNDER A PTY,
    waits until `sbx ls` reports running AND `sbx exec NAME true`
    succeeds, yields a SandboxContext (with .captured() to read sbx's
    PTY output), then tears down sandbox + cleans up tmpdirs.

  * sbx_run_until_exit(spec_yaml, name, *, timeout=90) →
    (rc, captured_output) — spawns `sbx run` under a PTY and waits
    for it to exit (without expecting success). Returns sbx's PTY
    output for inspection. Use for failure-mode tests where sbx run
    DOES exit (e.g. install failure).

  * sbx_exec(name, *args, timeout=30) → CompletedProcess — small wrapper
    around `subprocess.run(["sbx", "exec", name, *args])`.

PTY rationale: sbx 0.31 only emits diagnostic output (ERROR lines,
progress, status) when stdio is a TTY. devm's anchor spawn also uses
PTY (internal/orchestrator/shell.go, Tier 1c), so the cohort matches
production. The PTY is fixed at 24x80 so sbx doesn't see 0x0 and
degrade.

Agent name is hardcoded to "probe" and image to docker/sandbox-
templates:shell so tests are fast and consistent.
"""
from __future__ import annotations
import errno
import fcntl
import os
import pty
import shutil
import struct
import subprocess
import tempfile
import termios
import threading
import time
from contextlib import contextmanager
from typing import Iterator

import yaml

from helpers import sbx as sbx_helper


PTY_ROWS = 24
PTY_COLS = 80


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
        # sbx 0.31 requires volumes: as a list of {path, size} MountSpec
        # entries. Accept the ergonomic {path: size} dict form and
        # translate. The map form (sbx <0.30) is REJECTED with
        # "cannot unmarshal !!map into []spec.MountSpec".
        items = []
        for path, size_str in volumes.items():
            # size_str is either "size=100M" (legacy devm render form) or
            # just "100M". Strip the legacy prefix.
            sz = size_str.removeprefix("size=") if isinstance(size_str, str) else size_str
            items.append({"path": path, "size": sz})
        spec["volumes"] = items
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


def _spawn_pty(argv: list[str]) -> tuple[subprocess.Popen, int, bytearray, threading.Event, threading.Thread]:
    """Spawn argv under a PTY (24x80). Returns (proc, master_fd, buf,
    stop_event, drain_thread). The drain thread copies bytes from
    master_fd into buf until stop_event is set or EOF.

    Caller is responsible for setting stop_event and joining the thread
    on cleanup (then closing master_fd).
    """
    master_fd, slave_fd = pty.openpty()
    # Set the PTY size to 24x80 so sbx doesn't see 0x0 and degrade.
    winsize = struct.pack("HHHH", PTY_ROWS, PTY_COLS, 0, 0)
    fcntl.ioctl(master_fd, termios.TIOCSWINSZ, winsize)

    proc = subprocess.Popen(
        argv,
        stdin=slave_fd,
        stdout=slave_fd,
        stderr=slave_fd,
        close_fds=True,
    )
    # The child has its own copy of slave_fd now; we don't need ours.
    os.close(slave_fd)

    buf = bytearray()
    stop = threading.Event()

    def _drain():
        while not stop.is_set():
            try:
                data = os.read(master_fd, 4096)
                if not data:
                    return
                buf.extend(data)
            except OSError as e:
                if e.errno in (errno.EIO, errno.EBADF):
                    return
                raise

    drain = threading.Thread(target=_drain, daemon=True)
    drain.start()
    return proc, master_fd, buf, stop, drain


def _stop_drain(master_fd: int, stop: threading.Event, drain: threading.Thread) -> None:
    """Tell the drain thread to stop, close the master fd, join."""
    stop.set()
    try:
        os.close(master_fd)
    except Exception:
        pass
    drain.join(timeout=2)


class SandboxContext:
    """Yielded by contract_sandbox. Lets tests read what sbx printed on
    the PTY (use ``ctx.captured()`` for a snapshot of bytes-decoded-so-far).
    """
    def __init__(self, buf: bytearray):
        self._buf = buf

    def captured(self) -> str:
        return bytes(self._buf).decode(errors="replace")


@contextmanager
def contract_sandbox(
    spec_yaml: str,
    name: str,
    *,
    workspace: str | None = None,
    extra_positionals: list[str] | None = None,
) -> Iterator[SandboxContext]:
    """Materialize a kit, spawn sbx run under a PTY, wait until exec-
    ready, yield a SandboxContext (with .captured() for the sbx output),
    clean up on exit.

    `extra_positionals` are additional workspace paths appended after
    the primary workspace argument — used by the mount contract tests.
    """
    kit_dir = tempfile.mkdtemp(prefix="contract-kit-")
    cleanup_ws = workspace is None
    ws = workspace or tempfile.mkdtemp(prefix="contract-ws-")
    with open(os.path.join(kit_dir, "spec.yaml"), "w") as f:
        f.write(spec_yaml)

    argv = ["sbx", "run", "--kit", kit_dir, "--name", name, "probe", ws]
    if extra_positionals:
        argv.extend(extra_positionals)
    proc, master_fd, buf, stop, drain = _spawn_pty(argv)
    try:
        _wait_running(name, timeout=90)
        _wait_exec_ready(name, timeout=30)
        yield SandboxContext(buf)
    finally:
        subprocess.run(["sbx", "stop", name], capture_output=True, timeout=15)
        subprocess.run(["sbx", "rm", "-f", name], capture_output=True, timeout=15)
        try:
            proc.kill()
            proc.wait(timeout=5)
        except Exception:
            pass
        _stop_drain(master_fd, stop, drain)
        shutil.rmtree(kit_dir, ignore_errors=True)
        if cleanup_ws:
            shutil.rmtree(ws, ignore_errors=True)


def sbx_run_until_exit(
    spec_yaml: str,
    name: str,
    *,
    timeout: float = 90.0,
) -> tuple[int, str]:
    """Spawn `sbx run` under a PTY and wait until it exits. Returns
    (rc, captured_pty_output).

    Use for failure-mode tests where sbx run is expected to exit
    (e.g. L2 — install failure). Cleans up tmpdirs and force-removes
    any stray sandbox name afterward.
    """
    kit_dir = tempfile.mkdtemp(prefix="contract-kit-")
    ws = tempfile.mkdtemp(prefix="contract-ws-")
    with open(os.path.join(kit_dir, "spec.yaml"), "w") as f:
        f.write(spec_yaml)

    argv = ["sbx", "run", "--kit", kit_dir, "--name", name, "probe", ws]
    proc, master_fd, buf, stop, drain = _spawn_pty(argv)
    try:
        proc.wait(timeout=timeout)
        rc = proc.returncode
        # Let the drain thread catch any final bytes.
        time.sleep(0.5)
        return rc, bytes(buf).decode(errors="replace")
    finally:
        subprocess.run(["sbx", "rm", "-f", name], capture_output=True, timeout=15)
        try:
            proc.kill()
        except Exception:
            pass
        _stop_drain(master_fd, stop, drain)
        shutil.rmtree(kit_dir, ignore_errors=True)
        shutil.rmtree(ws, ignore_errors=True)


def sbx_exec(name: str, *args: str, timeout: float = 30.0) -> subprocess.CompletedProcess:
    """sbx exec NAME args — convenience wrapper around subprocess.run."""
    return subprocess.run(
        ["sbx", "exec", name, *args], capture_output=True, timeout=timeout,
    )
