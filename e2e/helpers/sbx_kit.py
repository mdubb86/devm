"""Shared fixtures for the test_sbx_anchor_* fleet.

These tests probe sbx behavior directly (no devm Go in the loop) so we
can pin down the load-bearing assumptions of the "anchor stays alive"
architecture. Each test materializes its own minimal kit + workspace,
spawns `sbx run` as the anchor, and observes behavior; the helpers
here collapse the boilerplate.

PEXPECT POLICY (also see test_sbx_anchor_* file headers):
  pexpect is for driving an interactive bash session as a user would
  type into it. The user shell host process MUST inherit the wrapper's
  stdio (production-shape). It is an antipattern to
  pexpect.spawn(["sbx", "exec", "-it", ...]) directly — pexpect
  allocates a fresh PTY for the child, which sbx happens to treat
  specially and which production never produces. Use the wrapper-exec
  pattern in tests that need an interactive shell.
"""
from __future__ import annotations
import os
import subprocess
import tempfile
import textwrap
import time
from dataclasses import dataclass
from typing import Optional

from helpers import sbx


# A kit that's deliberately devm-shaped: two foreground startup steps
# that reference scripts on disk (init-volumes.sh, install-templates.sh)
# plus a long-running heartbeat daemon wrapped foreground+nohup&.
# Matches what `internal/render/spec.go` produces today.
DEVM_SHAPED_KIT = textwrap.dedent("""\
    schemaVersion: "1"
    kind: agent
    name: anchortest
    displayName: anchor-architecture probe
    description: pure-sbx tests pinning down anchor-alive behavior
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
        - command: 'touch /tmp/install-marker'
      startup:
        - command: ['bash', '-c', 'exec bash "$WORKSPACE_DIR/.devm/scripts/init-volumes.sh"']
          user: "1000"
          description: Claim ext4 volume mounts for agent user
        - command: ['bash', '-c', 'exec bash "$WORKSPACE_DIR/.devm/scripts/install-templates.sh"']
          user: "0"
          description: Install rendered service templates
        - command: ['sh', '-c', 'nohup ''sh'' ''-c'' ''date +%s.%N > /tmp/daemon-start; while true; do date +%s.%N >> /tmp/daemon-trail; sleep 0.1; done'' > ''/tmp/daemon.log'' 2>&1 &']
          user: "1000"
          description: 'heartbeat startup daemon (foreground + nohup&)'
""")


@dataclass
class KitFixture:
    """Materialized kit + workspace tempdirs. Caller is responsible for
    cleanup (use as a context manager or call .cleanup())."""

    kit_dir: str
    workspace: str

    def cleanup(self) -> None:
        import shutil
        shutil.rmtree(self.kit_dir, ignore_errors=True)
        shutil.rmtree(self.workspace, ignore_errors=True)


def materialize_kit(*, spec: str = DEVM_SHAPED_KIT) -> KitFixture:
    """Write a kit spec.yaml + the devm-style init-volumes / install-templates
    scripts at <workspace>/.devm/scripts/. Returns the paths."""
    workspace = tempfile.mkdtemp(prefix="sbx-anchor-ws-")
    kit_dir = tempfile.mkdtemp(prefix="sbx-anchor-kit-")
    with open(os.path.join(kit_dir, "spec.yaml"), "w") as f:
        f.write(spec)

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
    return KitFixture(kit_dir=kit_dir, workspace=workspace)


def spawn_anchor(
    sandbox_name: str,
    kit: KitFixture,
    *,
    agent_name: str = "anchortest",
    stdout=subprocess.DEVNULL,
    stderr=None,
    setsid: bool = False,
) -> subprocess.Popen:
    """Spawn `sbx run` as the anchor. Matches devm's
    ExecSpawner{Interactive:false} shape by default (stdin=DEVNULL,
    stdout=DEVNULL, stderr=parent). Set setsid=True to detach the
    process from the parent's session/process group (for the
    parent-exits-anchor-survives test)."""
    return subprocess.Popen(
        [
            "sbx", "run",
            "--kit", kit.kit_dir,
            "--name", sandbox_name,
            agent_name,
            kit.workspace,
        ],
        stdin=subprocess.DEVNULL,
        stdout=stdout,
        stderr=stderr,
        start_new_session=setsid,
    )


def wait_running(sandbox_name: str, *, timeout: float = 60.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "running":
            return
        time.sleep(0.25)
    raise AssertionError(f"sandbox {sandbox_name} never reached 'running' within {timeout}s")


def wait_exec_ready(sandbox_name: str, *, timeout: float = 30.0) -> None:
    deadline = time.monotonic() + timeout
    last_err: Optional[bytes] = None
    while time.monotonic() < deadline:
        p = subprocess.run(
            ["sbx", "exec", sandbox_name, "true"],
            capture_output=True, timeout=5,
        )
        if p.returncode == 0:
            return
        last_err = p.stderr
        time.sleep(0.25)
    raise AssertionError(
        f"sandbox {sandbox_name} not exec-ready within {timeout}s; "
        f"last stderr: {last_err!r}"
    )


def bring_up_anchored(
    sandbox_name: str,
    kit: KitFixture,
    **kw,
) -> subprocess.Popen:
    """Spawn anchor + wait for running + exec-ready. Returns the anchor
    Popen handle; caller is responsible for cleanup.

    Raises AssertionError if the anchor died during bring-up. Sbx
    occasionally fails on startup with a "no sandbox found" race — the
    sandbox can reach running for a moment but the `sbx run` process
    exits rc=1. Without this check downstream assertions about
    anchor.poll() get a delayed, confusing failure."""
    anchor = spawn_anchor(sandbox_name, kit, **kw)
    try:
        wait_running(sandbox_name)
        wait_exec_ready(sandbox_name)
        if anchor.poll() is not None:
            raise AssertionError(
                f"anchor exited rc={anchor.returncode} during bring-up "
                f"(sbx run startup race?); sandbox reached running but "
                f"the anchor process died"
            )
    except Exception:
        try:
            anchor.kill()
        except Exception:
            pass
        raise
    return anchor


def read_daemon_lifetime(sandbox_name: str, *, timeout: float = 10.0):
    """Read /tmp/daemon-start + last line of /tmp/daemon-trail + the
    line count + whether the daemon process is currently alive.

    Returns (start, last_heartbeat, count, alive) or None if files are
    missing.
    """
    r = subprocess.run(
        ["sbx", "exec", sandbox_name, "sh", "-c",
         "cat /tmp/daemon-start; echo ===; "
         "tail -1 /tmp/daemon-trail; echo ===; "
         "wc -l < /tmp/daemon-trail; echo ===; "
         "pgrep -af 'while true.*daemon-trail' | grep -v pgrep && echo ALIVE || echo DEAD"],
        capture_output=True, timeout=timeout,
    )
    if r.returncode != 0:
        return None
    parts = r.stdout.decode().split("===")
    if len(parts) < 4:
        return None
    try:
        start = float(parts[0].strip())
        last = float(parts[1].strip())
        count = int(parts[2].strip())
        alive = "ALIVE" in parts[3]
        return (start, last, count, alive)
    except ValueError:
        return None
