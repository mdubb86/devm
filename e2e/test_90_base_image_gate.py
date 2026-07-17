"""90: the base-image gate floor — a daemon-less boot is inert +
egress-locked.

Boots a raw `devm-base` clone (NO daemon, NO provisioning) and asserts
the pre-daemon floor the boot-integrity gate depends on: egress is
default-drop, devm.target is inactive, and ssh/caddy/dnsmasq are not
running.
"""
from __future__ import annotations
import subprocess, pytest
pytestmark = pytest.mark.devm

@pytest.mark.slow
@pytest.mark.timeout(300)
def test_daemon_less_boot_is_locked_and_inert(base_clone):
    vm = base_clone  # fixture: clones devm-base, `tart run`s it, yields name, cleans up
    def x(*cmd): return subprocess.run(["tart","exec",vm,*cmd],capture_output=True,text=True)

    # devm.target inactive (nothing user-facing was pulled in)
    assert x("systemctl","is-active","devm.target").stdout.strip() != "active"
    # ssh / caddy / dnsmasq NOT running
    for unit in ("ssh","caddy","dnsmasq"):
        assert x("systemctl","is-active",unit).stdout.strip() != "active", f"{unit} should be down"
    # egress locked: a curl to a public host fails (default-drop)
    r = x("curl","-sS","-m","5","https://example.com")
    assert r.returncode != 0, "egress should be locked on a daemon-less boot"
    # nftables policy is drop on output
    assert "policy drop" in x("sudo","nft","list","chain","inet","devm_filter","output").stdout
