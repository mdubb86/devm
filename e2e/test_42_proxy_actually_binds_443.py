"""Pin: the daemon's reverse proxy actually binds :443.

The Ship 3/4 gap was that LaunchAgent socket activation returned
unbound file descriptors for :80/:443. Ship 4.2's LaunchDaemon
makes them genuinely bound. This test pins that.

Minimal pin: TCP-connect to 127.0.0.1:443. Don't TLS-negotiate, don't
issue HTTP — just verify a TCP listener is there. If the proxy
isn't bound the connect fails with ECONNREFUSED.
"""
import socket

import pytest


@pytest.mark.devm
def test_proxy_binds_443():
    """devm install must result in the daemon binding :443.

    Pure TCP connect — no sudo, no devm CLI. Relies on whatever install
    state the host already has. If the daemon isn't installed/running,
    this fails informatively.
    """
    try:
        s = socket.create_connection(("127.0.0.1", 443), timeout=5)
        s.close()
    except ConnectionRefusedError:
        pytest.fail("nothing listening on 127.0.0.1:443 — proxy not bound")
    except OSError as e:
        pytest.fail(f"unexpected error connecting to :443: {e}")


@pytest.mark.devm
def test_proxy_binds_80():
    """Same pin, port 80."""
    try:
        s = socket.create_connection(("127.0.0.1", 80), timeout=5)
        s.close()
    except ConnectionRefusedError:
        pytest.fail("nothing listening on 127.0.0.1:80 — proxy not bound")
    except OSError as e:
        pytest.fail(f"unexpected error connecting to :80: {e}")
