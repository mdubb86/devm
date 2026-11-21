"""`gdevm run <target>` dispatches the target as the devm user (uid
1000), not as root. A root-dispatched run reads credentials from the
wrong $HOME (authenticated package feeds 401) and leaves root-owned
build output under /home/devm that breaks every later run until
ownership is repaired by hand.

Pins the identity contract end-to-end: the service runs as devm, the
dispatched bash inherits uid 1000, $HOME is /home/devm, and files the
target creates in the repo are owned by devm — not root.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_gdevm_run_executes_as_devm(workspace, devm):
    workspace.write_devmyaml(
        repos={
            "main": {
                "url": workspace.bare_repo_url(),
                "primary": True,
                "commands": ["whoami"],
            },
        },
    )
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "whoami() {\n"
        '  echo "user=$(id -un)"\n'
        '  echo "uid=$(id -u)"\n'
        '  echo "home=$HOME"\n'
        # Touch a file inside the repo so we can assert ownership from
        # the Mac side — the whole point of the fix is that artifacts
        # land back in /home/devm owned by devm, not root.
        "  : > marker-from-run\n"
        "}\n"
    )
    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()!r}"

    guest_repo = f"/home/devm/{workspace.bare_repo_label()}"

    # Baseline: gdevm-serve.service itself is running under the devm
    # account, not root. systemd's `show -p User` resolves the
    # effective User= from the unit, so this catches a regression even
    # if the dispatch path somehow dropped privileges separately.
    show = devm_exec_with_retry(
        devm.path,
        ["systemctl", "show", "-p", "User", "gdevm-serve.service"],
        cwd=str(workspace.path), timeout=15,
    )
    assert show.returncode == 0, (
        f"systemctl show failed: stderr={show.stderr.decode()!r}"
    )
    assert show.stdout.decode().strip() == "User=devm", (
        f"gdevm-serve.service must run as devm, got {show.stdout.decode()!r}"
    )

    # Dispatch the target and read back identity + ownership.
    r = devm_exec_with_retry(
        devm.path,
        ["bash", "-c", f"cd {guest_repo} && gdevm run whoami"],
        cwd=str(workspace.path), timeout=60,
    )
    assert r.returncode == 0, (
        f"gdevm run whoami failed: stdout={r.stdout.decode()!r} "
        f"stderr={r.stderr.decode()!r}"
    )
    out = r.stdout.decode()
    assert "user=devm" in out, f"target must run as devm, got: {out!r}"
    assert "uid=1000" in out, f"target must run as uid 1000, got: {out!r}"
    assert "home=/home/devm" in out, (
        f"$HOME in the dispatched target must be /home/devm, got: {out!r}"
    )

    # Files the target created in the repo are owned by devm:devm, not
    # root. A root-owned artifact silently poisons the workspace for
    # every subsequent build.
    stat = devm_exec_with_retry(
        devm.path,
        ["stat", "-c", "%U:%G", f"{guest_repo}/marker-from-run"],
        cwd=str(workspace.path), timeout=15,
    )
    assert stat.returncode == 0, (
        f"stat failed: stdout={stat.stdout.decode()!r} "
        f"stderr={stat.stderr.decode()!r}"
    )
    assert stat.stdout.decode().strip() == "devm:devm", (
        f"artifact owner must be devm:devm, got: {stat.stdout.decode()!r}"
    )

    # /var/log/gdevm is devm-owned so the uid-1000 serve can write
    # state and log files; the install path's chown makes this hold
    # across reinstalls, not just the cold create.
    log_stat = devm_exec_with_retry(
        devm.path,
        ["stat", "-c", "%U:%G", "/var/log/gdevm"],
        cwd=str(workspace.path), timeout=15,
    )
    assert log_stat.returncode == 0
    assert log_stat.stdout.decode().strip() == "devm:devm", (
        f"/var/log/gdevm must be devm-owned, got: {log_stat.stdout.decode()!r}"
    )
