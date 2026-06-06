"""exec: sbx exec -it NAME bash gives a working interactive PTY shell.

A pexpect-driven sbx exec -it bash session should reach a bash
prompt, accept commands, echo their output, and exit cleanly.

Devm dependency: `devm shell`'s user-shell handoff is literally
`sbx exec -it NAME bash`. If this contract breaks, the most visible
devm command stops working.
"""
from __future__ import annotations

import re

import pexpect
import pytest

from helpers.contract import contract_sandbox, minimal_kit

pytestmark = pytest.mark.sbx_contract

# The shell base image gives an `agent@HOST:DIR$ ` prompt.
PROMPT_RE = re.compile(r"agent@\S+:\S+\$ ?")


@pytest.mark.timeout(90)
# pexpect.spawn calls os.forkpty() while contract_sandbox's anchor-drain
# thread is alive. Python 3.14 warns about potential deadlock; in
# practice the drain thread only does os.read on the anchor master, no
# lock-holding work, and the fork has been deadlock-free across this
# test's history. Suppress the noise without papering over a real risk.
@pytest.mark.filterwarnings("ignore:.*forkpty.*:DeprecationWarning")
def test_exec_dash_it_gives_interactive_bash(sandbox_name):
    with contract_sandbox(minimal_kit(), sandbox_name):
        child = pexpect.spawn(
            "sbx", ["exec", "-it", sandbox_name, "bash"],
            encoding="utf-8", timeout=30, dimensions=(40, 200),
        )
        try:
            child.expect(PROMPT_RE, timeout=30)
            child.sendline("echo CONTRACT_E2_OK")
            child.expect("CONTRACT_E2_OK", timeout=10)
            child.expect(PROMPT_RE, timeout=10)
            child.sendline("exit")
            child.expect(pexpect.EOF, timeout=10)
        finally:
            try:
                child.close(force=True)
            except Exception:
                pass
