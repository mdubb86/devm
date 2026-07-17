"""50: cold-start brings VM to running state, exec-ready, base-image invariants.

Consolidated (was also test_55, test_63, test_82): all four are
read-only assertions against a single freshly cold-started VM with a
default devm.yaml — no state mutation, so they can share one boot
instead of four.

What this pins:
  - Cold-create path brings the VM to 'running' state.
  - tart exec (via tart_sandbox.exec) works on the running VM.
  - tart exec propagates the inner command's exit code, both a zero
    and a non-zero (17) exit (was test_55).
  - bash is on PATH and executes a trivial command (was test_63) —
    devm's rendered shell wrappers (.devm/scripts/wrap-fg.sh,
    wrap-bg.sh) depend on bash-isms.
  - The guest user is `devm` (uid 1000, primary group `devm`), sudoers
    survived the admin->devm rename, the rename machinery itself is
    gone from the shipped image, and tart-guest-agent runs as devm
    (was test_82).

What it doesn't cover (tested elsewhere):
  - Interactive shell prompt -> test_01.
  - Stop lifecycle -> test_03, test_52.
  - Teardown -> test_05, test_53.
  - bash pipe + PIPESTATUS contract -> test_64.
  - WORKSPACE_DIR in exec contexts -> test_61.
  - The raw cirruslabs guest still being `admin` -> test_tart_contract_04.
  - Rename firing on first boot of a clone (design bakes the rename
    into devm-base itself; the one-shot never fires again).
"""
from __future__ import annotations

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)  # first-time devm-base build via the autouse fixture is ~5 min
def test_cold_start_exec_bash_identity(tart_sandbox):
    # ---- (was test_50) Cold-create path brings the VM to 'running'
    # ---- state; tart exec works on it. ----
    current = tart_sandbox.state()
    assert current == "running", (
        f"expected VM to be running after cold-start; got {current!r}"
    )
    result = tart_sandbox.exec("true")
    assert result.exit_code == 0, (
        f"`true` inside VM should return 0; got {result.exit_code}"
    )

    # ---- (was test_55) tart exec propagates the inner command's exit
    # ---- code, both directions. ----
    fail = tart_sandbox.exec_shell("exit 17")
    assert fail.exit_code == 17, (
        f"`exit 17` should be propagated; got {fail.exit_code}"
    )

    # ---- (was test_63) bash is preinstalled on the base image. ----
    r = tart_sandbox.exec_shell("command -v bash")
    assert r.ok, (
        f"bash missing on Tart base image — 'command -v bash' failed: "
        f"stdout={r.stdout!r} stderr={r.stderr!r}"
    )
    bash_path = r.stdout.strip()
    assert bash_path, f"empty path from 'command -v bash': {r.stdout!r}"

    r = tart_sandbox.exec("bash", "-c", 'echo "rc=$?"')
    assert r.ok and "rc=0" in r.stdout, (
        f"bash present but won't run trivial command: "
        f"stdout={r.stdout!r} stderr={r.stderr!r}"
    )

    # ---- (was test_82) guest user is `devm` (uid 1000), sudoers
    # ---- survived the rename, transient rename machinery gone. ----
    r = tart_sandbox.exec_shell("id -un && id -u && id -gn")
    assert r.ok, f"id failed: {r.stderr}"
    lines = r.stdout.strip().splitlines()
    assert lines == ["devm", "1000", "devm"], (
        f"unexpected identity — expected ['devm', '1000', 'devm'], got {lines!r}"
    )

    r = tart_sandbox.exec_shell("sudo -n whoami")
    assert r.ok, f"sudo failed post-rename: {r.stderr}"
    assert r.stdout.strip() == "root", (
        f"sudo -n whoami returned {r.stdout.strip()!r}, expected 'root'. "
        "sudoers.d rewrite of admin -> devm regressed."
    )

    r = tart_sandbox.exec_shell(r"stat -c '%U %G' /home/devm")
    assert r.ok, f"stat /home/devm failed: {r.stderr}"
    assert r.stdout.strip() == "devm devm", (
        f"/home/devm owner is {r.stdout.strip()!r}, expected 'devm devm'"
    )

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
    for rename_artifact in (
        "/usr/local/bin/devm-rename-user",
        "/etc/systemd/system/devm-rename-user.service",
        "/etc/systemd/system/multi-user.target.wants/devm-rename-user.service",
        "/var/lib/devm/user-renamed",
    ):
        r = tart_sandbox.exec_shell(f"test -e {rename_artifact} && echo present || echo absent")
        assert r.ok
        assert r.stdout.strip() == "absent", (
            f"{rename_artifact} still exists inside a project VM — the build-time "
            "cleanup step (internal/image/builder.go cleanupScript) regressed."
        )

    # tart-guest-agent's User= is `devm` — pinned by every other test
    # that reaches the VM via `tart exec` (they only work if the agent
    # is running under the renamed user), but we assert it directly
    # here so the failure mode is obvious.
    #
    # The unit ships at /etc/systemd/system/ on cirruslabs (verified via
    # test_tart_contract_13). Grep on that path only — some other
    # distros put it under /usr/lib/systemd/system, and passing both
    # paths to a single grep would return exit 2 (file unreadable) even
    # when the match succeeded, which is misleading.
    r = tart_sandbox.exec_shell(
        "grep '^User=' /etc/systemd/system/tart-guest-agent.service"
    )
    assert r.ok, f"could not read tart-guest-agent unit: {r.stderr}"
    assert "User=devm" in r.stdout, (
        f"tart-guest-agent unit still says User=admin (or missing) — "
        f"the build-time sed regressed:\n{r.stdout}"
    )
    assert "User=admin" not in r.stdout, (
        f"tart-guest-agent unit still contains User=admin:\n{r.stdout}"
    )
