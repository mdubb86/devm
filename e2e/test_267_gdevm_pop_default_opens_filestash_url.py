"""267: gdevm pop (default) reaches the Mac-side daemon via the softnet
gateway:81 hairpin, has the daemon shell `open <filestash URL>` for the
guest file, AND that URL actually renders a working filestash view of
the file in a browser. End-to-end proof that in-guest Claude saying
"let me show the human this file" actually results in the human seeing
the file.

Three layers of assertion, each catching a different kind of break:

  1. The CLI exits 0 and surfaces the daemon's response body on stdout.
     The body carries the exact URL the daemon shelled `open` on, so
     this pins the URL the gdevm/daemon chain produced.
  2. The daemon's out.log carries the `serviceapi: pop: opened <URL>
     (project <proj>, native=false)` line the handler emits — proves
     the daemon's own open-dispatch fired on the right target.
  3. Playwright visits the URL the daemon produced. Asserts the
     filestash SPA rendered the parent directory's listing with the
     seeded sentinel file visible in it. That proves the baked
     direct-strategy preset + reserved files.* route + filestash
     bundle actually serve the URL the pop produced."""
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
def test_gdevm_pop_default_opens_filestash_url(devm, workspace):
    workspace.write_devmyaml(no_repo=True)
    log_path = Path.home() / "Library" / "Logs" / "com.devm.e2e.service.out.log"
    assert log_path.exists(), f"daemon out log missing at {log_path}"

    try:
        r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                           capture_output=True, timeout=180)
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # Seed a guest-side sentinel the pop addresses. no_repo means
        # the mutagen sync doesn't carry arbitrary Mac files over; drop
        # the file directly in-guest.
        sentinel_name = "pop-sentinel.html"
        guest_file = f"/home/devm/{sentinel_name}"
        r = subprocess.run(
            [devm.path, "exec", "bash", "-c", f"echo '<p>hi</p>' > {guest_file}"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0, f"seed sentinel failed: {r.stderr.decode()!r}"

        # Pop the PARENT directory so filestash renders a listing we
        # can scrape for the sentinel. (Pop'ing the file itself would
        # take us to filestash's single-file viewer, which still works
        # but is harder to assert against generically.) filepath.Clean
        # in the guest CLI drops the trailing slash before posting.
        guest_dir = "/home/devm"
        log_cursor = log_path.stat().st_size

        r = subprocess.run(
            [devm.path, "exec", "gdevm", "pop", guest_dir],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"gdevm pop failed:\nstdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )

        # Layer 1: CLI stdout == URL the daemon shelled `open` on.
        expected_url = f"https://files.{workspace.vm_name}.e2e.test/files{guest_dir}"
        got_url = r.stdout.decode().strip()
        assert got_url == expected_url, (
            f"gdevm pop stdout did not carry the expected filestash URL\n"
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

        # Layer 3: visit the URL in a real browser.
        #
        # First land at `/` so filestash's direct-strategy auto-POSTs
        # the login form and the session cookie is set; THEN navigate
        # to the pop URL and verify the sentinel renders. Filestash's
        # SPA tries to list the folder in parallel with session fetch,
        # so a direct deep-URL hit on a cookie-less browser 404s the
        # file list — in production the user's browser already has a
        # cookie from the project's first-ever filestash visit, so this
        # pre-warm matches real-world behavior. 30s wait on the file-
        # listing because the SPA's first ls after auth takes a beat.
        root_url = f"https://files.{workspace.vm_name}.e2e.test/"
        with open_page(root_url) as page:
            assert page.locator('input[type="password"]').count() == 0, (
                "filestash showed a password prompt — direct-strategy "
                "preset regressed; see internal/scripts/embed/filestash-config.json."
            )
            # Direct-strategy's auto-POST + redirect lands at /home/devm.
            page.wait_for_url(lambda u: "/files/home/devm" in u, timeout=15000)

            # Now navigate to the daemon-produced URL and prove the
            # listing actually contains the sentinel we seeded.
            page.goto(expected_url, wait_until="domcontentloaded", timeout=15000)
            listing = page.locator(f"text={sentinel_name}")
            try:
                listing.wait_for(timeout=30000)
            except Exception as e:
                raise AssertionError(
                    f"filestash didn't render the seeded sentinel at {expected_url}\n"
                    f"— the URL opened but the file isn't in the listing.\n"
                    f"final url: {page.url}\n"
                    f"title: {page.title()}\n"
                    f"body text first 500 chars: {page.inner_text('body')[:500]!r}"
                ) from e
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
