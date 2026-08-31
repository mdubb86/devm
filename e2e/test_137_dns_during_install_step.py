"""137: DNS resolves during install-step provisioning.

Regression pin for the v0.9.14 → v0.9.15 fix. v0.9.14 shipped a
half-fix: it masked systemd-resolved and pointed /etc/resolv.conf at
127.0.0.1, but dnsmasq wasn't listening yet during the install-step
window — devm.target (which started dnsmasq) was the LAST step in
provision.go, after every user install step. Any install step that
did DNS (apt-get update, curl, …) hit NXDOMAIN. This broke real
projects hard — first-boot provisioning failed entirely.

The v0.9.15 fix: bake the dnsmasq drop-in into the base image and
enable dnsmasq at boot, so DNS is ready before install steps run.

test_136 only covered post-provisioning DNS (via `devm shell` after
cold-start finished). That's why it passed on v0.9.14 while real
projects couldn't cold-start. This test closes that gap.

The assertion is simple: an install step that does `getent hosts
anything.test` must succeed. If dnsmasq isn't up during install-step
execution, getent returns non-zero, the install pipeline fails,
`devm shell` fails, the test fails.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_dns_works_during_install_step(devm, workspace, sandbox_name):
    # Install step exercises DNS. Uses a `.test` name so no external
    # egress is required — dnsmasq's local answer suffices. If dnsmasq
    # isn't listening on 127.0.0.1:53 at install-step time, getent
    # returns non-zero and the whole install pipeline fails.
    workspace.write_devmyaml(
        install=["getent hosts anything.test"],
    )

    try:
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, (
            f"cold-start failed (rc={r.returncode}):\n"
            f"stderr: {r.stderr.decode()}\n\n"
            f"If the install step failed with NXDOMAIN, dnsmasq wasn't "
            f"running yet when the install step ran — the v0.9.14 bug. "
            f"Check image/provision-base.sh enables dnsmasq at boot and "
            f"bakes the drop-in (dnsmasq-devm-test.conf)."
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
