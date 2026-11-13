"""PINNED DESIGNED BEHAVIOR — see spec Review Focus.

A process in the guest with access to /run/devm/ssh-agent.sock CAN
request a signature over arbitrary bytes from Mac's keys. This is the
irreducible cost of exposing an agent, same as `ssh -A`.

If this test ever fails because someone "hardened" the bridge by
filtering SIGN_REQUEST bytes, REVERT the hardening — it would break
every real SSH login that routes through the bridge. Different
mitigations (user-side `ssh-add -h destination constraints`) are the
correct knob.

Method: open /run/devm/ssh-agent.sock inside the guest, speak
ssh-agent protocol (RFC-draft wire format: uint32 len + byte type +
payload; strings are uint32 len + bytes, big-endian), request a
signature over an attacker-chosen byte string using the first
identity the agent lists. The returned signature's content is not
validated — the FACT that a SIGN_RESPONSE comes back proves the
oracle exists.
"""
from __future__ import annotations

import pytest

from helpers import e2e_project, ssh_add_and_cleanup

pytestmark = pytest.mark.devm


_GUEST_SIGN_PROBE = r"""
import socket, struct, sys

s = socket.socket(socket.AF_UNIX)
s.connect("/run/devm/ssh-agent.sock")

def send(msg_type, payload=b""):
    body = bytes([msg_type]) + payload
    s.sendall(struct.pack(">I", len(body)) + body)

def recv_all(n):
    buf = b""
    while len(buf) < n:
        chunk = s.recv(n - len(buf))
        if not chunk:
            raise RuntimeError("short read")
        buf += chunk
    return buf

def recv_msg():
    n = struct.unpack(">I", recv_all(4))[0]
    body = recv_all(n)
    return body[0], body[1:]

def read_string(buf, off):
    n = struct.unpack(">I", buf[off:off+4])[0]
    return buf[off+4:off+4+n], off+4+n

# 1. REQUEST_IDENTITIES (11) -> IDENTITIES_ANSWER (12).
send(11)
mtype, payload = recv_msg()
assert mtype == 12, "want IDENTITIES_ANSWER, got type=%d" % mtype
nkeys = struct.unpack(">I", payload[:4])[0]
assert nkeys >= 1, "no identities (nkeys=%d)" % nkeys
key_blob, off = read_string(payload, 4)
_, off = read_string(payload, off)  # skip comment

# 2. SIGN_REQUEST (13) over attacker-chosen bytes -> SIGN_RESPONSE (14).
data = b"arbitrary bytes the guest should not be able to sign but can"
sign_payload = (
    struct.pack(">I", len(key_blob)) + key_blob
    + struct.pack(">I", len(data)) + data
    + struct.pack(">I", 0)  # flags
)
send(13, sign_payload)
mtype, payload = recv_msg()
assert mtype == 14, "want SIGN_RESPONSE, got type=%d payload=%r" % (mtype, payload[:40])
sig_blob, _ = read_string(payload, 0)
assert len(sig_blob) > 0, "empty signature blob"
print("SIGNATURE_OK", len(sig_blob))
"""


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_signing_oracle_pinned():
    with e2e_project("ssh-signing-oracle") as p:
        vm_alias = f"devm-{p.vm_name}"
        p.set_devm_yaml({"ssh": [vm_alias]})
        p.start()
        with ssh_add_and_cleanup(p.client_key_path):
            res = p.run_in_guest(["python3", "-c", _GUEST_SIGN_PROBE])
            assert res.stdout.startswith("SIGNATURE_OK "), (
                "signing oracle broken — see pin comment at top of file.\n"
                f"stdout={res.stdout!r} stderr={res.stderr!r}"
            )
