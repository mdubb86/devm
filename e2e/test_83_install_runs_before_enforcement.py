"""83: install: steps run BEFORE iron-proxy enforcement is applied.

Iron-proxy is meant to gate the workload (agents / services), not the
developer's provisioning phase. This test pins that invariant:

  - devm.yaml has network.allow: ["api.github.com"] — only that host is
    allowed under enforcement.
  - install: has a step that reaches a host NOT on the allow list
    (pypi.org — a real public HTTPS endpoint we don't otherwise gate).
  - Cold-start MUST succeed — proving install ran with open egress.

Then, after cold-start, the same curl call is repeated. It MUST FAIL —
proving enforcement kicked in AFTER install (before services). Both
halves together lock the sequencing: install runs open, workload runs
gated.

Devm dependency: the guest runs ONE composed provisioning script
(render.RenderProvisionScript / internal/provision.Provisioner) instead
of the old per-step provisioner. Its `::devm:stage:open::` stage flushes
the base image's boot-lock nftables ruleset for the whole open window —
packages/install:/docker/templates/startup: all run under open egress —
and only the LATER `::devm:stage:enforce::` stage applies the real
allowlist ruleset, before the `::devm:stage:services::` stage
starts/health-polls declared services. If somebody ever moves the
enforce stage's nft apply earlier than install:/startup:, this test
breaks loudly.
"""
from __future__ import annotations

import subprocess

import pytest


pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(240)
def test_install_runs_before_enforcement(workspace, devm):
    # network.allow ONLY contains api.github.com. If enforcement were
    # active during install, curl to pypi.org would return 502 and the
    # install step would fail with `curl -sf` exiting non-zero.
    workspace.write_devmyaml(
        install=[
            # Reach a real public host that is NOT on the allow list.
            # `curl -sf` exits non-zero on any HTTP >=400 (which iron-proxy
            # returns as 502 for non-allowlisted SNI) OR on TLS failure.
            "curl -sf -o /dev/null --max-time 15 https://pypi.org/simple/",
        ],
        services={"idle": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
        network={"allow": ["api.github.com"]},
    )

    try:
        # Cold-start MUST succeed — meaning install: reached pypi.org.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=300,
        )
        assert r.returncode == 0, (
            "cold-start should succeed with open-network install even when "
            "install target isn't on network.allow — meaning enforcement "
            "fires AFTER install, not before.\n"
            f"stdout: {r.stdout.decode()}\n"
            f"stderr: {r.stderr.decode()}"
        )

        # After cold-start, the SAME curl now runs against an enforced VM.
        # It MUST fail — pinning that enforcement kicked in post-install.
        r = subprocess.run(
            [devm.path, "shell", "--", "curl", "-sf", "-o", "/dev/null",
             "--max-time", "15", "https://pypi.org/simple/"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=30,
        )
        assert r.returncode != 0, (
            "post-cold-start curl to non-allowlisted host should be blocked "
            "by iron-proxy — but it succeeded, meaning enforcement never "
            "actually got applied.\n"
            f"stdout: {r.stdout.decode()}\n"
            f"stderr: {r.stderr.decode()}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=60,
        )
