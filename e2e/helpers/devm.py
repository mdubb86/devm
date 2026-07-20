"""Devm CLI wrapper for non-interactive subcommands.

Interactive `devm shell` goes through helpers.shell.Shell (pexpect).
Everything else (reconcile, stop, teardown, status, ...) is plain
subprocess with explicit timeouts. Non-zero exit raises DevmError
with the full stdout/stderr so debugging is straightforward.
"""
from __future__ import annotations
import subprocess


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

    def stop(self, *, yes: bool = False, timeout: float = 30.0) -> subprocess.CompletedProcess:
        args = ["stop"]
        if yes:
            args.append("--yes")
        return self._run(args, timeout=timeout)

    def teardown(self, *, yes: bool = False, timeout: float = 30.0) -> subprocess.CompletedProcess:
        args = ["teardown"]
        if yes:
            args.append("--yes")
        return self._run(args, timeout=timeout)

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
