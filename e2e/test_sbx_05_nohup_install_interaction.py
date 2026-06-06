"""sbx-05: does wrapping `sbx run` in nohup break install: bringup?

Pure-sbx test (no devm). 2026-06-05 dogfood + the test_24 failure
showed that the EXACT SAME kit + EXACT SAME args bring up cleanly when
sbx run is invoked directly from a shell, but fail with "container is
not running" during sbx's started hook when invoked via devm. Devm
wraps the spawn with `nohup`. This test asks the simplest possible
question: does nohup alone cause the failure?

If this test fails (sandbox bringup breaks) → nohup is the trigger.
If this test passes → it's something else in devm's spawn shape (the
Go exec.Cmd shape, the runDone-channel goroutine, the order of post-
spawn operations) and we move the suspect upstream.

Uses an install: command that does real-ish work (apt-get update),
because the trivial `true`-install case has been shown to pass under
devm too — the regression only shows up with non-trivial install
commands.
"""
from __future__ import annotations
import os
import subprocess
import tempfile
import textwrap
import time

import pytest

from helpers import sbx


def _kit_spec(devm_shape: bool = False) -> str:
    if not devm_shape:
        startup_block = textwrap.dedent("""\
              startup:
                - command: ['sh', '-c', 'true']
                  user: "1000"
                  description: noop
        """).rstrip()
    else:
        # Exact same shape devm renders today (init-volumes + install-
        # templates), and importantly: BARE flow-style strings (no
        # single-quotes around the array elements) matching yaml.v3's
        # output.
        startup_block = textwrap.dedent("""\
              startup:
                - command: [bash, -c, exec bash "$WORKSPACE_DIR/.devm/scripts/init-volumes.sh"]
                  user: "1000"
                  description: Claim ext4 volume mounts for agent user
                - command: [bash, -c, exec bash "$WORKSPACE_DIR/.devm/scripts/install-templates.sh"]
                  user: "0"
                  description: Install rendered service templates
        """).rstrip()
    return textwrap.dedent("""\
        schemaVersion: "1"
        kind: agent
        name: nohupprobe
        displayName: nohup install probe
        description: pure-sbx test of nohup-wrapped sbx run vs install
        agent:
          image: docker/sandbox-templates:shell
          aiFilename: CLAUDE.md
          entrypoint:
            run: ["sh", "-c", "exec sleep infinity </dev/null"]
        environment:
          variables:
            IS_SANDBOX: "1"
        commands:
          install:
            - command: apt-get update
            - command: touch /home/agent/marker-a
        """) + startup_block + "\n"


def _materialize_kit(*, devm_shape: bool = False, kit_inside_ws: bool = False) -> tuple[str, str]:
    workspace = tempfile.mkdtemp(prefix="sbx-nohupprobe-ws-")
    if kit_inside_ws:
        # Match devm's exact layout: kit is at <workspace>/.devm/
        kit_dir = os.path.join(workspace, ".devm")
        os.makedirs(kit_dir, exist_ok=True)
    else:
        kit_dir = tempfile.mkdtemp(prefix="sbx-nohupprobe-kit-")
    with open(os.path.join(kit_dir, "spec.yaml"), "w") as f:
        f.write(_kit_spec(devm_shape=devm_shape))
    if devm_shape:
        # Plant the scripts devm's startup steps reference. Same content
        # as internal/scripts/init-volumes.sh and install-templates.sh.
        scripts_dir = os.path.join(workspace, ".devm", "scripts")
        os.makedirs(scripts_dir, exist_ok=True)
        with open(os.path.join(scripts_dir, "init-volumes.sh"), "w") as f:
            f.write(
                "#!/usr/bin/env bash\n"
                "set -euo pipefail\n"
                'mounts=$(findmnt -ln -t ext4 -o TARGET | grep -F "$WORKSPACE_DIR" || true)\n'
                '[ -n "$mounts" ] || exit 0\n'
            )
        with open(os.path.join(scripts_dir, "install-templates.sh"), "w") as f:
            f.write(
                "#!/usr/bin/env bash\n"
                "set -euo pipefail\n"
                'DIR="$WORKSPACE_DIR/.devm/templates"\n'
                '[ -d "$DIR" ] || exit 0\n'
            )
        os.chmod(os.path.join(scripts_dir, "init-volumes.sh"), 0o755)
        os.chmod(os.path.join(scripts_dir, "install-templates.sh"), 0o755)
    return workspace, kit_dir


def _cleanup(workspace: str, kit_dir: str) -> None:
    import shutil
    shutil.rmtree(workspace, ignore_errors=True)
    shutil.rmtree(kit_dir, ignore_errors=True)


def _run_and_check(sandbox_name: str, workspace: str, kit_dir: str,
                   *, wrap_nohup: bool, timeout: float = 90.0) -> tuple[int, str]:
    """Spawn `sbx run` and wait until either sandbox is exec-ready
    or sbx run exits. wrap_nohup=True prepends `nohup` to match
    devm's spawn shape; False matches test_sbx_04's known-passing
    shape."""
    cmd = []
    if wrap_nohup:
        cmd.append("nohup")
    cmd.extend([
        "sbx", "run",
        "--kit", kit_dir,
        "--name", sandbox_name,
        "nohupprobe",
        workspace,
    ])
    proc = subprocess.Popen(
        cmd,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    deadline = time.monotonic() + timeout
    try:
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                stderr = proc.stderr.read().decode() if proc.stderr else ""
                stdout = proc.stdout.read().decode() if proc.stdout else ""
                return proc.returncode, stdout + stderr
            if sbx.sandbox_state(sandbox_name) == "running":
                p = subprocess.run(
                    ["sbx", "exec", sandbox_name, "true"],
                    capture_output=True, timeout=5,
                )
                if p.returncode == 0:
                    return -1, ""  # success
            time.sleep(0.5)
        return -2, "timeout"
    finally:
        try:
            proc.kill()
        except Exception:
            pass
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            pass


@pytest.mark.timeout(180)
def test_baseline_no_nohup_brings_up(sandbox_name):
    """Sanity-check: direct sbx run (no nohup) brings up the apt-install
    kit. Matches test_sbx_04's known-passing shape."""
    workspace, kit_dir = _materialize_kit()
    try:
        rc, output = _run_and_check(sandbox_name, workspace, kit_dir, wrap_nohup=False)
        assert rc == -1, (
            f"baseline (no nohup) FAILED: rc={rc}, output={output!r}"
        )
    finally:
        subprocess.run(["sbx", "stop", sandbox_name], capture_output=True, timeout=15)
        subprocess.run(["sbx", "rm", "-f", sandbox_name], capture_output=True, timeout=15)
        _cleanup(workspace, kit_dir)


@pytest.mark.timeout(180)
def test_nohup_wrapped_brings_up(sandbox_name):
    """The decisive test: nohup-wrapped sbx run with the same kit.
    If this fails, nohup is the regression trigger for devm's
    cold-start path."""
    workspace, kit_dir = _materialize_kit()
    try:
        rc, output = _run_and_check(sandbox_name, workspace, kit_dir, wrap_nohup=True)
        assert rc == -1, (
            f"nohup-wrapped sbx run FAILED: rc={rc}, output={output!r}\n"
            f"\nNOHUP IS THE TRIGGER. Devm's cold-start wraps sbx run in "
            f"nohup; reverting that wrap will fix the regression. The "
            f"nohup wrap was added 2026-06-04 (commit history) to make "
            f"the anchor survive terminal close — we'll need an alternate "
            f"approach (setsid, explicit SIGHUP ignore inside Go, etc.)."
        )
    finally:
        subprocess.run(["sbx", "stop", sandbox_name], capture_output=True, timeout=15)
        subprocess.run(["sbx", "rm", "-f", sandbox_name], capture_output=True, timeout=15)
        _cleanup(workspace, kit_dir)


@pytest.mark.timeout(180)
def test_nohup_plus_devm_shape_startup(sandbox_name):
    """Mimic devm's spec.yaml exactly: nohup-wrapped sbx run, with the
    same 2 startup steps referencing $WORKSPACE_DIR/.devm/scripts/*.sh,
    and the same 2 install steps. If this fails, the devm-style spec
    is the regression trigger (not nohup, not apt-get, not the
    snapshot exec)."""
    workspace, kit_dir = _materialize_kit(devm_shape=True)
    try:
        rc, output = _run_and_check(sandbox_name, workspace, kit_dir, wrap_nohup=True)
        assert rc == -1, (
            f"devm-shape kit bringup FAILED: rc={rc}, output={output!r}\n"
            f"\nThe devm-shape spec is the regression trigger. The "
            f"differences vs the passing 'noop startup' kit are:\n"
            f"  - 2 startup steps instead of 1\n"
            f"  - startup refs scripts at $WORKSPACE_DIR/.devm/scripts/*.sh\n"
            f"  - second startup runs as user 0 (root)\n"
            f"Narrow further by disabling each in turn."
        )
    finally:
        subprocess.run(["sbx", "stop", sandbox_name], capture_output=True, timeout=15)
        subprocess.run(["sbx", "rm", "-f", sandbox_name], capture_output=True, timeout=15)
        _cleanup(workspace, kit_dir)


@pytest.mark.timeout(180)
def test_nohup_with_kit_inside_workspace(sandbox_name):
    """Match devm's exact directory layout: --kit <workspace>/.devm/.
    Devm uses this pattern; test_sbx_05's other tests use separate
    kit/workspace dirs. If this fails, the kit-inside-workspace
    pattern is the trigger."""
    workspace, kit_dir = _materialize_kit(devm_shape=True, kit_inside_ws=True)
    try:
        rc, output = _run_and_check(sandbox_name, workspace, kit_dir, wrap_nohup=True)
        assert rc == -1, (
            f"kit-inside-workspace bringup FAILED: rc={rc}, output={output!r}\n"
            f"\nKIT-INSIDE-WORKSPACE IS THE TRIGGER. Devm renders .devm/ "
            f"inside the workspace and passes --kit <workspace>/.devm/; "
            f"sbx might be confused by overlapping mount + kit paths."
        )
    finally:
        subprocess.run(["sbx", "stop", sandbox_name], capture_output=True, timeout=15)
        subprocess.run(["sbx", "rm", "-f", sandbox_name], capture_output=True, timeout=15)
        _cleanup(workspace, kit_dir)


@pytest.mark.timeout(180)
def test_nohup_plus_snapshot_style_exec_during_install(sandbox_name):
    """Mimic devm's full cold-start sequence: spawn under nohup, wait
    for exec-ready, THEN immediately run an sbx-exec that writes a
    file (WriteSnapshot's shape). If install is still happening
    underneath and that exec triggers a crash, this reproduces the
    test_14/everstone failure shape outside devm."""
    workspace, kit_dir = _materialize_kit()
    try:
        rc, output = _run_and_check(sandbox_name, workspace, kit_dir, wrap_nohup=True)
        assert rc == -1, f"bringup itself failed: rc={rc}, output={output!r}"

        # Now do the snapshot-style exec — same shape as WriteSnapshot
        # (mkdir + base64 decode + atomic mv). Runs IMMEDIATELY after
        # exec-ready, mirroring devm's flow.
        snapshot_cmd = (
            "mkdir -p /home/agent/.devm && "
            "echo aGVsbG8K | base64 -d > /home/agent/.devm/applied.yaml.tmp && "
            "mv /home/agent/.devm/applied.yaml.tmp /home/agent/.devm/applied.yaml"
        )
        p = subprocess.run(
            ["sbx", "exec", sandbox_name, "sh", "-c", snapshot_cmd],
            capture_output=True, timeout=15,
        )
        assert p.returncode == 0, (
            f"snapshot-style exec FAILED post-bringup: rc={p.returncode}\n"
            f"stderr={p.stderr.decode()!r}\n"
            f"This reproduces the test_14 / everstone failure outside devm. "
            f"The trigger is the snapshot-style exec hitting the container "
            f"while install is still in progress (apt-get update takes "
            f"longer than waitForExecReady's window). Fix: wait for "
            f"install completion before WriteSnapshot."
        )
    finally:
        subprocess.run(["sbx", "stop", sandbox_name], capture_output=True, timeout=15)
        subprocess.run(["sbx", "rm", "-f", sandbox_name], capture_output=True, timeout=15)
        _cleanup(workspace, kit_dir)
