"""266: filestash serves the workspace end-to-end: bundled binary +
systemd unit + reserved route + Mac-side TLS termination + JS render.
Server-side HTTP fetch would only see the shell HTML; the DOM assertion
after Playwright loads the page proves filestash's JS actually executes
and hydrates the file listing."""
from __future__ import annotations
import subprocess
import pytest

from helpers.playwright import open_page

pytestmark = pytest.mark.devm


@pytest.mark.timeout(240)
def test_filestash_serves_workspace(devm, workspace):
    workspace.write_devmyaml(no_repo=True)
    (workspace.path / "SENTINEL_FILE.txt").write_text("hello from e2e\n")
    try:
        r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                           capture_output=True, timeout=180)
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # Mac-side reserved route (Task 4). Playwright follows HTTPS
        # with default (ignore_https_errors=False) — devm's local CA
        # trust is what makes the page load without a warning.
        #
        # no_repo=True syncs the Mac workspace straight to the guest's
        # home directory (mutagen), not into a <vm_name> subdirectory,
        # so the seeded file lands at /home/devm/SENTINEL_FILE.txt.
        url = f"https://files.{workspace.vm_name}.e2e.test/files/local/home/devm/"
        with open_page(url) as page:
            # Filestash's passthrough middleware still renders its SPA
            # login shell first; the file listing only hydrates after
            # the one-click CONNECT button is clicked.
            connect = page.get_by_role("button", name="CONNECT")
            connect.wait_for(timeout=10000)
            connect.click()

            listing = page.locator("text=SENTINEL_FILE.txt")
            listing.wait_for(timeout=15000)
            assert listing.count() > 0, (
                f"filestash didn't render our seeded file in the workspace "
                f"listing at {url} after clicking CONNECT."
            )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
