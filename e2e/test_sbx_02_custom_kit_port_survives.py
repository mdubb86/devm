"""sbx-02: sbx-01 pattern, but with a custom kit (NOT a built-in agent).

Pure-sbx test. sbx-01 proves the publish-spawn-kill-survive pattern
works when sbx is given the built-in `shell` agent. Devm runs through
`sbx run --kit <dir> ... <agent>` — a *custom* kit. If sbx behaves
differently for kits than for built-in agents, that would explain the
devm flakiness even when devm follows the right ordering.

This test uses a HARDCODED minimal kit (shape extracted from
`internal/render/spec.go`):

  kit/
    spec.yaml      # schemaVersion 1, agent kind, sleep-infinity entrypoint

Flow mirrors sbx-01:
  1. `sbx run --kit <kit> --name <name> portprobe <workspace>` (session A).
  2. Wait for ready (poll `sbx exec ... true`).
  3. Publish a port from outside.
  4. Verify visible and working end-to-end.
  5. Attach `sbx exec -it <name> bash` (session B).
  6. Close session A.
  7. +3s and +30s checks: sandbox up, port visible, port works.

Interpretation:
  - PASS  -> sbx is consistent across built-in agents and custom kits.
            devm flakiness is in devm's Go orchestration (spawner mechanics,
            timing within RunShell, post-killRun activity).
  - FAIL  -> sbx treats custom kits differently. Either we're using the
            kit wrong, or sbx has a kit-specific bug.
"""
from __future__ import annotations
import os
import socket
import subprocess
import tempfile
import textwrap
import time

import pexpect
import pytest

from helpers import sbx


# Anchor uses sleep-infinity entrypoint — no shell. We don't expect a
# prompt from it; we poll exec readiness instead.
PROMPT_RE = r"\$ ?\r?\n?$|agent@\S+:\S+\$ ?"

KIT_SPEC = textwrap.dedent("""\
    # Minimal sbx kit for testing — sleep-infinity entrypoint mimics devm's.
    schemaVersion: "1"
    kind: agent
    name: portprobe
    displayName: port-probe
    description: pure-sbx port behavior probe
    agent:
      image: docker/sandbox-templates:shell
      entrypoint:
        run: ["sh", "-c", "exec sleep infinity </dev/null"]
    environment:
      variables:
        IS_PORT_PROBE: "1"
""")


def _build_kit_dir() -> str:
    """Materialise the kit into a tempdir; return the dir path."""
    d = tempfile.mkdtemp(prefix="sbx-e2e-kit-")
    with open(os.path.join(d, "spec.yaml"), "w") as f:
        f.write(KIT_SPEC)
    return d


def _wait_exec_ready(name: str, timeout: float = 60.0) -> None:
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


def _send_payload_and_check(host_port, in_port, name, sender_pexpect,
                            log_path, payload):
    sender_pexpect.sendline(f"rm -f {log_path}")
    sender_pexpect.expect(PROMPT_RE, timeout=10)
    sender_pexpect.sendline(
        f"(socat -u TCP-LISTEN:{in_port},reuseaddr OPEN:{log_path},creat,trunc) &"
    )
    sender_pexpect.expect(PROMPT_RE, timeout=10)
    time.sleep(0.75)
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


@pytest.mark.timeout(240)
def test_custom_kit_port_survives_session_swap(sandbox_name):
    workspace = tempfile.mkdtemp(prefix="sbx-e2e-customkit-ws-")
    kit_dir = _build_kit_dir()
    host_port = 58803
    in_port = 8080

    # 1) sbx run with custom kit. Anchor is sleep-infinity (no shell),
    #    so we don't pexpect a prompt — we poll readiness from the host.
    anchor = pexpect.spawn(
        "sbx", ["run", "--kit", kit_dir, "--name", sandbox_name,
                "portprobe", workspace],
        encoding="utf-8", timeout=120, dimensions=(40, 200),
    )
    try:
        # Wait for sbx ls to show running.
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            if sbx.sandbox_state(sandbox_name) == "running":
                break
            time.sleep(0.5)
        else:
            pytest.fail(f"custom-kit sandbox {sandbox_name} never ran")

        _wait_exec_ready(sandbox_name, timeout=30)

        # 3) Publish a port.
        r = subprocess.run(
            ["sbx", "ports", sandbox_name, "--publish", f"{host_port}:{in_port}"],
            capture_output=True, timeout=10,
        )
        assert r.returncode == 0, (
            f"publish failed: rc={r.returncode} stderr={r.stderr.decode()!r}"
        )
        sbx.wait_for_port_published(
            sandbox_name, host_port=host_port, sandbox_port=in_port, timeout=10,
        )

        # 4) End-to-end with a *separate* exec (anchor has no shell).
        #    We use the second pexpect session (session B) for this, since
        #    session A is just sleep-infinity.
        second = pexpect.spawn(
            "sbx", ["exec", "-it", sandbox_name, "bash"],
            encoding="utf-8", timeout=30, dimensions=(40, 200),
        )
        try:
            second.expect(PROMPT_RE, timeout=30)
            _send_payload_and_check(host_port, in_port, sandbox_name,
                                    second, "/tmp/recv1.log", "hello-pre-swap-kit\n")

            # 6) Close session A.
            anchor.close(force=True)

            # 7a) +3s: sandbox up, port up, e2e works.
            time.sleep(3)
            assert sbx.sandbox_state(sandbox_name) == "running", (
                "sandbox stopped after closing session A (custom kit)"
            )
            ports_short = sbx.ports(sandbox_name)
            assert any(p.get("host_port") == host_port for p in ports_short), (
                f"port {host_port} disappeared within 3s (custom kit); "
                f"ports={ports_short}"
            )
            _send_payload_and_check(host_port, in_port, sandbox_name,
                                    second, "/tmp/recv2.log", "hello-post-swap-kit-3s\n")

            # 7b) +30s: same as 7a but after a longer wait.
            time.sleep(30)
            assert sbx.sandbox_state(sandbox_name) == "running", (
                "sandbox stopped during +30s wait (custom kit)"
            )
            ports_long = sbx.ports(sandbox_name)
            assert any(p.get("host_port") == host_port for p in ports_long), (
                f"port {host_port} disappeared within ~33s (custom kit); "
                f"ports={ports_long}\n"
                f"==> custom kit triggers delayed eviction that built-in "
                f"shell agent does not"
            )
            _send_payload_and_check(host_port, in_port, sandbox_name,
                                    second, "/tmp/recv3.log", "hello-post-swap-kit-33s\n")
        finally:
            second.close(force=True)
    finally:
        try:
            anchor.close(force=True)
        except Exception:
            pass
