"""Bridge resilience: when Mac has no reachable SSH_AUTH_SOCK, guest
ssh fails cleanly, the daemon dedupe-logger emits exactly one line,
and the daemon itself keeps running.

Pins the "logs once per hour, no hot loop" guarantee from the
design's Review Focus — a hot log loop here would pass a crash-only
assertion.

The dedupe log goes to stdout (log.Print via
internal/serviceapi/hairpin_sni.go's serviceapiDedupLogger), which
launchd routes to ~/Library/Logs/com.devm.e2e.service.out.log (not
.err.log). Exact message is defined in
internal/serviceapi/sshagentbridge.go:43:
    "agent-bridge: SSH_AUTH_SOCK unresolvable project=<projectID>"
"""
from __future__ import annotations

import time
from pathlib import Path

import pytest

from helpers import e2e_project, local_sshd_target, mac_env_unset

pytestmark = pytest.mark.devm

_DAEMON_OUT_LOG = (
    Path.home() / "Library" / "Logs" / "com.devm.e2e.service.out.log"
)


def _log_size() -> int:
    """Current size of the daemon's stdout log, 0 if not yet present."""
    try:
        return _DAEMON_OUT_LOG.stat().st_size
    except FileNotFoundError:
        return 0


def _count_needle_in_delta(pre_size: int, needle: str) -> tuple[int, str]:
    """Count occurrences of `needle` in the slice of the daemon log
    written since `pre_size`. Returns (count, delta-text) so a
    failing assertion can print what did arrive."""
    if not _DAEMON_OUT_LOG.exists():
        return 0, ""
    with open(_DAEMON_OUT_LOG, "rb") as f:
        f.seek(pre_size)
        delta = f.read().decode("utf-8", errors="replace")
    return delta.count(needle), delta


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_mac_agent_missing():
    with e2e_project("ssh-no-agent") as p, local_sshd_target() as alias:
        p.set_devm_yaml({"ssh": [alias]})
        p.start()

        needle = f"agent-bridge: SSH_AUTH_SOCK unresolvable project={p.vm_name}"
        pre_size = _log_size()

        with mac_env_unset("SSH_AUTH_SOCK"):
            res = p.run_in_guest(
                ["ssh", "-o", "ConnectTimeout=5", alias, "true"],
                check=False,
            )
            assert res.returncode != 0, "ssh must fail when agent unreachable"

        # launchd may buffer stdout; poll briefly for the line to land.
        deadline = time.time() + 5.0
        count, delta = 0, ""
        while time.time() < deadline:
            count, delta = _count_needle_in_delta(pre_size, needle)
            if count >= 1:
                break
            time.sleep(0.25)
        assert count == 1, (
            f"dedupe-logger must emit exactly 1 line for a single "
            f"guest ssh (hot-loop guard). got {count} occurrences of "
            f"{needle!r} in {_DAEMON_OUT_LOG}. delta=\n{delta}"
        )

        # Daemon still responsive — a benign status call succeeds.
        res = p.mac_cmd(["/usr/local/bin/devm-e2e", "status"])
        assert res.returncode == 0, (
            f"daemon must not crash on SSH_AUTH_SOCK='': "
            f"stdout={res.stdout!r} stderr={res.stderr!r}"
        )
