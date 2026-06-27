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
@pytest.mark.slow
def test_proxy_binds_443(devm, sudo_capable):
    """devm install must result in the daemon binding :443."""
    # The sudo_capable fixture + install assumption matches test_41's
    # shape. We don't re-install here; we rely on whatever install
    # state test_41 (or a manual install) produced.
    try:
        s = socket.create_connection(("127.0.0.1", 443), timeout=5)
        s.close()
    except ConnectionRefusedError:
        pytest.fail("nothing listening on 127.0.0.1:443 — proxy not bound")
    except OSError as e:
        pytest.fail(f"unexpected error connecting to :443: {e}")


@pytest.mark.devm
@pytest.mark.slow
def test_proxy_binds_80(devm, sudo_capable):
    """Same pin, port 80."""
    try:
        s = socket.create_connection(("127.0.0.1", 80), timeout=5)
        s.close()
    except ConnectionRefusedError:
        pytest.fail("nothing listening on 127.0.0.1:80 — proxy not bound")
    except OSError as e:
        pytest.fail(f"unexpected error connecting to :80: {e}")
