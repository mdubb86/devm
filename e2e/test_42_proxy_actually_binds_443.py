"""Pin: the daemon's reverse proxy actually binds :443.

The Ship 3/4 gap was that LaunchAgent socket activation returned
unbound file descriptors for :80/:443. Ship 4.2's LaunchDaemon
makes them genuinely bound. This test pins that.

Minimal pin: TCP-connect to 127.0.0.1:443. Don't TLS-negotiate, don't
issue HTTP — just verify a TCP listener is there. If the proxy
isn't bound the connect fails with ECONNREFUSED.

Depends on the ambient install: skips (rather than fails) if the
LaunchDaemon plist isn't present, so `test_41`'s uninstall doesn't
poison the suite for later runs.
"""
import socket
from pathlib import Path

import pytest


_LAUNCH_DAEMON_PLIST = Path("/Library/LaunchDaemons/com.devm.service.plist")


def _require_devm_installed():
    if not _LAUNCH_DAEMON_PLIST.exists():
        pytest.skip(
            "devm is not installed on this Mac (no LaunchDaemon plist). "
            "Run `devm install` before rerunning this test — the port-bind "
            "pin is meaningful only against a live install."
        )


@pytest.mark.devm
def test_proxy_binds_443():
    _require_devm_installed()
    try:
        s = socket.create_connection(("127.0.0.1", 443), timeout=5)
        s.close()
    except ConnectionRefusedError:
        pytest.fail("nothing listening on 127.0.0.1:443 — proxy not bound")
    except OSError as e:
        pytest.fail(f"unexpected error connecting to :443: {e}")


@pytest.mark.devm
def test_proxy_binds_80():
    _require_devm_installed()
    try:
        s = socket.create_connection(("127.0.0.1", 80), timeout=5)
        s.close()
    except ConnectionRefusedError:
        pytest.fail("nothing listening on 127.0.0.1:80 — proxy not bound")
    except OSError as e:
        pytest.fail(f"unexpected error connecting to :80: {e}")
