"""78: `services.X.masks` overlay per-service dirs over workspace paths via
bind-mount at cold-start.

devm renders each mask as a bind-mount from /var/devm/masks/<project>/<svc>/<path>
onto $WORKSPACE/<path>. Effect: writes to $WORKSPACE/<path> from inside
the VM land in the per-service mask dir instead of the shared workspace,
so a service can have its own sandboxed slice of the workspace tree that
doesn't collide with the host or other services.

What this pins:
  - `services.foo.masks: [{path: subdir, size: 100m}]` produces a bind
    mount from /var/devm/masks/<project-id>/foo/subdir onto
    $WORKSPACE/subdir at cold-start.
  - Content pre-existing on the host at $WORKSPACE/subdir is HIDDEN by
    the mask (bind-mount overlay).
  - Writes from inside the VM to $WORKSPACE/subdir land in the mask
    dir, NOT on the host — the host's original subdir stays unchanged.
  - The mask dir is writable by the guest user the service runs as (the
    provisioner chowns to svc.User, default devm) — no sudo needed for
    the natural in-mask write.

What it doesn't cover (tested elsewhere):
  - Mask validation (relative path, size required) — unit-tested in
    internal/schema.
  - The `size` field's actual effect (tmpfs sizing) is not exercised
    here; devm currently bind-mounts a regular dir, not tmpfs, so size
    is a config placeholder.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_service_masks_overlay_workspace_path(workspace, devm, sandbox_name):
    # Plant a host-side sentinel that will be HIDDEN by the mask.
    subdir = workspace.path / "masked"
    subdir.mkdir()
    (subdir / "HOST_SENTINEL").write_text("host-only\n")

    workspace.write_devmyaml(
        services={
            "masksvc": {
                "port": 8080,  # a service must declare at least one of port/exec/etc.
                "masks": [{"path": "masked", "size": "10m"}],
            },
        },
    )

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    tart_sandbox = TartSandbox(name=sandbox_name)
    ws = str(workspace.path)

    # The masked path is a mountpoint inside the VM.
    r = tart_sandbox.exec_shell(f"mountpoint -q {ws}/masked && echo mounted")
    assert r.ok and r.stdout.strip() == "mounted", (
        f"masked path not a mountpoint: exit={r.exit_code} stderr={r.stderr!r}"
    )

    # HOST_SENTINEL is HIDDEN by the mask (bind-mount over the dir).
    r = tart_sandbox.exec_shell(f"ls {ws}/masked/HOST_SENTINEL 2>&1; echo rc=$?")
    assert "rc=0" not in r.stdout, (
        f"HOST_SENTINEL still visible under mask — mask did not overlay: "
        f"{r.stdout!r}"
    )

    # Guest writes to $WORKSPACE/masked/GUEST_MARK land in the mask dir,
    # not on the host workspace. No sudo needed — the provisioner chowns
    # the mask dir to the service's User (default devm) before mount.
    r = tart_sandbox.exec_shell(f"echo from-guest > {ws}/masked/GUEST_MARK")
    assert r.ok, (
        f"devm failed to write into its own mask — the mask chown-to-user "
        f"fix regressed: {r.stderr}"
    )

    host_should_not_exist = subdir / "GUEST_MARK"
    assert not host_should_not_exist.exists(), (
        f"guest write leaked to host at {host_should_not_exist} — the mask "
        f"is not isolating from the host workspace"
    )

    # Host sentinel still present on host (mask didn't touch the underlying dir).
    assert (subdir / "HOST_SENTINEL").exists(), (
        "HOST_SENTINEL disappeared from host — mask should overlay, not delete"
    )
