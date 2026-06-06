"""ports: published port is reachable from host (TCP round-trip).

It's not enough for sbx to LIST the port in --json — it must actually
route a TCP connection. Inside the sandbox, start a python listener
on port 8080 writing payload to /tmp/p3-marker. Publish
127.0.0.1:55201:8080. From the host, open a socket to 127.0.0.1:55201
and send a payload. Read the marker via sbx exec — assert payload
arrived.

Devm dependency: devm.yaml's services map to host ports via this
exact round-trip — host_port = port_offset + sandbox_port reachable
on 127.0.0.1.
"""
from __future__ import annotations

import socket
import subprocess
import time

import pytest

from helpers import sbx
from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract

LISTENER_SCRIPT = (
    "import socket;"
    "s=socket.socket();"
    "s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);"
    "s.bind(('0.0.0.0',8080));"
    "s.listen(1);"
    "c,_=s.accept();"
    "open('/tmp/p3-marker','wb').write(c.recv(4096));"
    "c.close()"
)


@pytest.mark.timeout(120)
def test_published_port_routes_tcp_to_sandbox(sandbox_name):
    HOST_PORT = 55201
    SANDBOX_PORT = 8080
    PAYLOAD = "contract-P3-payload\n"

    with contract_sandbox(minimal_kit(), sandbox_name):
        sbx_exec(sandbox_name, "rm", "-f", "/tmp/p3-marker")

        # Start a backgrounded python listener inside the sandbox via nohup.
        # sbx exec is synchronous; use sh -c with & + disown-ish redirect to
        # let it return immediately while the listener keeps running.
        spawn = sbx_exec(
            sandbox_name, "sh", "-c",
            f"nohup python3 -c \"{LISTENER_SCRIPT}\" "
            f">/tmp/p3-listener.log 2>&1 </dev/null & echo $!",
        )
        assert spawn.returncode == 0, f"spawn listener failed: {spawn.stderr.decode()}"
        time.sleep(1)  # let python bind

        r = subprocess.run(
            ["sbx", "ports", sandbox_name, "--publish",
             f"127.0.0.1:{HOST_PORT}:{SANDBOX_PORT}"],
            capture_output=True, timeout=15,
        )
        assert r.returncode == 0, f"publish failed: {r.stderr.decode()}"
        sbx.wait_for_port_published(
            sandbox_name, host_port=HOST_PORT, sandbox_port=SANDBOX_PORT, timeout=10,
        )

        s = socket.create_connection(("127.0.0.1", HOST_PORT), timeout=10)
        try:
            s.sendall(PAYLOAD.encode())
        finally:
            s.close()
        time.sleep(1)  # let listener flush + exit

        r = sbx_exec(sandbox_name, "cat", "/tmp/p3-marker")
        assert r.returncode == 0, (
            f"marker missing — listener log: "
            f"{sbx_exec(sandbox_name, 'cat', '/tmp/p3-listener.log').stdout.decode()}"
        )
        got = r.stdout.decode()
        assert PAYLOAD.strip() in got, (
            f"payload didn't arrive; sent {PAYLOAD!r}, got {got!r}"
        )
