"""Thin Python wrappers around the sbx CLI.

Every call has an explicit timeout. Functions that query state return
parsed data; failures raise ``SbxError`` with full stdout/stderr so
debugging doesn't require digging through subprocess plumbing.
"""
from __future__ import annotations
import json
import subprocess
import time as _time
from typing import Any


class SbxError(RuntimeError):
    def __init__(self, cmd: list[str], returncode: int, stdout: str, stderr: str):
        self.cmd = cmd
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr
        super().__init__(
            f"sbx {' '.join(cmd)} -> exit {returncode}\nstdout: {stdout}\nstderr: {stderr}"
        )


def _run(args: list[str], timeout: float) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["sbx", *args],
        capture_output=True,
        timeout=timeout,
        check=False,
    )


def ls_raw(timeout: float = 10.0) -> str:
    p = _run(["ls"], timeout=timeout)
    if p.returncode != 0:
        raise SbxError(["ls"], p.returncode, p.stdout.decode(), p.stderr.decode())
    return p.stdout.decode()


def sandbox_exists(name: str, *, timeout: float = 10.0) -> bool:
    """Return True if `sbx ls` lists a sandbox with this name."""
    for line in ls_raw(timeout=timeout).splitlines()[1:]:  # skip header
        fields = line.split()
        if fields and fields[0] == name:
            return True
    return False


def sandbox_state(name: str, *, timeout: float = 10.0) -> str | None:
    """Return the sandbox's STATUS column ('running'/'stopped'), or None."""
    for line in ls_raw(timeout=timeout).splitlines()[1:]:
        fields = line.split()
        if len(fields) >= 3 and fields[0] == name:
            return fields[2]
    return None


def ports(name: str, *, timeout: float = 10.0) -> list[dict[str, Any]]:
    """`sbx ports <name> --json` parsed."""
    p = _run(["ports", name, "--json"], timeout=timeout)
    if p.returncode != 0:
        raise SbxError(["ports", name, "--json"], p.returncode, p.stdout.decode(), p.stderr.decode())
    out = p.stdout.decode().strip()
    if not out:
        return []
    return json.loads(out)


def policy_list_network(*, timeout: float = 10.0) -> str:
    """Raw output of `sbx policy ls --type network` (search via substring)."""
    p = _run(["policy", "ls", "--type", "network"], timeout=timeout)
    if p.returncode != 0:
        raise SbxError(["policy", "ls", "--type", "network"], p.returncode, p.stdout.decode(), p.stderr.decode())
    return p.stdout.decode()


def policy_remove(domain: str, *, timeout: float = 10.0) -> None:
    """Best-effort: ignore non-zero (policy may not exist)."""
    _run(["policy", "rm", "network", "--resource", domain], timeout=timeout)


def sandbox_rm(name: str, *, timeout: float = 30.0) -> None:
    """Best-effort: ignore non-zero (sandbox may not exist).

    `-f` skips the confirmation prompt sbx 0.29+ added. Without it,
    rm hangs waiting for stdin (we pass DEVNULL) and orphaned
    sandboxes accumulate across the suite.
    """
    _run(["rm", "-f", name], timeout=timeout)


def wait_for_port_published(
    name: str, *, host_port: int | None = None, sandbox_port: int | None = None,
    timeout: float = 30.0, poll: float = 0.25,
) -> None:
    """Poll `sbx ports --json` until a mapping matching host_port and/or
    sandbox_port appears. sbx's publish→list visibility can lag briefly;
    documented in docs/sbx-quirks.md.
    """
    deadline = _time.monotonic() + timeout
    last: list[dict] = []
    while _time.monotonic() < deadline:
        last = ports(name)
        for m in last:
            if host_port is not None and m.get("host_port") != host_port:
                continue
            if sandbox_port is not None and m.get("sandbox_port") != sandbox_port:
                continue
            return
        _time.sleep(poll)
    raise AssertionError(
        f"sbx ports for {name} never showed host={host_port} sandbox={sandbox_port} "
        f"within {timeout}s; last list: {last}"
    )


def wait_for_port_absent(
    name: str, *, host_port: int | None = None, sandbox_port: int | None = None,
    timeout: float = 30.0, poll: float = 0.25,
) -> None:
    """Poll `sbx ports --json` until a matching mapping is absent."""
    deadline = _time.monotonic() + timeout
    while _time.monotonic() < deadline:
        current = ports(name)
        match = any(
            (host_port is None or m.get("host_port") == host_port)
            and (sandbox_port is None or m.get("sandbox_port") == sandbox_port)
            for m in current
        )
        if not match:
            return
        _time.sleep(poll)
    raise AssertionError(
        f"sbx ports for {name} still has host={host_port} sandbox={sandbox_port} after {timeout}s"
    )
