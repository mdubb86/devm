"""25: cold start with a curl|bash style install step.

Locks the install lifecycle when install: includes a remote-fetched
script — the pattern many real projects use (rustup, nvm, claude.ai/
install.sh, etc.). Until this test landed (2026-06-05) no e2e test
exercised an install step that does a network curl; all our install:
fixtures used local-only commands like `touch /home/agent/marker`.

That gap meant we couldn't tell whether install steps that do real
network work survive devm's cold-start orchestration timing (the
read-loop / waitForExecReady gating, ring-buffer pipe handling, etc.).
This test installs nothing destructive: it curls a tiny known-stable
URL and asserts the downloaded file landed.
"""
import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_cold_start_with_curl_install(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        install=[
            # Tiny, stable: a single byte from a github-hosted file.
            # github.com is the canonical reliable host. Keep the URL
            # extremely short-lived so a network blip is the only thing
            # that can plausibly break this — not a flaky upstream.
            "curl -fsSL https://raw.githubusercontent.com/octocat/Hello-World/master/README > /tmp/devm-e2e-fetch.txt",
        ],
        # Need github in the allowed_domains for curl during install:.
        # (We're not certain whether sbx enforces this at install time —
        # test_sbx_04_install_network_policy_pin says it doesn't — but
        # set it anyway so the test doesn't depend on that subtlety.)
        network={
            "allowed_domains": ["github.com", "raw.githubusercontent.com"],
        },
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)
        # The install: ran at sandbox create. The fetched file must
        # exist inside the sandbox.
        sh.run_check("test -s /tmp/devm-e2e-fetch.txt", expect_zero=True, timeout=15)
        # Sanity-check content shape so a totally empty file (silent
        # curl failure that didn't trip set -e) is caught.
        sh.run_check("grep -q Hello /tmp/devm-e2e-fetch.txt", expect_zero=True, timeout=10)
        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
