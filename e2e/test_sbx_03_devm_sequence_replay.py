"""sbx-03: CLI replay of devm's EXACT cold-start sequence.

Pure-sbx test. sbx-01 and sbx-02 prove sbx itself is consistent — a
port published before a session swap survives. devm violates that
ordering (publishes AFTER killRun) and is flaky. This test asks:

  If we replay devm's exact sbx CLI sequence — including the small
  weirdnesses (no pty on the anchor, immediate post-kill sbx calls,
  WriteSnapshot exec) — does the port still disappear?

If YES: the bug is in the SEQUENCE itself (devm could be rewritten in
        any language and have the same bug). Fixing devm = reordering.
If NO:  the bug is in devm's Go mechanics (spawner, signal handling,
        timing precision). Fixing devm = digging into shell.go.

Sequence (mirrors orchestrator/shell.go RunShell cold-start path):
  1. `sbx run --kit <kit> --name <name> portprobe <ws>` via
     subprocess.Popen with pipes (NOT a pty) — mimics
     ExecSpawner{Interactive: false}.
  2. waitForRunning: poll `sbx ls` until state == "running".
  3. waitForExecReady: poll `sbx exec <name> true` until rc == 0.
  4. (PROBE_PUBLISH_PRE_HANDOFF would go here — we intentionally
      do NOT publish yet, matching the actual devm flow.)
  5. `sbx exec -it <name> bash` via pexpect.spawn (real pty) —
     mimics ExecSpawner{Interactive: true} with inherited host TTY.
  6. Sleep 500ms (WaitForPty default).
  7. SIGKILL the anchor (`runCmd.Kill()`), wait up to 3s for exit.
  8. Safety check: sandbox still running (user session holds it).
  9. ReconcilePortsWithRunner equivalent:
     a. `sbx ports <name> --json` (currentMappings)
     b. `sbx ports <name> --publish 58804:8080` (publishWithVerify
        first iter)
     c. Poll `sbx ports <name> --json` for visibility (3s budget)
  10. WriteSnapshot equivalent:
      `sbx exec <name> sh -c "mkdir -p ... && echo ...|base64 -d > .tmp && mv ..."`
  11. Wait +3s, check port. Wait +30s more (33s total), check port.
  12. End-to-end through user session.
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

# Same kit as sbx-02 for fair comparison.
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


def _publish_with_verify(name: str, host_port: int, in_port: int,
                         deadline_s: float = 30.0) -> bool:
    """Mirror orchestrator/ports.go publishWithVerify: publish, poll list
    for visibility, retry until deadline."""
    end = time.monotonic() + deadline_s
    while time.monotonic() < end:
        subprocess.run(
            ["sbx", "ports", name, "--publish", f"{host_port}:{in_port}"],
            capture_output=True, timeout=10,
        )
        # 3s visibility poll, 250ms cadence.
        verify_end = time.monotonic() + 3.0
        while time.monotonic() < verify_end:
            for p in sbx.ports(name):
                if p.get("host_port") == host_port:
                    return True
            time.sleep(0.25)
    return False


def _write_snapshot(name: str, content: str) -> None:
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
def test_devm_cli_sequence_replay(sandbox_name):
    workspace = tempfile.mkdtemp(prefix="sbx-e2e-devm-seq-ws-")
    kit_dir = _build_kit_dir()
    host_port = 58804
    in_port = 8080

    # 1) Anchor via subprocess.Popen with pipes (no pty).
    anchor = subprocess.Popen(
        ["sbx", "run", "--kit", kit_dir, "--name", sandbox_name,
         "portprobe", workspace],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    user_shell = None
    try:
        # 2 + 3) Wait for running + exec-ready.
        _wait_running(sandbox_name, timeout=60)
        _wait_exec_ready(sandbox_name, timeout=30)

        # 5) User shell via pexpect (real pty), Interactive=true equiv.
        user_shell = pexpect.spawn(
            "sbx", ["exec", "-it", sandbox_name, "bash"],
            encoding="utf-8", timeout=30, dimensions=(40, 200),
        )
        user_shell.expect(PROMPT_RE, timeout=30)

        # 6) Settle (devm's WaitForPty default).
        time.sleep(0.5)

        # 7) SIGKILL anchor, wait up to 3s.
        anchor.send_signal(signal.SIGKILL)
        try:
            anchor.wait(timeout=3)
        except subprocess.TimeoutExpired:
            pass

        # 8) Safety check.
        assert sbx.sandbox_state(sandbox_name) == "running", (
            "sandbox died on anchor kill (user session didn't hold it)"
        )

        # 9) ReconcilePortsWithRunner equivalent.
        ok = _publish_with_verify(sandbox_name, host_port, in_port,
                                  deadline_s=30.0)
        assert ok, "publishWithVerify equivalent never saw the mapping visible"

        # 10) WriteSnapshot equivalent.
        _write_snapshot(sandbox_name, "test: replay\n")

        # 11a) +3s: still there?
        time.sleep(3)
        ports_short = sbx.ports(sandbox_name)
        assert any(p.get("host_port") == host_port for p in ports_short), (
            f"port {host_port} disappeared within 3s of post-kill publish; "
            f"ports={ports_short}\n"
            f"==> the CLI sequence alone reproduces devm's flakiness"
        )

        # 11b) +30s: this is devm's actual failure window.
        time.sleep(30)
        ports_long = sbx.ports(sandbox_name)
        assert any(p.get("host_port") == host_port for p in ports_long), (
            f"port {host_port} disappeared within ~33s of post-kill publish; "
            f"ports={ports_long}\n"
            f"==> CLI replay matches devm's failure exactly. The bug lives "
            f"in the SEQUENCE, not in devm's Go mechanics."
        )

        # 12) End-to-end.
        _send_payload_and_check(host_port, in_port, sandbox_name,
                                user_shell, "/tmp/recv.log",
                                "hello-cli-replay\n")
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
