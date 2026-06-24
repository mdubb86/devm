"""37: devm route vm registers Caddy routes that actually proxy traffic.

Requires brew-installed Caddy running on the host. If Caddy isn't
reachable on http://localhost:2019, the test SKIPs with a clear
message — this isn't a fundamental test failure, it's an environment
gap.

What this pins:
  - `devm route vm` exits 0.
  - A devm-owned Caddy route exists for each service hostname after
    the command runs.
  - `devm route down` removes the routes; subsequent inspection shows
    no devm-owned route present.

What it doesn't cover (tested elsewhere):
  - The actual HTTP proxy round-trip (would require a running
    sandbox to back the dial port). Caddy's admin API confirms the
    routes are configured; that's the contract devm route owns.
  - localias resolver — opt-in; default snippet path is what runs here.
"""
from __future__ import annotations

import subprocess
import urllib.error
import urllib.request

import pytest

pytestmark = pytest.mark.devm

CADDY_ADMIN = "http://localhost:2019"


def _caddy_running() -> bool:
    try:
        with urllib.request.urlopen(f"{CADDY_ADMIN}/config/", timeout=2) as r:
            return r.status < 500
    except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError):
        return False


def _caddy_route_present(project_id: str, hostname: str) -> bool:
    """Returns True if Caddy's admin API has a devm.<project>.route.<hostname> @id."""
    url = f"{CADDY_ADMIN}/id/devm.{project_id}.route.{hostname}"
    try:
        with urllib.request.urlopen(url, timeout=2) as r:
            return r.status < 300
    except urllib.error.HTTPError as e:
        return e.code < 300
    except Exception:
        return False


@pytest.mark.timeout(60)
def test_route_vm_registers_and_removes(workspace, devm, sandbox_name):
    if not _caddy_running():
        pytest.skip(f"Caddy not reachable at {CADDY_ADMIN} — brew install + start caddy to run this test")

    hostname = f"{sandbox_name}-route.local"
    workspace.write_devmyaml(
        services={
            "web": {"port": 8080, "hostname": hostname},
        },
    )

    project_id = workspace.slug

    # Defensive cleanup: in case a prior aborted run left a route.
    subprocess.run(
        [devm.path, "route", "down"],
        cwd=str(workspace.path),
        capture_output=True, timeout=15, check=False,
    )

    try:
        r = subprocess.run(
            [devm.path, "route", "vm"],
            cwd=str(workspace.path),
            capture_output=True, timeout=30, check=False,
        )
        assert r.returncode == 0, (
            f"`devm route vm` exit {r.returncode}\nstdout: {r.stdout.decode()!r}\nstderr: {r.stderr.decode()!r}"
        )

        # The route should now exist in Caddy.
        assert _caddy_route_present(project_id, hostname), (
            f"devm route vm exited 0 but Caddy has no @id devm.{project_id}.route.{hostname}"
        )

        # Tear it down.
        r = subprocess.run(
            [devm.path, "route", "down"],
            cwd=str(workspace.path),
            capture_output=True, timeout=15, check=False,
        )
        assert r.returncode == 0, (
            f"`devm route down` exit {r.returncode}\nstderr: {r.stderr.decode()!r}"
        )

        # Route should be gone.
        assert not _caddy_route_present(project_id, hostname), (
            "devm route down exited 0 but the route is still present in Caddy"
        )
    finally:
        # Always clean up the route in case of mid-test failure.
        subprocess.run(
            [devm.path, "route", "down"],
            cwd=str(workspace.path),
            capture_output=True, timeout=15, check=False,
        )
