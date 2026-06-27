"""Pin: cirruslabs/debian template ships with systemd-resolved on :53.

This is what caused our dnsmasq-fails-to-start incident. Pinning it
means the per-project provisioner (which installs dnsmasq) MUST
either mask systemd-resolved or use a different port. If cirruslabs
ever ships a leaner image without systemd-resolved, the provisioner's
mask step becomes unnecessary — we want a test failure to tell us.
"""
import pytest


@pytest.mark.devm
def test_systemd_resolved_owns_port_53_in_template(inspector_vm):
    r = inspector_vm.exec("sudo", "ss", "-tlnp")
    assert r.ok, f"ss failed: {r.stderr!r}"
    # Look for any LISTEN socket on :53 with systemd-resolve as the owner.
    found = False
    for line in r.stdout.splitlines():
        if ":53 " in line and "systemd-resolve" in line:
            found = True
            break
    assert found, (
        "systemd-resolved is NOT listening on :53 in the template "
        "anymore — the per-project provisioner's mask step can be "
        "dropped. ss output:\n" + r.stdout
    )


@pytest.mark.devm
def test_systemd_resolved_service_is_enabled(inspector_vm):
    r = inspector_vm.exec("systemctl", "is-enabled", "systemd-resolved")
    # is-enabled returns 0 for "enabled", non-zero otherwise; the
    # state appears on stdout regardless.
    state = r.stdout.strip()
    assert state == "enabled", \
        f"systemd-resolved is no longer enabled by default: {state!r}"
