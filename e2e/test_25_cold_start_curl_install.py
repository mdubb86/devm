"""25: cold start where install: shells out to curl over the network produces a sandbox with the fetched artifact in place.

User declares an `install:` step that curls a small known-stable
URL into /tmp inside the sandbox, plus `network.allowed_domains`
covering the host. Cold-start, then assert the downloaded file
exists and has plausible content.

What this pins:
  - install: steps that hit the network during cold-start survive
    devm's spawn/orchestration shape.
  - The fetched file lands at the expected path inside the sandbox
    with non-zero size.
  - File content is non-empty AND contains expected substring
    ("Hello") — guards against silent partial-success where curl
    didn't trip set -e.

What it doesn't cover (tested elsewhere):
  - install failure surfacing loud (non-zero install exit):
    test_51_lifecycle_install_failure_surfaces_loud.
  - Install-phase network policy semantics: not pinned by a contract
    test in the post-iron-proxy world (iron-proxy enforces uniformly
    across phases; no longer a distinct "install phase unrestricted"
    invariant to pin).
  - Install-change recreate: test_14_install_change_recreate.
  - install: that pipes the curl output directly into a shell
    (true `curl|bash`, not curl-to-file): not yet pinned.
"""
import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_cold_start_with_curl_install(workspace, devm, tart_sandbox, sandbox_name):
    workspace.write_devmyaml(
        install=[
            # Tiny, stable: a single byte from a github-hosted file.
            # github.com is the canonical reliable host. Keep the URL
            # extremely short-lived so a network blip is the only thing
            # that can plausibly break this — not a flaky upstream.
            "curl -fsSL https://raw.githubusercontent.com/octocat/Hello-World/master/README > /tmp/devm-e2e-fetch.txt",
        ],
        # Need github in the allowed_domains for curl during install:.
        # (We're not certain whether the sandbox enforces this at install
        # time, but set it anyway so the test doesn't depend on that.)
        network={
            "allow": ["github.com", "raw.githubusercontent.com"],
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
