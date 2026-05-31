"""pexpect-based interactive shell wrapper for `devm shell`.

Used as a context manager:

    with Shell(devm, cwd=workspace.path) as sh:
        sh.expect_prompt()
        sh.run_check("test -e /home/agent/marker", expect_zero=True)

Every wait has an explicit timeout. Exceptions on timeout/EOF carry
the buffered output so failures are debuggable. `run_check` uses an
echo-tag pattern (`echo "TAG=$?"`) so the matched value (`=0` / `=1`)
never appears in the echoed command — sidesteps a class of desync
bugs we hit in the prior Tcl/expect harness.
"""
from __future__ import annotations
import re
import secrets

import pexpect


# Bash prompt from the sbx-templates:shell agent.
# Shape: `agent@e2e-<slug>-<rand>:workspace$ ` — basename cwd, optional trailing space.
PROMPT_RE = r"agent@\S+:\S+\$ ?"


class ShellTimeoutError(RuntimeError):
    def __init__(self, op: str, timeout: float, buffer: str):
        super().__init__(f"{op}: timed out after {timeout}s.\nbuffered: {buffer!r}")
        self.op = op
        self.timeout = timeout
        self.buffer = buffer


class ShellEofError(RuntimeError):
    def __init__(self, op: str, buffer: str):
        super().__init__(f"{op}: unexpected EOF.\nbuffered: {buffer!r}")
        self.op = op
        self.buffer = buffer


class Shell:
    def __init__(self, devm, cwd: str, args: list[str] | None = None):
        """devm: a Devm instance (uses .path). cwd: passed to spawn."""
        self._spawn_args = [devm.path, "shell", *(args or [])]
        self._cwd = cwd
        self._child: pexpect.spawn | None = None

    def __enter__(self) -> "Shell":
        # Wide window; we set encoding so .before is str.
        self._child = pexpect.spawn(
            self._spawn_args[0],
            self._spawn_args[1:],
            cwd=self._cwd,
            encoding="utf-8",
            timeout=60,
            dimensions=(40, 200),
        )
        return self

    def __exit__(self, exc_type, exc_val, exc_tb) -> None:
        c = self._child
        if c is None:
            return
        # Best-effort terminate; tests should have already exited the shell.
        try:
            if c.isalive():
                c.sendline("exit")
                c.expect(pexpect.EOF, timeout=10)
        except Exception:
            pass
        finally:
            try:
                c.close(force=True)
            except Exception:
                pass

    @property
    def child(self) -> pexpect.spawn:
        if self._child is None:
            raise RuntimeError("Shell used outside of context manager")
        return self._child

    # --- wait ops ---

    def expect_prompt(self, timeout: float = 60.0) -> None:
        c = self.child
        try:
            c.expect(PROMPT_RE, timeout=timeout)
        except pexpect.TIMEOUT:
            raise ShellTimeoutError("expect_prompt", timeout, c.before or "") from None
        except pexpect.EOF:
            raise ShellEofError("expect_prompt", c.before or "") from None

    def expect_eof(self, timeout: float = 30.0) -> None:
        c = self.child
        try:
            c.expect(pexpect.EOF, timeout=timeout)
        except pexpect.TIMEOUT:
            raise ShellTimeoutError("expect_eof", timeout, c.before or "") from None

    def expect_text(self, pattern: str, timeout: float = 30.0) -> None:
        """Match an arbitrary regex (not the prompt)."""
        c = self.child
        try:
            c.expect(pattern, timeout=timeout)
        except pexpect.TIMEOUT:
            raise ShellTimeoutError(f"expect_text({pattern!r})", timeout, c.before or "") from None
        except pexpect.EOF:
            raise ShellEofError(f"expect_text({pattern!r})", c.before or "") from None

    # --- run ops ---

    def send(self, line: str) -> None:
        """Send a line + Enter. Does NOT wait for output."""
        self.child.sendline(line)

    def run(self, cmd: str, *, timeout: float = 30.0) -> None:
        """Send a command and wait for the next prompt. No assertion on exit."""
        self.send(cmd)
        self.expect_prompt(timeout=timeout)

    def run_check(self, cmd: str, *, expect_zero: bool = True, timeout: float = 30.0) -> None:
        """Run a command; assert its exit code matches expect_zero.

        Implementation note: we wrap the user's command as
            `<cmd>; echo "DEVM_E2E_<tag>=$?"`
        and match against `DEVM_E2E_<tag>=0` / `=1`. The matched value
        (=0/=1) does NOT appear in the echoed command line, so expect
        can't match the echoed command by mistake and desync. The tag
        is a random hex string per call so back-to-back invocations
        don't collide in the buffer.
        """
        tag = "DEVM_E2E_" + secrets.token_hex(4)
        wrapped = f'{cmd}; echo "{tag}=$?"'
        self.send(wrapped)
        pattern = re.escape(tag) + r"=(\d+)"
        c = self.child
        try:
            c.expect(pattern, timeout=timeout)
        except pexpect.TIMEOUT:
            raise ShellTimeoutError(f"run_check({cmd!r})", timeout, c.before or "") from None
        except pexpect.EOF:
            raise ShellEofError(f"run_check({cmd!r})", c.before or "") from None
        rc = int(c.match.group(1))
        # Drain to the next prompt so subsequent calls start clean.
        self.expect_prompt(timeout=timeout)
        if expect_zero and rc != 0:
            raise AssertionError(f"{cmd!r} exited {rc}; expected 0")
        if not expect_zero and rc == 0:
            raise AssertionError(f"{cmd!r} exited 0; expected non-zero")

    def exit(self, *, timeout: float = 30.0) -> None:
        """Send `exit`, wait for EOF."""
        self.send("exit")
        self.expect_eof(timeout=timeout)
