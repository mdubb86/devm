"""Contract pin: the devm-e2e-helper accepts a bind request over UDS
and returns a working listening FD via SCM_RIGHTS.

Requires the helper installed (run `just e2e-install` first) — it runs
as a root LaunchDaemon serving /var/run/devm-e2e-helper.sock (see
docs/superpowers/specs/2026-07-19-per-project-bind-isolation-design.md
and cmd/devm-helper/main.go). Skips cleanly if the helper's UDS is
absent.

Uses 127.42.0.21 (an in-pool address for the e2e identity —
cmd/devm-helper/main.go's validateIPInPool only accepts
127.42.0.21..40 when built with -X identity.Profile=e2e) with port 0
(OS-picked ephemeral port). Requesting port 0 means this test can't
collide with a live project's own listener on 127.42.0.21, even if one
is running concurrently on this host — SO_REUSEADDR + an OS-assigned
port make the two independent binds coexist.
"""
from __future__ import annotations

import json
import os
import socket
import struct

import pytest

SOCK_PATH = "/var/run/devm-e2e-helper.sock"


@pytest.mark.contract
def test_helper_binds_and_returns_fd():
    if not os.path.exists(SOCK_PATH):
        pytest.skip(f"{SOCK_PATH} absent; run `just e2e-install` first")

    uc = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    uc.settimeout(5.0)
    uc.connect(SOCK_PATH)
    try:
        # The helper reads requests with bufio.ReadBytes('\n') (see
        # handle() in cmd/devm-helper/main.go) — without the
        # trailing newline it blocks forever waiting for a delimiter.
        req = (
            json.dumps({"op": "bind", "ip": "127.42.0.21", "port": 0, "proto": "tcp"}).encode()
            + b"\n"
        )
        uc.send(req)

        # Receive with ancillary data — the bound FD rides SCM_RIGHTS
        # alongside the JSON status reply in the same write.
        fds_size = struct.calcsize("i")
        msg, ancdata, flags, addr = uc.recvmsg(4096, socket.CMSG_LEN(fds_size))
        resp = json.loads(msg)
        assert resp["ok"], f"helper error: {resp}"

        fd = None
        for cmsg_level, cmsg_type, cmsg_data in ancdata:
            if cmsg_level == socket.SOL_SOCKET and cmsg_type == socket.SCM_RIGHTS:
                fd = struct.unpack("i", cmsg_data[:fds_size])[0]
                break
        assert fd is not None, "no FD in SCM_RIGHTS"

        # Wrap the FD and confirm it's a real listener bound to the
        # requested IP, on an OS-picked (non-zero) port.
        ln = socket.socket(fileno=fd)
        try:
            bound_addr = ln.getsockname()
            assert bound_addr[0] == "127.42.0.21"
            assert bound_addr[1] > 0, "expected an OS-picked ephemeral port"
        finally:
            ln.close()
    finally:
        uc.close()
