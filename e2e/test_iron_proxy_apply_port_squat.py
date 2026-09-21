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
  1. devm.yaml declares `env.TEST_TOKEN: !secret TEST_TOKEN`; plant
     the secret's initial value.
  2. Cold-start (iron-proxy spawns holding the initial secret hash).
  3. Read iron-proxy's on-disk config for the HTTPS listen port.
  4. Arm the delay hook (3000ms).
  5. Rotate the secret's value. `devm reconcile --yes` now sees a
     KindSecretChange → BucketEgressRestart → apply-iron-proxy fires
     (secret rotations respawn iron-proxy; allowlist edits do not —
     those are BucketLive and skip this handler entirely).
  6. In the delay window a squatter thread polls until it wins the
     freed HTTPS port and holds it. iron-proxy's spawn then loses the
     bind race and exits.
  7. Assert reconcile FAILS with the identity-check's error message.
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
    # Plant the secret BEFORE cold-start so iron-proxy comes up with
    # its value already baked into the hash iron-proxy tracks.
    subprocess.run(
        [devm.path, "secret", "set", "TEST_TOKEN"],
        input=b"v1\n",
        cwd=str(workspace.path),
        capture_output=True, timeout=15, check=True,
    )

    # write_devmyaml's YAML dumper won't emit a `!secret` tag, so
    # hand-craft the file. Fixture guard needs github.com in allow
    # because we're not passing no_repo=True (secret reconcile needs a
    # cold-started, running project). Actually — no_repo=True is fine
    # too since apply-iron-proxy doesn't touch the repo.
    workspace.write_devmyaml(
        no_repo=True,
        env={"TEST_TOKEN": "placeholder"},
        network={
            "allow": [
                # `{host, secrets}` shape: iron-proxy is authorized to
                # substitute TEST_TOKEN's value into requests targeting
                # api.github.com. Env reference + host binding both need
                # to be present or schema validation refuses cold-start.
                {"host": "api.github.com", "secrets": ["TEST_TOKEN"]},
            ],
        },
    )
    # write_devmyaml uses safe_dump which can't emit `!secret` tags;
    # patch it in as raw YAML.
    raw = workspace.devmyaml_path.read_text()
    raw = raw.replace("TEST_TOKEN: placeholder", "TEST_TOKEN: !secret TEST_TOKEN")
    workspace.devmyaml_path.write_text(raw)

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
        """Poll until the port is free (iron-proxy released it in the
        stop-gate) then bind before the delay hook expires and spawn
        runs. Retries at 50ms intervals for up to 10s — apply-iron-proxy
        may take longer than a fixed sleep to reach its stop-gate under
        e2e load, so a fixed lead-time races unreliably.
        """
        nonlocal squatter
        deadline = time.monotonic() + 10.0
        while time.monotonic() < deadline:
            s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try:
                s.bind(("127.0.0.1", https_port))
            except OSError:
                s.close()
                time.sleep(0.05)
                continue
            try:
                s.listen(1)
            except OSError as exc:
                s.close()
                squatter_error.append(exc)
                return
            squatter = s
            return
        squatter_error.append(TimeoutError(
            f"squatter never won port {https_port} within 10s — "
            f"apply-iron-proxy stop-gate never released it, or the "
            f"delay hook fired and spawn re-bound before poll"
        ))

    try:
        # Rotate the secret's on-disk value. reconcile will see a
        # KindSecretChange → BucketEgressRestart → apply-iron-proxy.
        # No devm.yaml edit; approve gate stays out of it.
        subprocess.run(
            [devm.path, "secret", "set", "TEST_TOKEN"],
            input=b"v2\n",
            cwd=str(workspace.path),
            capture_output=True, timeout=15, check=True,
        )

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
        # Identity check surfaces one of two messages depending on
        # timing: "exited after spawn" (first check catches the exit)
        # or "crash-looping after spawn" (second check catches the
        # backoff-restart gap). Either is a correct fail-loud.
        assert (
            "iron-proxy exited after spawn" in stderr
            or "iron-proxy crash-looping after spawn" in stderr
        ), f"expected the apply-iron-proxy identity-check failure in stderr; got {stderr!r}"
    finally:
        if squatter is not None:
            squatter.close()
        _HOOK_PATH.unlink(missing_ok=True)
        # workspace fixture's own finally does the guaranteed
        # `devm teardown --yes` (best-effort, swallowed) once this
        # test returns — no need to duplicate it here.
