"""203: SSH keeps working across teardown+start, even when the freed
project IP is squatted by a listener outside daemon state.

Run against the bootstrapped devm-e2e install via `just e2e`.

Pins the orphaned-VM cross-wiring failure: daemon state and running
VMs have independent lifetimes, so a pool IP the daemon considers free
can still be held by a live softnet (e.g. a VM whose state snapshot
was lost). Handing that IP to a new project cross-wires it — DNS
resolves the new project's name to the IP while :22 keeps answering
with the squatter's sshd (wrong host key, client key rejected) and the
new softnet's bind fails with a swallowed EADDRINUSE. The allocator
must probe :22 and skip such addresses.

  1. Cold-start a project; note its allocated project IP; SSH works.
  2. Teardown (frees the IP).
  3. Squat the freed IP's :22 via the e2e root helper (same bind path
     an orphaned softnet holds it through).
  4. Cold-start again: the allocator must skip the squatted IP and log
     the skip; SSH — strict host-key checking against the project's
     pinned known_hosts — must work.
"""
from __future__ import annotations

import array
import json
import socket
import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm

E2E_RUNTIME_DIR = Path.home() / "Library/Application Support/devm-e2e"
E2E_HELPER_SOCK = "/var/run/devm-e2e-helper.sock"
E2E_ERR_LOG = Path.home() / "Library/Logs/com.devm.e2e.service.err.log"


def _cold_start(devm, workspace) -> None:
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=300,
    )
    assert r.returncode == 0, (
        f"cold-start failed:\nstdout={r.stdout.decode()!r}\n"
        f"stderr={r.stderr.decode()!r}"
    )


def _project_ip(slug: str) -> str:
    snap = json.loads((E2E_RUNTIME_DIR / "state" / f"{slug}.json").read_text())
    ip = snap.get("project_ip", "")
    assert ip, f"state snapshot for {slug} has no project_ip: {snap}"
    return ip


def _assert_ssh_whoami(vm_name: str, deadline_s: float = 90.0) -> None:
    """SSH `whoami` with retries. ConnectTimeout bounds each attempt so
    a listener that accepts TCP but never sends an SSH banner (e.g. a
    stale-DNS-cached connection to the squatted IP — mDNSResponder can
    briefly serve the previous lifecycle's answer despite devm's TTL-0
    records) fails the attempt in seconds instead of hanging."""
    end = time.monotonic() + deadline_s
    last = None
    while time.monotonic() < end:
        r = subprocess.run(
            ["ssh", "-F", str(E2E_RUNTIME_DIR / "ssh_config"),
             "-o", "ConnectTimeout=5", f"devm-{vm_name}", "whoami"],
            capture_output=True, text=True, timeout=20,
        )
        if r.returncode == 0:
            assert r.stdout.strip() == "devm", (
                f"expected devm user, got {r.stdout.strip()!r}"
            )
            return
        last = r
        time.sleep(2)
    assert False, (
        f"ssh whoami failed for {deadline_s}s:\n"
        f"stdout={last.stdout!r}\nstderr={last.stderr!r}" if last else
        f"ssh whoami never attempted within {deadline_s}s"
    )


def _squat(ip: str, port: int) -> socket.socket:
    """Bind ip:port through the e2e root helper and hold the listening
    socket — the same way an orphaned softnet holds a pool IP. The
    kernel completes handshakes into the backlog, so a connect probe
    sees the address as live without this test ever accepting."""
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as uds:
        uds.connect(E2E_HELPER_SOCK)
        req = json.dumps({"op": "bind", "ip": ip, "port": port, "proto": "tcp"}) + "\n"
        uds.sendall(req.encode())
        msg, ancdata, _flags, _addr = uds.recvmsg(4096, socket.CMSG_SPACE(4))
        resp = json.loads(msg)
        assert resp.get("ok"), f"helper bind {ip}:{port} failed: {resp}"
        fds = array.array("i")
        for level, typ, data in ancdata:
            if level == socket.SOL_SOCKET and typ == socket.SCM_RIGHTS:
                fds.frombytes(data[: len(data) - (len(data) % fds.itemsize)])
        assert len(fds), "helper reply carried no FD"
    return socket.socket(fileno=fds[0])


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_ssh_after_teardown_start_skips_squatted_ip(devm, workspace):
    # --- First lifecycle: start, record IP, prove SSH, teardown ---
    _cold_start(devm, workspace)
    first_ip = _project_ip(workspace.slug)
    _assert_ssh_whoami(workspace.vm_name)

    r = subprocess.run(
        [devm.path, "teardown", "--yes"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=120,
    )
    assert r.returncode == 0, (
        f"teardown failed:\nstdout={r.stdout.decode()!r}\n"
        f"stderr={r.stderr.decode()!r}"
    )

    # --- Squat the freed IP's :22, then start again ---
    err_log_offset = E2E_ERR_LOG.stat().st_size if E2E_ERR_LOG.exists() else 0
    squatter = _squat(first_ip, 22)
    try:
        _cold_start(devm, workspace)

        second_ip = _project_ip(workspace.slug)
        assert second_ip != first_ip, (
            f"allocator handed out squatted IP {first_ip} — the project's "
            f"name now resolves to a foreign :22 listener"
        )

        # The project's IP just changed; drop any cached answer for the
        # old one so the ssh below resolves fresh.
        subprocess.run(["dscacheutil", "-flushcache"], capture_output=True, timeout=10)

        # Strict host-key checking against the project's pinned
        # known_hosts: success proves the right VM answers on :22.
        _assert_ssh_whoami(workspace.vm_name)

        # The skip must be loud in the daemon's error log.
        assert E2E_ERR_LOG.exists(), f"expected daemon error log at {E2E_ERR_LOG}"
        with E2E_ERR_LOG.open() as f:
            f.seek(err_log_offset)
            tail = f.read()
        assert f"pool IP {first_ip}:22 is held by a listener outside daemon state" in tail, (
            f"expected squat-skip error in daemon err log, got:\n{tail}"
        )
    finally:
        squatter.close()
