"""266: filestash serves the workspace end-to-end: bundled binary +
systemd unit + reserved route + Mac-side TLS termination + JS render.
Server-side HTTP fetch would only see the shell HTML; the DOM assertion
after Playwright loads the page proves filestash's JS actually executes
and hydrates the file listing.

Also pins the baked identity-provider setup: the preset uses passthrough
with `strategy: "direct"`, so a cold-browser visit to `/` auto-authenticates
through filestash's self-posting form, no password prompt, no CONNECT
click — if an upstream filestash bump changes that behavior, the password
input appearing in the DOM will fail this test loud."""
from __future__ import annotations
import subprocess
import pytest

from helpers.playwright import open_page

pytestmark = pytest.mark.devm


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

        # Mac-side reserved route. Playwright follows HTTPS with default
        # (ignore_https_errors=False) — devm's local CA trust is what
        # makes the page load without a warning. The direct-strategy
        # preset auto-authenticates the viewer during initial load, so
        # the sentinel in /home/devm/ shows up in the first listing with
        # no interaction required.
        url = f"https://files.{workspace.vm_name}.e2e.test/"
        with open_page(url) as page:
            # First: fail loud if filestash shows ANY password prompt —
            # that means the direct-strategy preset regressed to the
            # one-password-per-session UX.
            assert page.locator('input[type="password"]').count() == 0, (
                "filestash showed a password prompt — direct-strategy "
                "preset regressed; see internal/scripts/embed/filestash-config.json."
            )

            listing = page.locator("text=SENTINEL_FILE.txt")
            listing.wait_for(timeout=15000)
            assert listing.count() > 0, (
                f"filestash didn't render our seeded file in the listing "
                f"at {url}."
            )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
