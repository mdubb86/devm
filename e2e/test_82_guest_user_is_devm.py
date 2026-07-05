"""82: the guest user in every devm project VM is `devm` (uid 1000),
sudoers is preserved, and no transient rename machinery is left behind.

Pins the admin -> devm rename that fires at devm-base build time (via
the `devm-rename-user.service` systemd one-shot ordered
Before=tart-guest-agent). Cirruslabs's stock Debian image ships a
user `admin` (uid 1000) — devm's base-image builder renames it to
`devm` (same uid) so users see a devm-flavored home dir + sudo works
under the new login name.

What this pins:
  - `id -un` inside any project VM returns `devm`.
  - `id -u` returns `1000` (uid stayed put — this is the tart-guest-agent
    contract, not a devm choice).
  - `id -gn` returns `devm` (the primary group renamed too, so `stat
    -c '%U %G' /home/devm` shows devm:devm).
  - `sudo -n whoami` returns `root` (sudoers rewrite kept NOPASSWD).
  - The transient rename machinery is gone from the image: no
    `/usr/local/bin/devm-rename-user`, no
    `/etc/systemd/system/devm-rename-user.service`, no marker at
    `/var/lib/devm/user-renamed`. The image ships already-renamed.

What it doesn't cover:
  - The raw cirruslabs guest still being `admin` — pinned by
    test_tart_contract_04, which clones cirruslabs directly rather
    than devm-base.
  - Rename firing on first boot of a clone (the current design bakes
    the rename into devm-base itself; the one-shot never fires again).
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)  # first-time devm-base build via the autouse fixture is ~5 min
def test_guest_identity_is_devm(workspace, devm, sandbox_name):
    workspace.write_devmyaml()

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    tart_sandbox = TartSandbox(name=sandbox_name)

    # Identity: name is `devm`, uid stayed 1000, primary group is `devm`.
    r = tart_sandbox.exec_shell("id -un && id -u && id -gn")
    assert r.ok, f"id failed: {r.stderr}"
    lines = r.stdout.strip().splitlines()
    assert lines == ["devm", "1000", "devm"], (
        f"unexpected identity — expected ['devm', '1000', 'devm'], got {lines!r}"
    )

    # Sudoers survived the rename — NOPASSWD still works.
    r = tart_sandbox.exec_shell("sudo -n whoami")
    assert r.ok, f"sudo failed post-rename: {r.stderr}"
    assert r.stdout.strip() == "root", (
        f"sudo -n whoami returned {r.stdout.strip()!r}, expected 'root'. "
        "sudoers.d rewrite of admin -> devm regressed."
    )

    # Home dir was renamed AND chowned to devm:devm.
    r = tart_sandbox.exec_shell(r"stat -c '%U %G' /home/devm")
    assert r.ok, f"stat /home/devm failed: {r.stderr}"
    assert r.stdout.strip() == "devm devm", (
        f"/home/devm owner is {r.stdout.strip()!r}, expected 'devm devm'"
    )

    # /home/admin must NOT exist — usermod -d ... -m moved it.
    r = tart_sandbox.exec_shell("test -e /home/admin && echo present || echo absent")
    assert r.ok
    assert r.stdout.strip() == "absent", (
        "/home/admin still exists — usermod -d /home/devm -m did not move the home dir"
    )

    # Transient rename machinery is gone from the built image. The
    # one-shot unit + script + marker are used during devm-base build
    # and cleaned up before poweroff (see internal/image/builder.go's
    # cleanupScript). A project VM cloned from devm-base must not have
    # any of them.
    for path in (
        "/usr/local/bin/devm-rename-user",
        "/etc/systemd/system/devm-rename-user.service",
        "/etc/systemd/system/multi-user.target.wants/devm-rename-user.service",
        "/var/lib/devm/user-renamed",
    ):
        r = tart_sandbox.exec_shell(f"test -e {path} && echo present || echo absent")
        assert r.ok
        assert r.stdout.strip() == "absent", (
            f"{path} still exists inside a project VM — the build-time cleanup "
            "step (internal/image/builder.go cleanupScript) regressed."
        )

    # tart-guest-agent's User= is `devm` — pinned by every other test
    # that reaches the VM via `tart exec` (they only work if the agent
    # is running under the renamed user), but we assert it directly
    # here so the failure mode is obvious.
    r = tart_sandbox.exec_shell(
        "grep '^User=' /etc/systemd/system/tart-guest-agent.service "
        "/usr/lib/systemd/system/tart-guest-agent.service 2>/dev/null"
    )
    assert r.ok, f"could not read tart-guest-agent unit: {r.stderr}"
    assert "User=devm" in r.stdout, (
        f"tart-guest-agent unit still says User=admin (or missing) — "
        f"the build-time sed regressed:\n{r.stdout}"
    )
    assert "User=admin" not in r.stdout, (
        f"tart-guest-agent unit still contains User=admin:\n{r.stdout}"
    )
