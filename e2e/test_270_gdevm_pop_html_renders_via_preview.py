"""270: gdevm pop <sketch.html> opens the preview server URL in the
Mac browser, and the preview server actually serves the sketch's
referenced assets through three different URL path shapes:

  1. Relative, same-dir subtree (href="themes/sibling.css")
  2. Relative, one dir up      (href="../shared/parent.css")
  3. Absolute from guest root  (href="/home/devm/absolute.css")

Each stylesheet sets a distinctive computed-style property; the test
reads them with page.evaluate so any one broken path shape surfaces
as a specific assertion failure.

Also pins the daemon log line (`serviceapi: pop: opened <preview-URL>
(project <vm>, native=false)`) — the same shape test_267 asserts for
filestash — so a regression that routes HTML back to filestash fails
loud at the dispatch level, not just at render."""
from __future__ import annotations
import subprocess
import time
from pathlib import Path

import pytest

from helpers.playwright import open_page

pytestmark = pytest.mark.devm


def _tail_log(path: Path, start_offset: int) -> str:
    with path.open("rb") as f:
        f.seek(start_offset)
        return f.read().decode(errors="replace")


@pytest.mark.timeout(300)
def test_gdevm_pop_html_renders_via_preview(devm, workspace):
    workspace.write_devmyaml(no_repo=True)
    log_path = Path.home() / "Library" / "Logs" / "com.devm.e2e.service.out.log"
    assert log_path.exists(), f"daemon out log missing at {log_path}"

    try:
        r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                           capture_output=True, timeout=180)
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # Seed the sketch tree in-guest:
        #   /home/devm/sketch/index.html
        #   /home/devm/sketch/themes/sibling.css          (relative same-dir)
        #   /home/devm/shared/parent.css                   (relative up)
        #   /home/devm/absolute.css                        (absolute from fs root)
        seed = r"""
set -e
mkdir -p /home/devm/sketch/themes /home/devm/shared
cat > /home/devm/sketch/themes/sibling.css <<'CSS'
body { background-color: rgb(10, 10, 10); }
CSS
cat > /home/devm/shared/parent.css <<'CSS'
body { color: rgb(20, 20, 20); }
CSS
cat > /home/devm/absolute.css <<'CSS'
h1 { font-size: 42px; }
CSS
cat > /home/devm/sketch/index.html <<'HTML'
<!doctype html>
<html>
  <head>
    <meta charset="utf-8">
    <link rel="stylesheet" href="themes/sibling.css">
    <link rel="stylesheet" href="../shared/parent.css">
    <link rel="stylesheet" href="/home/devm/absolute.css">
  </head>
  <body>
    <h1 id="headline">preview test</h1>
  </body>
</html>
HTML
"""
        r = subprocess.run(
            [devm.path, "exec", "bash", "-c", seed],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"seed sketch failed: {r.stderr.decode()!r}"

        guest_file = "/home/devm/sketch/index.html"
        log_cursor = log_path.stat().st_size

        r = subprocess.run(
            [devm.path, "exec", "gdevm", "pop", guest_file],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"gdevm pop failed:\n"
            f"stdout={r.stdout.decode()!r}\nstderr={r.stderr.decode()!r}"
        )

        # Layer 1: CLI stdout IS the preview URL.
        expected_url = f"https://preview.{workspace.vm_name}.e2e.test{guest_file}"
        got_url = r.stdout.decode().strip()
        assert got_url == expected_url, (
            f"gdevm pop stdout did not carry the expected preview URL\n"
            f"want: {expected_url}\n"
            f"got:  {got_url}"
        )

        # Layer 2: daemon's own invocation record.
        time.sleep(0.5)  # log buffer flush
        new_log = _tail_log(log_path, log_cursor)
        want_line = f"serviceapi: pop: opened {expected_url} (project {workspace.vm_name}, native=false)"
        assert want_line in new_log, (
            f"daemon log missing pop invocation record.\n"
            f"want: {want_line}\n"
            f"got (new log tail):\n{new_log[-2000:]}"
        )

        # Layer 3: Playwright visits the URL and verifies all three
        # stylesheets loaded via computed styles.
        with open_page(expected_url) as page:
            page.wait_for_selector("h1#headline", timeout=15000)
            styles = page.evaluate("""() => {
                const body = document.body;
                const h1 = document.querySelector('h1');
                const bcs = getComputedStyle(body);
                const hcs = getComputedStyle(h1);
                return {
                    background: bcs.backgroundColor,
                    color: bcs.color,
                    fontSize: hcs.fontSize,
                };
            }""")
            assert styles["background"] == "rgb(10, 10, 10)", (
                f"relative same-dir CSS did not load — "
                f"background = {styles['background']!r}"
            )
            assert styles["color"] == "rgb(20, 20, 20)", (
                f"relative up-dir CSS did not load — color = {styles['color']!r}"
            )
            assert styles["fontSize"] == "42px", (
                f"absolute-from-root CSS did not load — "
                f"h1 font-size = {styles['fontSize']!r}"
            )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
