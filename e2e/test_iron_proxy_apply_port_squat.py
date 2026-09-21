"""Port-squat race on /vm/apply-iron-proxy's health check.

waitIronProxyHealthy (internal/serviceapi/apply_iron_proxy.go) proves
only that *something* accepts a TCP connection on iron-proxy's HTTPS
port — not that the thing answering is the iron-proxy process the
handler just spawned. If the spawn loses the bind race (a squatter —
an orphan iron-proxy, or literally any other process — already holds
the port), the real iron-proxy logs a fatal bind error and exits
almost immediately, but the squatter still answers the health dial.
Pre-fix, the handler returned 200 to the caller: reconcile looked
successful while every real request was hitting the squatter instead
of iron-proxy.

The fix adds an identity check after the health dial: `sup.Status(key)
.Running` must also be true. The supervisor's process-monitor observes
iron-proxy's exit (bind failure) and flips Running false, so the
handler now fails loud instead of reporting a fake positive.

Reproduction needs to land a squatter on the port in the narrow window
between iron-proxy losing (or winning) the bind race and the health
check completing. A file-based delay hook
(`<runtime>/e2e-test-hooks/apply-iron-proxy-delay-ms`, read once per
apply-iron-proxy call, e2e-only via identity.Config.IsE2E()) widens
that window to something a test can reliably hit: pause right before
the spawn, bind the squatter mid-pause, let the spawn (and its losing
bind) proceed.

Sequence:
  1. Cold-start with network.allow=[api.github.com], no_repo=True.
  2. Read iron-proxy's on-disk config for the HTTPS listen port.
  3. Arm the delay hook (3000ms).
  4. In parallel: kick off `devm approve` + `devm reconcile --yes`
     (adds example.com to network.allow, which fires
     apply-iron-proxy), and shortly after, bind a squatter socket to
     iron-proxy's HTTPS port and hold it.
  5. Assert reconcile FAILS with the identity-check's error message.
"""
from __future__ import annotations

import socket
import subprocess
import threading
import time
from pathlib import Path

import pytest
import yaml

pytestmark = pytest.mark.devm

_HOOK_PATH = (
    Path.home() / "Library" / "Application Support" / "devm-e2e"
    / "e2e-test-hooks" / "apply-iron-proxy-delay-ms"
)


def _iron_proxy_https_port(vm_name: str) -> int:
    """Read iron-proxy's on-disk config and return its HTTPS listen port."""
    cfg_path = (
        Path.home() / "Library" / "Application Support" / "devm-e2e"
        / "iron-proxy" / f"{vm_name}.yaml"
    )
    cfg = yaml.safe_load(cfg_path.read_text())
    https_listen = cfg["proxy"]["https_listen"]
    return int(https_listen.rsplit(":", 1)[1])


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_iron_proxy_apply_port_squat(workspace, devm):
    workspace.write_devmyaml(
        no_repo=True,
        network={
            "allow": [
                "api.github.com",
            ],
        },
    )

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=600,
    )
    assert start.returncode == 0, (
        f"devm start failed:\nstderr={start.stderr.decode()!r}"
    )

    https_port = _iron_proxy_https_port(workspace.vm_name)

    # Arm the delay hook: apply-iron-proxy pauses 3s between the
    # stop-gate and the spawn, wide enough for the squatter thread
    # below to land its bind before iron-proxy's own spawn attempt.
    _HOOK_PATH.parent.mkdir(parents=True, exist_ok=True)
    _HOOK_PATH.write_text("3000")

    squatter: socket.socket | None = None
    squatter_error: list[BaseException] = []

    def _squat() -> None:
        nonlocal squatter
        try:
            time.sleep(0.5)
            s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            s.bind(("127.0.0.1", https_port))
            s.listen(1)
            squatter = s
        except BaseException as exc:  # pragma: no cover - surfaced via squatter_error
            squatter_error.append(exc)

    try:
        # Allowlist change + approve first — mirrors test_92's pattern
        # (devm.yaml is host-immutable while the VM runs; `devm approve`
        # unlocks it for the reconcile that follows).
        cfg = yaml.safe_load(workspace.devmyaml_path.read_text())
        cfg["network"]["allow"].append("example.com")
        workspace.devmyaml_path.write_text(yaml.safe_dump(cfg, sort_keys=False))

        approve = subprocess.run(
            [devm.path, "approve"],
            cwd=str(workspace.path), input=b"y\n",
            capture_output=True, timeout=30,
        )
        assert approve.returncode == 0, f"approve failed: {approve.stderr.decode()!r}"

        squat_thread = threading.Thread(target=_squat, daemon=True)
        squat_thread.start()

        reconcile = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path),
            capture_output=True, timeout=120,
        )

        squat_thread.join(timeout=30)
        assert not squatter_error, f"squatter thread failed: {squatter_error}"
        assert squatter is not None, "squatter never bound the iron-proxy HTTPS port"

        stderr = reconcile.stderr.decode()
        assert reconcile.returncode != 0, (
            f"reconcile should have FAILED (squatter held iron-proxy's port); "
            f"got rc=0, stdout={reconcile.stdout.decode()!r}"
        )
        assert "iron-proxy exited after spawn" in stderr, (
            f"expected the apply-iron-proxy identity-check failure in stderr; got {stderr!r}"
        )
    finally:
        if squatter is not None:
            squatter.close()
        _HOOK_PATH.unlink(missing_ok=True)
        # workspace fixture's own finally does the guaranteed
        # `devm teardown --yes` (best-effort, swallowed) once this
        # test returns — no need to duplicate it here.
