"""Tart-backed sandbox fixture and helpers.

Replaces the deleted helpers/sbx.py from the sbx era. Provides:
  - TartSandbox: thin wrapper with exec/ip/state primitives
  - Used by the tart_sandbox fixture in conftest.py
"""
from __future__ import annotations

import json
import subprocess
import time
from dataclasses import dataclass


@dataclass
class TartSandbox:
    """Handle for a Tart VM brought up by `devm shell`."""
    name: str

    def exec(self, *argv: str, timeout: float = 60.0) -> "ExecResult":
        """Run a command inside the VM via `tart exec`."""
        r = subprocess.run(
            ["tart", "exec", self.name, *argv],
            capture_output=True, timeout=timeout,
        )
        return ExecResult(
            stdout=r.stdout.decode(errors="replace"),
            stderr=r.stderr.decode(errors="replace"),
            exit_code=r.returncode,
        )

    def exec_shell(self, script: str, timeout: float = 60.0) -> "ExecResult":
        """Run a shell script inside the VM via `tart exec bash -c '...'`."""
        return self.exec("bash", "-c", script, timeout=timeout)

    def ip(self) -> str:
        """Return the VM's current vmnet IP. Empty string on error."""
        r = subprocess.run(
            ["tart", "ip", self.name],
            capture_output=True, timeout=10,
        )
        if r.returncode != 0:
            return ""
        return r.stdout.decode().strip()

    def state(self) -> str:
        """Return 'absent' / 'stopped' / 'running'."""
        r = subprocess.run(
            ["tart", "list", "--format=json"],
            capture_output=True, timeout=10,
        )
        if r.returncode != 0:
            return "absent"
        try:
            entries = json.loads(r.stdout.decode())
        except Exception:
            return "absent"
        for e in entries:
            name = e.get("Name") or e.get("name") or ""
            if name == self.name:
                state = (e.get("State") or e.get("state") or "").lower()
                return "running" if state == "running" else "stopped"
        return "absent"

    def wait_running(self, timeout: float = 60.0) -> bool:
        """Poll until state == 'running' or timeout."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.state() == "running":
                return True
            time.sleep(1.0)
        return False


@dataclass
class ExecResult:
    stdout: str
    stderr: str
    exit_code: int

    @property
    def ok(self) -> bool:
        return self.exit_code == 0
