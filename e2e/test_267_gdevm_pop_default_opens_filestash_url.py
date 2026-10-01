"""267: gdevm pop (default) reaches the Mac-side daemon via the softnet
gateway:81 hairpin and tells it to `open` the filestash URL for the
guest file. Mirrors what in-guest Claude does when it wants to show a
file to the human on the Mac.

Verified by tailing the daemon's out.log for the
"serviceapi: pop: opened <URL> (project <proj>, native=false)" line
the pop handler emits on a successful dispatch. Intercepting `open`
itself would require editing the daemon's PATH, so we assert on the
daemon's own invocation record instead. The side effect — a browser
tab opening on the filestash URL — is harmless under the e2e TLD since
filestash serves 200 OK for any guest path."""
from __future__ import annotations
import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


def _tail_log(path: Path, start_offset: int) -> str:
    with path.open("rb") as f:
        f.seek(start_offset)
        return f.read().decode(errors="replace")


@pytest.mark.timeout(240)
def test_gdevm_pop_default_opens_filestash_url(devm, workspace):
    workspace.write_devmyaml(no_repo=True)
    log_path = Path.home() / "Library" / "Logs" / "com.devm.e2e.service.out.log"
    assert log_path.exists(), f"daemon out log missing at {log_path}"

    try:
        r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                           capture_output=True, timeout=180)
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # Seed a guest-side sentinel the pop can address. no_repo means
        # the mutagen sync doesn't carry arbitrary Mac files over; drop
        # the file directly in-guest.
        guest_path = "/home/devm/pop-sentinel.html"
        r = subprocess.run(
            [devm.path, "exec", "bash", "-c", f"echo '<p>hi</p>' > {guest_path}"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0, f"seed sentinel failed: {r.stderr.decode()!r}"

        # Note the daemon log cursor BEFORE the pop so we only read new
        # lines emitted by this specific dispatch.
        log_cursor = log_path.stat().st_size

        # Guest-side gdevm pop. It should return 0 — the daemon's open
        # exit code propagates, and `open https://files.<proj>.e2e.test/...`
        # returns 0 even if no window actually renders (open is fire-and-
        # forget by default).
        r = subprocess.run(
            [devm.path, "exec", "gdevm", "pop", guest_path],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"gdevm pop failed:\nstdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )
        # The daemon writes the target it opened to the response body;
        # exec surfaces that on stdout. Assert the filestash URL is in
        # there as a tight correctness pin.
        expected_url = f"https://files.{workspace.vm_name}.e2e.test/files/local{guest_path}"
        assert expected_url in r.stdout.decode(), (
            f"gdevm pop stdout missing the filestash URL\n"
            f"want: {expected_url}\ngot: {r.stdout.decode()!r}"
        )

        # Give log flushing a beat before reading.
        time.sleep(0.5)
        new_log = _tail_log(log_path, log_cursor)
        want_line = f"serviceapi: pop: opened {expected_url} (project {workspace.vm_name}, native=false)"
        assert want_line in new_log, (
            f"daemon log missing pop invocation record.\n"
            f"want: {want_line}\n"
            f"got (new log tail):\n{new_log[-2000:]}"
        )

        # Hold off teardown long enough for the browser tab the daemon's
        # `open` call spawned to land its HTTPS request on the proxy
        # while the project's reserved files.* route still exists.
        # Without this the browser races teardown and the viewer sees a
        # "no route configured" error page even though the test passed.
        time.sleep(2.5)
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
