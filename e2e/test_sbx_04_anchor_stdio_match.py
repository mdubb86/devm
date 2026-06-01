"""sbx-04: sbx-03 + anchor stdio matched to devm's exactly.

sbx-03 used `subprocess.PIPE` for the anchor's stdout/stderr (which
nothing ever reads). devm uses Go's nil → /dev/null for stdout and
inherits the parent's stderr. This variant matches devm: stdout to
DEVNULL, stderr inherited.

If sbx-04 flakes while sbx-03 passes, the anchor's unread PIPE buffers
were masking the bug in sbx-03 — i.e., devm's stdio handling is
implicated. If sbx-04 still passes, stdio is not the cause; move to
the next suspect (Go reaper goroutine, EnvArgs, lock release).
"""
from __future__ import annotations
import base64
import os
import signal
import socket
import subprocess
import tempfile
import textwrap
import time

import pexpect
import pytest

from helpers import sbx


PROMPT_RE = r"\$ ?\r?\n?$|agent@\S+:\S+\$ ?"

KIT_SPEC = textwrap.dedent("""\
    schemaVersion: "1"
    kind: agent
    name: portprobe
    displayName: port-probe
    description: pure-sbx port behavior probe
    agent:
      image: docker/sandbox-templates:shell
      persistence: persistent
      entrypoint:
        run: ["sh", "-c", "exec sleep infinity </dev/null"]
    environment:
      variables:
        IS_PORT_PROBE: "1"
""")


def _build_kit_dir() -> str:
    d = tempfile.mkdtemp(prefix="sbx-e2e-kit-")
    with open(os.path.join(d, "spec.yaml"), "w") as f:
        f.write(KIT_SPEC)
    return d


def _wait_running(name: str, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if sbx.sandbox_state(name) == "running":
            return
        time.sleep(0.25)
    pytest.fail(f"sandbox {name} never reached 'running' within {timeout}s")


def _wait_exec_ready(name: str, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        r = subprocess.run(["sbx", "exec", name, "true"],
                           capture_output=True, timeout=5)
        if r.returncode == 0:
            return
        last = r.stderr.decode()
        time.sleep(0.25)
    pytest.fail(f"sandbox {name} not exec-ready within {timeout}s: {last!r}")


def _publish_with_verify(name, host_port, in_port, deadline_s=30.0):
    end = time.monotonic() + deadline_s
    while time.monotonic() < end:
        subprocess.run(
            ["sbx", "ports", name, "--publish", f"{host_port}:{in_port}"],
            capture_output=True, timeout=10,
        )
        verify_end = time.monotonic() + 3.0
        while time.monotonic() < verify_end:
            for p in sbx.ports(name):
                if p.get("host_port") == host_port:
                    return True
            time.sleep(0.25)
    return False


def _write_snapshot(name, content):
    encoded = base64.b64encode(content.encode()).decode()
    cmd = (
        "mkdir -p /home/agent/.devm && "
        f"echo {encoded} | base64 -d > /home/agent/.devm/applied.yaml.tmp && "
        "mv /home/agent/.devm/applied.yaml.tmp /home/agent/.devm/applied.yaml"
    )
    subprocess.run(["sbx", "exec", name, "sh", "-c", cmd],
                   capture_output=True, timeout=10, check=True)


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
def test_devm_cli_sequence_anchor_stdio_match(sandbox_name):
    workspace = tempfile.mkdtemp(prefix="sbx-e2e-stdio-match-ws-")
    kit_dir = _build_kit_dir()
    host_port = 58805
    in_port = 8080

    # Anchor stdio matched to devm: stdout=/dev/null, stderr=inherited.
    anchor = subprocess.Popen(
        ["sbx", "run", "--kit", kit_dir, "--name", sandbox_name,
         "portprobe", workspace],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=None,
    )
    user_shell = None
    try:
        _wait_running(sandbox_name, timeout=60)
        _wait_exec_ready(sandbox_name, timeout=30)

        user_shell = pexpect.spawn(
            "sbx", ["exec", "-it", sandbox_name, "bash"],
            encoding="utf-8", timeout=30, dimensions=(40, 200),
        )
        user_shell.expect(PROMPT_RE, timeout=30)

        time.sleep(0.5)
        anchor.send_signal(signal.SIGKILL)
        try:
            anchor.wait(timeout=3)
        except subprocess.TimeoutExpired:
            pass

        assert sbx.sandbox_state(sandbox_name) == "running"

        ok = _publish_with_verify(sandbox_name, host_port, in_port, deadline_s=30.0)
        assert ok, "publishWithVerify equivalent never saw the mapping visible"
        _write_snapshot(sandbox_name, "test: stdio-match\n")

        time.sleep(3)
        ports_short = sbx.ports(sandbox_name)
        assert any(p.get("host_port") == host_port for p in ports_short), (
            f"port {host_port} disappeared within 3s (stdio-match); ports={ports_short}"
        )

        time.sleep(30)
        ports_long = sbx.ports(sandbox_name)
        assert any(p.get("host_port") == host_port for p in ports_long), (
            f"port {host_port} disappeared within ~33s (stdio-match); ports={ports_long}\n"
            f"==> anchor stdio handling was masking the bug in sbx-03"
        )

        _send_payload_and_check(host_port, in_port, sandbox_name,
                                user_shell, "/tmp/recv.log",
                                "hello-stdio-match\n")
    finally:
        if user_shell is not None:
            try:
                user_shell.close(force=True)
            except Exception:
                pass
        if anchor.poll() is None:
            anchor.send_signal(signal.SIGKILL)
            try:
                anchor.wait(timeout=3)
            except subprocess.TimeoutExpired:
                pass
