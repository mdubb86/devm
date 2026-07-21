"""28: declared service that exits non-zero makes devm shell exit
non-zero with a structured error.

Pin Task 3 from the e2e refresh: provisioner asserts each declared
service reaches `is-active` after enable+start; a service that ends
in `failed` triggers a structured error and a non-zero shell exit.
"""
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_failed_service_makes_devm_shell_exit_nonzero(workspace, devm):
    # Declare a service that exits non-zero on start. systemd marks
    # it `failed`; the provisioner's health check returns a structured
    # error; devm shell propagates exit non-zero.
    workspace.write_devmyaml()
    workspace.add_systemd_service(
        name="broken",
        exec=["/bin/sh", "-c", "echo intentional fail >&2; exit 1"],
        restart="no",
    )
    proc = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path),
        capture_output=True, timeout=180,
    )
    assert proc.returncode != 0, (
        f"devm shell should exit non-zero on service-fail; got rc=0\n"
        f"stderr={proc.stderr.decode()!r}"
    )
    err = proc.stderr.decode()
    assert "service broken failed" in err
