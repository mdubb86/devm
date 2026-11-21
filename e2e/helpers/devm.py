"""Devm CLI wrapper for non-interactive subcommands.

Interactive `devm shell` goes through helpers.shell.Shell (pexpect).
Everything else (reconcile, stop, teardown, status, ...) is plain
subprocess with explicit timeouts. Non-zero exit raises DevmError
with the full stdout/stderr so debugging is straightforward.
"""
from __future__ import annotations
import re
import subprocess

import pexpect


class DevmError(RuntimeError):
    def __init__(self, args: list[str], returncode: int, stdout: str, stderr: str):
        self.args = args
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr
        super().__init__(
            f"{' '.join(args)} -> exit {returncode}\nstdout: {stdout}\nstderr: {stderr}"
        )


class Devm:
    def __init__(self, path: str, cwd: str | None = None):
        self.path = path
        self.cwd = cwd

    @classmethod
    def from_env(cls, cwd: str | None = None) -> "Devm":
        """Devm bound to the bootstrapped e2e binary (see `just e2e-bootstrap`)."""
        return cls("/usr/local/bin/devm-e2e", cwd=cwd)

    def _run(self, args: list[str], timeout: float, check: bool = True) -> subprocess.CompletedProcess:
        full = [self.path, *args]
        p = subprocess.run(full, capture_output=True, timeout=timeout, cwd=self.cwd, check=False)
        if check and p.returncode != 0:
            raise DevmError(full, p.returncode, p.stdout.decode(), p.stderr.decode())
        return p

    # --- subcommands ---

    def reconcile(
        self,
        *,
        yes: bool = False,
        dry_run: bool = False,
        json_out: bool = False,
        timeout: float = 90.0,
        check: bool = True,
    ) -> subprocess.CompletedProcess:
        args = ["reconcile"]
        if yes:
            args.append("--yes")
        if dry_run:
            args.append("--dry-run")
        if json_out:
            args.append("--json")
        return self._run(args, timeout=timeout, check=check)

    def stop(
        self,
        *,
        yes: bool = False,
        skip_snapshot: bool = False,
        timeout: float = 30.0,
        check: bool = True,
    ) -> subprocess.CompletedProcess:
        args = ["stop"]
        if yes:
            args.append("--yes")
        if skip_snapshot:
            args.append("--skip-snapshot")
        return self._run(args, timeout=timeout, check=check)

    def teardown(
        self,
        *,
        yes: bool = False,
        skip_snapshot: bool = False,
        timeout: float = 30.0,
        check: bool = True,
    ) -> subprocess.CompletedProcess:
        args = ["teardown"]
        if yes:
            args.append("--yes")
        if skip_snapshot:
            args.append("--skip-snapshot")
        return self._run(args, timeout=timeout, check=check)

    def snapshot(
        self,
        verb: str,
        name: str | None = None,
        *extra: str,
        timeout: float = 120.0,
        check: bool = True,
    ) -> subprocess.CompletedProcess:
        """Dispatch `devm snapshot <verb>` with optional name and extra args.

        verb is one of 'create', 'restore', 'list', 'prune', 'show'.
        Returns the CompletedProcess; raises DevmError on non-zero exit
        when check is True (consistent with other Devm methods).
        """
        args = ["snapshot", verb]
        if name is not None:
            args.append(name)
        args.extend(extra)
        return self._run(args, timeout=timeout, check=check)

    def unlock(self, *, duration: str | None = None, timeout: float = 30.0) -> subprocess.CompletedProcess:
        """No-op: config-lock is not part of the mutagen-volumes shape.
        The subcommand no longer exists on the CLI; kept as a helper
        method so tests written for the older shape don't have to be
        edited just to skip a call. Returns a fake success result.
        """
        return subprocess.CompletedProcess(args=["unlock"], returncode=0, stdout=b"", stderr=b"")

    def lock(self, *, timeout: float = 30.0) -> subprocess.CompletedProcess:
        """No-op: see unlock()."""
        return subprocess.CompletedProcess(args=["lock"], returncode=0, stdout=b"", stderr=b"")

    def approve(self, *, timeout: float = 30.0) -> subprocess.CompletedProcess:
        """Run `devm approve` under a real pty so the TTY-gate accepts
        the "y" answer. `devm approve` refuses piped stdin by design
        (scripts cannot self-approve), so we drive it with pexpect the
        same way test_03_stop drives `devm stop`.
        Raises DevmError on non-zero exit.
        """
        full = [self.path, "approve"]
        child = pexpect.spawn(
            self.path, ["approve"], cwd=self.cwd,
            encoding="utf-8", timeout=timeout, dimensions=(40, 200),
        )
        try:
            idx = child.expect([
                re.compile(r"Approve these changes\?\s*\[y/N\]\s*"),
                r"devm\.yaml is already approved",
                pexpect.EOF,
            ], timeout=timeout)
            if idx == 0:
                child.sendline("y")
                child.expect(pexpect.EOF, timeout=timeout)
            # idx 1 or 2: nothing to approve or already exited; still
            # drain to EOF to collect exit status.
            child.close()
        finally:
            if child.isalive():
                child.close(force=True)
        rc = child.exitstatus if child.exitstatus is not None else -1
        if rc != 0:
            raise DevmError(full, rc, child.before or "", "")
        return subprocess.CompletedProcess(args=full, returncode=0, stdout=(child.before or "").encode(), stderr=b"")

    def status(
        self,
        *,
        json_out: bool = False,
        live: bool = False,
        timeout: float = 20.0,
    ) -> subprocess.CompletedProcess:
        args = ["status"]
        if json_out:
            args.append("--json")
        if live:
            args.append("--live")
        return self._run(args, timeout=timeout)
