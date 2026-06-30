"""Pin: `tart exec -i -t NAME bash` keeps an interactive bash alive; without
`-t`, bash exits immediately on EOF.

`devm shell` attaches the user's terminal to the in-VM shell via tart exec.
For bash to stay alive as an interactive session, tart needs to allocate
a PTY inside the VM (`-t`) and forward host stdin (`-i`). Without `-t`,
bash sees no TTY on its standard streams and exits before reading any
input — silently breaking `devm shell` interactivity.

This contract was implicit and unpinned until a regression made it loud
(see `internal/orchestrator/shell.go:attachShell`).
"""
import re

import pexpect
import pytest


PROMPT_RE = re.compile(rb"[$#] $")


@pytest.mark.contract
def test_tart_exec_dash_i_dash_t_keeps_bash_interactive(inspector_vm):
    """With -i -t, bash stays open until the caller exits it."""
    child = pexpect.spawn(
        "tart",
        ["exec", "-i", "-t", inspector_vm.name, "bash"],
        timeout=30,
        encoding="utf-8",
    )
    try:
        # Bash should print a prompt and wait for input.
        child.expect(PROMPT_RE.pattern.decode())
        child.sendline("echo HOLDS-OPEN")
        child.expect("HOLDS-OPEN")
        child.sendline("exit")
        child.expect(pexpect.EOF)
        assert child.exitstatus == 0, \
            f"bash exited non-zero: status={child.exitstatus}"
    finally:
        child.close(force=True)


@pytest.mark.contract
def test_tart_exec_without_dash_t_drops_pty(inspector_vm):
    """Without -t, bash sees no TTY and exits before showing a prompt.

    Pinned because relying on this behaviour silently for `devm shell`
    breaks the user's interactive session (the bug that motivated the pin).
    """
    child = pexpect.spawn(
        "tart",
        ["exec", "-i", inspector_vm.name, "bash"],
        timeout=15,
        encoding="utf-8",
    )
    try:
        # No PTY: bash should hit EOF without ever printing a prompt.
        # If a prompt DID appear (contract changed), the with-flag test
        # above stops being meaningful — fail loud here so we notice.
        idx = child.expect(
            [pexpect.EOF, PROMPT_RE.pattern.decode()],
            timeout=10,
        )
        assert idx == 0, (
            "tart exec WITHOUT -t showed a prompt — "
            "the no-PTY contract has changed; revisit devm's attachShell."
        )
    finally:
        child.close(force=True)
