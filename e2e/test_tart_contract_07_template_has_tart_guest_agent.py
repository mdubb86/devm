"""Pin: cirruslabs/debian template has tart-guest-agent installed and running.

The guest agent is what makes `tart exec`, `tart ip`, and stdin/output
forwarding work. Without it, every other contract test fails. Pinning
its presence and exec-readiness sentinels the whole base.
"""
import pytest


@pytest.mark.contract
def test_tart_guest_agent_package_present(inspector_vm):
    r = inspector_vm.exec("dpkg-query", "-W", "-f", "${Version}",
                          "tart-guest-agent")
    assert r.ok, (
        f"tart-guest-agent not installed in cirruslabs/debian template "
        f"(stderr: {r.stderr!r})"
    )
    version = r.stdout.strip()
    # We pinned 0.10.0 in the Ship 4 spec. If cirruslabs bumps it
    # significantly, we want to know — a major-version bump may
    # change the wire protocol or capabilities.
    assert version.startswith("0.10."), \
        f"tart-guest-agent version drifted: {version!r} (expected 0.10.x)"


@pytest.mark.contract
def test_tart_guest_agent_service_active(inspector_vm):
    r = inspector_vm.exec("systemctl", "is-active", "tart-guest-agent")
    assert r.stdout.strip() == "active", \
        f"tart-guest-agent service not active: {r.stdout!r}"


@pytest.mark.contract
def test_tart_exec_round_trips_basic_command(inspector_vm):
    r = inspector_vm.exec("echo", "hello-tart")
    assert r.ok, f"echo failed: {r.stderr!r}"
    assert r.stdout.strip() == "hello-tart", \
        f"unexpected echo output: {r.stdout!r}"
