"""Pin: `tart exec NAME` runs as the `admin` user (uid 1000), which has NOPASSWD sudo.

The cirruslabs/debian template ships with two users: `admin` (uid
1000) and `debian` (uid 1001). `tart exec` lands as admin. Admin
has NOPASSWD sudo. The provisioner wraps its script in `sudo`
because of this. If tart ever starts exec'ing as root OR as a
different user, our user-rename step in provision-base.sh breaks.
"""
import pytest


@pytest.mark.contract
def test_tart_exec_runs_as_admin_uid_1000(inspector_vm):
    r = inspector_vm.exec("id", "-u")
    assert r.ok, f"id -u failed: {r.stderr!r}"
    uid = r.stdout.strip()
    assert uid != "0", "tart exec ran as root — provisioner sudo wrap is wrong"
    assert uid == "1000", (
        f"tart exec default user changed: uid={uid} "
        f"(was 1000 / `admin` when written). provision-base.sh's "
        f"user-rename step targets the wrong user."
    )

    r = inspector_vm.exec("id", "-un")
    assert r.stdout.strip() == "admin", \
        f"tart exec default username changed: {r.stdout.strip()!r}"


@pytest.mark.contract
def test_tart_exec_with_sudo_reaches_root(inspector_vm):
    r = inspector_vm.exec("sudo", "id", "-u")
    assert r.ok, f"sudo id -u failed: {r.stderr!r}"
    assert r.stdout.strip() == "0", \
        f"sudo did not reach root: uid={r.stdout.strip()!r}"
