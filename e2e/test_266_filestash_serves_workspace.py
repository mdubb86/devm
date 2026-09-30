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
        url = f"https://files.{workspace.vm_name}.e2e.test/files/local/home/devm/{workspace.vm_name}/"
        with open_page(url) as page:
            content = page.content()
            assert "SENTINEL_FILE.txt" in content, (
                f"filestash didn't render our seeded file in the workspace "
                f"listing at {url}. Page HTML head: {content[:500]!r}"
            )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
