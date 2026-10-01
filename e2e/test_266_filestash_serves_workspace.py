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


# Filestash with the pinned preset ("Local files - just for me") shows a
# one-field password prompt on first visit; the admin password is the
# project name — bcrypt-hashed per-project at bundle-render time by
# render.RenderInstallScript. After entering, filestash navigates to the
# file listing and remembers the session via cookie for subsequent
# requests.
@pytest.mark.timeout(240)
def test_filestash_serves_workspace(devm, workspace):
    workspace.write_devmyaml(no_repo=True)
    try:
        r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                           capture_output=True, timeout=180)
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # Seed the sentinel INSIDE the guest. A no_repo project has no
        # Mac→guest mutagen sync of arbitrary workspace files (devm.yaml
        # is the only auto-synced bit); writing SENTINEL on the Mac
        # would never show up at /home/devm/. `devm exec` with a shell
        # fragment is the simplest way to drop a file at a known path.
        r = subprocess.run(
            [devm.path, "exec", "bash", "-c", "echo 'hello from e2e' > /home/devm/SENTINEL_FILE.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0, f"seed sentinel failed: {r.stderr.decode()!r}"

        # Mac-side reserved route (Task 4). Playwright follows HTTPS
        # with default (ignore_https_errors=False) — devm's local CA
        # trust is what makes the page load without a warning.
        # Visit filestash's root; the preset's SPA shows a password
        # prompt first (admin password "devm"), then the local backend
        # defaults to /home/devm so the seeded sentinel is in the first
        # listing the SPA renders after login — no second navigation
        # needed.
        url = f"https://files.{workspace.vm_name}.e2e.test/"
        with open_page(url) as page:
            pw_input = page.locator('input[type="password"]')
            pw_input.wait_for(timeout=10000)
            pw_input.fill(workspace.vm_name)
            page.get_by_role("button", name="CONNECT").click()

            listing = page.locator("text=SENTINEL_FILE.txt")
            listing.wait_for(timeout=15000)
            assert listing.count() > 0, (
                f"filestash didn't render our seeded file in the listing "
                f"at {url} after login."
            )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
