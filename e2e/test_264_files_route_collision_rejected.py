"""264: shelfmates-style devm.yaml that declares hostname: files.<project>.<tld>
must fail loud on the first cold-start — that's Review Focus #1 in the
bundled-filestash-and-pop-rework spec.

The rejection is synchronous (cmd/devm's rejectReservedHostname,
called from runShellFlow before any cold-start work begins), not the
daemon-side serviceapi.Routes.Apply check alone — Apply's collision
check runs inside `devm start`'s best-effort background route-install
goroutine, whose error never reaches the CLI's exit code.
"""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_files_route_collision_rejected(devm, workspace):
    workspace.write_devmyaml(
        no_repo=True,
        services={
            "fileserver": {"port": 9999, "hostname": f"files.{workspace.vm_name}.e2e.test"},
        },
    )
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                        capture_output=True, timeout=60)
    try:
        assert r.returncode != 0, (
            f"expected cold-start to fail on the reserved-hostname collision; "
            f"got success (stdout={r.stdout!r} stderr={r.stderr!r})"
        )
        err = r.stderr.decode()
        assert f"files.{workspace.vm_name}.e2e.test" in err, (
            f"error must name the reserved hostname; got {err!r}"
        )
        assert "reserved" in err, f"error must say 'reserved'; got {err!r}"
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                        capture_output=True, timeout=60)
