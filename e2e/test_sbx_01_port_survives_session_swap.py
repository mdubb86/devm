"""sbx-01: published port survives session swap.

Pure-sbx test — does NOT touch devm. Verifies whether sbx ties a
published port mapping to the session that published it, or whether
ports are sandbox-scoped (the intuitive model).

Flow:
  1. Create + attach a sandbox via `sbx run shell <ws>` (session A).
  2. Publish a port (58801 -> 8080).
  3. Verify the mapping appears in `sbx ports --json`.
  4. Verify the mapping WORKS: start nc -l in the sandbox, connect from
     the host, confirm the payload arrives.
  5. Attach a second session: `sbx exec -it <name> bash` (session B).
  6. Kill session A (the original "anchor").
  7. Assert the sandbox stays running (session B holds it).
  8. Assert the port mapping is still in `sbx ports --json`.
  9. Assert the port still WORKS end-to-end through session B.

Interpretation:
  - PASS  -> ports are sandbox-scoped. devm's cold-start port flakiness
            is a devm orchestration bug we need to fix locally.
  - FAIL  -> sbx ties port lifetime to publishing session. devm's
            "publish after killRun, wait for settle" workaround is the
            best we can do without sbx-side changes.
"""
from __future__ import annotations
import re
import socket
import subprocess
import tempfile
import time

import pexpect
import pytest

from helpers import sbx


# The sbx `shell` agent uses an `agent@...` bash prompt under the hood.
PROMPT_RE = r"\$ ?\r?\n?$|agent@\S+:\S+\$ ?"


def _wait_exec_ready(name: str, timeout: float = 30.0) -> None:
    """Block until `sbx exec <name> true` succeeds — sbx is happy."""
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        r = subprocess.run(["sbx", "exec", name, "true"],
                           capture_output=True, timeout=5)
        if r.returncode == 0:
            return
        last = r.stderr.decode()
        time.sleep(0.5)
    pytest.fail(f"sandbox {name} never became exec-ready: {last!r}")


def _send_payload_and_check(host_port: int, in_port: int, name: str,
                            sender_pexpect, log_path: str, payload: str) -> None:
    """Start a socat TCP listener in the sandbox via `sender_pexpect`,
    connect from host to host_port, send payload, verify in log.

    Uses socat (present in the sbx shell-agent base image); nc is not.
    """
    sender_pexpect.sendline(f"rm -f {log_path}")
    sender_pexpect.expect(PROMPT_RE, timeout=10)
    # socat: -u = unidirectional, reuseaddr in case a prior bind lingers.
    sender_pexpect.sendline(
        f"(socat -u TCP-LISTEN:{in_port},reuseaddr OPEN:{log_path},creat,trunc) &"
    )
    sender_pexpect.expect(PROMPT_RE, timeout=10)
    time.sleep(0.75)  # let socat bind

    sock = socket.create_connection(("127.0.0.1", host_port), timeout=5)
    try:
        sock.sendall(payload.encode())
    finally:
        sock.close()
    time.sleep(0.5)

    out = subprocess.run(
        ["sbx", "exec", name, "cat", log_path],
        capture_output=True, timeout=5,
    ).stdout.decode()
    assert payload.rstrip() in out, (
        f"payload {payload!r} did not arrive in {log_path}: got {out!r}"
    )


@pytest.mark.timeout(180)
def test_published_port_survives_session_swap(sandbox_name):
    workspace = tempfile.mkdtemp(prefix="sbx-e2e-portswap-")
    host_port = 58801  # unique-ish to avoid colliding with parallel runs
    in_port = 8080

    # 1) Create + run sandbox via `sbx run shell <workspace>` (session A).
    anchor = pexpect.spawn(
        "sbx", ["run", "--name", sandbox_name, "shell", workspace],
        encoding="utf-8", timeout=120, dimensions=(40, 200),
    )
    try:
        anchor.expect(PROMPT_RE, timeout=120)

        # Make sure sbx is in a sane state.
        assert sbx.sandbox_state(sandbox_name) == "running"
        _wait_exec_ready(sandbox_name, timeout=30)

        # 2) Publish a port from outside.
        r = subprocess.run(
            ["sbx", "ports", sandbox_name, "--publish", f"{host_port}:{in_port}"],
            capture_output=True, timeout=10,
        )
        assert r.returncode == 0, (
            f"publish failed: rc={r.returncode} stderr={r.stderr.decode()!r}"
        )

        # 3) Confirm the mapping appears.
        sbx.wait_for_port_published(
            sandbox_name, host_port=host_port, sandbox_port=in_port, timeout=10,
        )

        # 4) End-to-end through session A.
        _send_payload_and_check(host_port, in_port, sandbox_name,
                                anchor, "/tmp/recv1.log", "hello-pre-swap\n")

        # 5) Attach session B (must outlive A).
        second = pexpect.spawn(
            "sbx", ["exec", "-it", sandbox_name, "bash"],
            encoding="utf-8", timeout=30, dimensions=(40, 200),
        )
        try:
            second.expect(PROMPT_RE, timeout=30)
        except Exception:
            second.close(force=True)
            raise

        try:
            # 6) Kill session A (the anchor).
            anchor.close(force=True)

            # 7) Short check: sandbox alive on session B, port still mapped.
            time.sleep(3)
            assert sbx.sandbox_state(sandbox_name) == "running", (
                "sandbox stopped after closing session A; session B did not register"
            )
            ports_short = sbx.ports(sandbox_name)
            assert any(p.get("host_port") == host_port for p in ports_short), (
                f"port {host_port} disappeared within 3s of session A close; "
                f"ports={ports_short}\n"
                f"==> immediate eviction"
            )
            _send_payload_and_check(host_port, in_port, sandbox_name,
                                    second, "/tmp/recv2.log", "hello-post-swap-3s\n")

            # 8) Longer check: does sbx eventually evict the port? devm's
            #    failure mode was visible-immediately-then-gone-within-35s.
            time.sleep(30)
            assert sbx.sandbox_state(sandbox_name) == "running", (
                "sandbox stopped during +30s wait"
            )
            ports_long = sbx.ports(sandbox_name)
            assert any(p.get("host_port") == host_port for p in ports_long), (
                f"port {host_port} disappeared within ~33s of session A close; "
                f"ports={ports_long}\n"
                f"==> delayed eviction (matches devm's observed failure window)"
            )
            _send_payload_and_check(host_port, in_port, sandbox_name,
                                    second, "/tmp/recv3.log", "hello-post-swap-33s\n")
        finally:
            second.close(force=True)
    finally:
        # If anchor wasn't already closed in step 6 (early failure), close it now.
        try:
            anchor.close(force=True)
        except Exception:
            pass
