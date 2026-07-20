"""Contract pin: macOS `lo0` interface aliases + per-alias bind isolation.

The full per-project-IP design hinges on this working exactly the way we
assume — otherwise the whole model is dead in the water.

We validate:

1.  `sudo ifconfig lo0 alias 127.0.0.X up` succeeds and the address becomes
    bindable from an unprivileged user immediately.
2.  Two aliases (say 127.0.0.2 and 127.0.0.3) can each host their own
    listener on the SAME port simultaneously, and traffic sent to
    127.0.0.2:P reaches ONLY 127.0.0.2's listener (never bleeds to
    127.0.0.3's listener or vice versa).
3.  Removing an alias (`sudo ifconfig lo0 -alias 127.0.0.X`) makes the
    bind fail again with EADDRNOTAVAIL — proves cleanup actually undoes
    the setup, so uninstall paths can rely on it.
4.  Adding an alias that already exists is idempotent (does not error),
    so boot-time provisioning can run every boot.

Requires sudo. Runs it as `sudo -n` (non-interactive) — if cached
credentials aren't available, the test SKIPS with a clear instruction
line rather than prompting or failing hard. To exercise: run once with
`sudo -v` to prime credentials, then `just e2e-lo0-contract`.

If ANY of these assertions fails, we cannot proceed with the per-project
IP model — pivot to picked host ports instead.
"""
from __future__ import annotations

import socket
import subprocess
import threading

import pytest

# Test IPs in devm's chosen 127.42/16 signature block. Real product code
# will allocate from 127.42.0.1..127.42.0.20 (20-project pool). We use
# high-index addresses (240/241) inside that same second-octet space so
# a test running concurrently with a live devm-daemon on this host
# doesn't stomp on any project's live allocation. The 127.42.0.0/24
# range is essentially unclaimed by other software (Docker/Colima/
# OrbStack all route through their own virtual NICs; tutorial examples
# stay in 127.0.0.0/24).
TEST_IPS = ["127.42.0.240", "127.42.0.241"]
TEST_PORT = 54987  # arbitrary, ephemeral-range, unlikely to collide


def _sudo(args: list[str]) -> subprocess.CompletedProcess:
    """Run `sudo -n <args>`; return the CompletedProcess without check."""
    return subprocess.run(
        ["sudo", "-n"] + args,
        capture_output=True,
        text=True,
        timeout=10,
    )


def _add_alias(ip: str) -> subprocess.CompletedProcess:
    return _sudo(["/sbin/ifconfig", "lo0", "alias", ip, "up"])


def _remove_alias(ip: str) -> subprocess.CompletedProcess:
    return _sudo(["/sbin/ifconfig", "lo0", "-alias", ip])


@pytest.fixture(autouse=True)
def _cleanup_aliases():
    """Best-effort cleanup after every test: remove any test aliases we
    might have left behind, so a test failure doesn't leak state into the
    next test."""
    yield
    for ip in TEST_IPS:
        _remove_alias(ip)  # ignore result — best effort


@pytest.fixture
def sudo_available():
    """Skip if sudo -n can't run — the test itself will run many sudo
    commands and we don't want to fail spuriously mid-test."""
    p = _sudo(["/usr/bin/true"])
    if p.returncode != 0:
        pytest.skip(
            "sudo -n unavailable; prime credentials with `sudo -v` then re-run"
        )


def _listen_and_echo(sock: socket.socket, marker: bytes, done: threading.Event) -> None:
    """Accept one connection, send marker, close. Set `done` when finished."""
    try:
        conn, _ = sock.accept()
        try:
            conn.sendall(marker)
        finally:
            conn.close()
    finally:
        done.set()


@pytest.mark.contract
def test_lo0_alias_add_then_bind(sudo_available):
    """Section 1: `ifconfig lo0 alias 127.0.0.X up` succeeds and the
    address is immediately bindable."""
    ip = TEST_IPS[0]

    # Before: bind should fail (address doesn't exist yet).
    with socket.socket() as s:
        with pytest.raises(OSError) as excinfo:
            s.bind((ip, TEST_PORT))
        assert excinfo.value.errno == 49, (  # EADDRNOTAVAIL on macOS
            f"expected EADDRNOTAVAIL(49) before alias, got errno={excinfo.value.errno}"
        )

    # Add the alias.
    p = _add_alias(ip)
    assert p.returncode == 0, f"ifconfig alias add failed: {p.stderr}"

    # After: bind should succeed.
    with socket.socket() as s:
        s.bind((ip, TEST_PORT))  # will raise if fails; no assertion needed


@pytest.mark.contract
def test_lo0_two_aliases_same_port_isolated(sudo_available):
    """Section 2: Two aliases can each host a listener on the same port
    simultaneously, and traffic to alias A never bleeds to alias B."""
    for ip in TEST_IPS:
        p = _add_alias(ip)
        assert p.returncode == 0, f"alias {ip} add failed: {p.stderr}"

    # Bind a listener on each alias, same port.
    servers = {}
    dones = {}
    threads = []
    try:
        for ip in TEST_IPS:
            s = socket.socket()
            s.bind((ip, TEST_PORT))
            s.listen(1)
            servers[ip] = s
            dones[ip] = threading.Event()
            marker = ip.encode()  # each listener returns its own IP as its response
            t = threading.Thread(target=_listen_and_echo, args=(s, marker, dones[ip]))
            t.start()
            threads.append(t)

        # Connect to each alias in turn. Expect to receive the marker
        # matching that alias — NOT the other alias's marker.
        for ip in TEST_IPS:
            with socket.socket() as c:
                c.settimeout(2.0)
                c.connect((ip, TEST_PORT))
                got = c.recv(64)
                assert got == ip.encode(), (
                    f"traffic bled: connected to {ip}:{TEST_PORT}, "
                    f"expected marker {ip.encode()!r}, got {got!r}"
                )
    finally:
        for s in servers.values():
            s.close()
        for t in threads:
            t.join(timeout=1.0)


@pytest.mark.contract
def test_lo0_alias_remove_undoes_bindability(sudo_available):
    """Section 3: `ifconfig lo0 -alias 127.0.0.X` removes the address; a
    subsequent bind fails with EADDRNOTAVAIL again. Proves uninstall
    hygiene actually works."""
    ip = TEST_IPS[0]

    assert _add_alias(ip).returncode == 0
    with socket.socket() as s:
        s.bind((ip, TEST_PORT))  # sanity: bind works after add

    assert _remove_alias(ip).returncode == 0
    with socket.socket() as s:
        with pytest.raises(OSError) as excinfo:
            s.bind((ip, TEST_PORT))
        assert excinfo.value.errno == 49, (
            f"expected EADDRNOTAVAIL(49) after remove, got errno={excinfo.value.errno}"
        )


@pytest.mark.contract
def test_lo0_alias_add_is_idempotent(sudo_available):
    """Section 4: Adding an alias that already exists is a no-op success.
    Boot-time provisioning can safely re-run every boot."""
    ip = TEST_IPS[0]

    p1 = _add_alias(ip)
    assert p1.returncode == 0, f"first add failed: {p1.stderr}"

    p2 = _add_alias(ip)
    assert p2.returncode == 0, (
        f"second add (idempotency check) failed: {p2.stderr}"
    )

    # Bindability still works.
    with socket.socket() as s:
        s.bind((ip, TEST_PORT))
