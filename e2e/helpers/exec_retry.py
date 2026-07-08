"""Retry helper for `devm exec` against the known Tart gRPC transport flake.

Tart's guest agent (which `devm exec` and `devm shell` shell out to via
`tart exec`) occasionally emits one of these errors on the response
handshake even when the command itself ran to completion:

    Error: internal error (13): transport: SendHeader called multiple times
    Error: unavailable (14): connection error: ...
    Error: transport: Transport became inactive

Both patterns are already caught by internal/sandbox/tart/tart.go's
ExecWithRetry in the Go layer, but `devm exec` shells out to `tart
exec` via UserSpawner and doesn't route through that helper — so a
transport hiccup surfaces to the caller as exit 1 despite the command
having done its work.

We handle this at the test layer rather than in devm's CLI because
retrying at the CLI risks double-execution of non-idempotent user
commands. Tests that use idempotent commands (`date -u +%s`, `sudo
date -s @...`, `curl`) are safe to retry.

Not a substitute for a real fix if this bug ever gets a proper repro
in upstream Tart — flag with `xfail` and revisit if you see this
firing on non-flake conditions.
"""
from __future__ import annotations

import subprocess
import time

# Errors that indicate a tart guest-agent transport flake (the command
# may or may not have run, but retry is safe for idempotent commands).
# Grouped with the equivalent list in internal/sandbox/tart/tart.go so
# a shift in upstream tart's error strings gets caught in both places.
_TRANSPORT_FLAKE_MARKERS = (
    "SendHeader called multiple times",
    "Transport became inactive",
    "unavailable (14)",
    "internal error (13)",
)


def devm_exec_with_retry(
    devm_path: str,
    argv: list[str],
    *,
    cwd: str,
    timeout: float = 30.0,
    retries: int = 2,
) -> subprocess.CompletedProcess:
    """Run `devm exec ARGV...` retrying on the tart transport flake.

    Returns the last CompletedProcess. The caller can still get a
    non-zero return code if the underlying command genuinely failed;
    only the transport flake pattern triggers retries.
    """
    last: subprocess.CompletedProcess | None = None
    for attempt in range(retries + 1):
        last = subprocess.run(
            [devm_path, "exec", *argv],
            cwd=cwd,
            capture_output=True,
            timeout=timeout,
        )
        if last.returncode == 0:
            return last
        stderr = last.stderr.decode(errors="replace")
        if not any(m in stderr for m in _TRANSPORT_FLAKE_MARKERS):
            return last
        # Transport flake — pause a beat and try again.
        if attempt < retries:
            time.sleep(0.5)
    assert last is not None  # loop always runs at least once
    return last
