"""sbx-03: property-pin for `install:` step context.

Pure-sbx test (no devm). Locks down two contract properties that
devm's renderer relies on when prepending bootstrap.sh as the first
install step:
  * $WORKSPACE_DIR is set in the install step's environment
  * the workspace mount is visible at the path WORKSPACE_DIR points to

These are NOT quirks (no weird sbx behavior to work around) — they
are facts our renderer assumes. This test fails loudly if a future
sbx release changes the install: context, so we'd catch the
regression rather than discovering it via cryptic cold-start failures.

Motivation: 2026-06-05 devm dogfood saw "no sandbox named 'everstone'"
at WriteSnapshot time after a fresh `devm shell`. One leading theory
was that install: ran without $WORKSPACE_DIR set, so
    bash "$WORKSPACE_DIR/.devm/scripts/bootstrap.sh"
resolved to /.devm/scripts/bootstrap.sh, failed, sbx tore down.
This test rebuts that theory (both properties hold), redirecting
the investigation to the apt-vs-network-policy interaction instead.

The test plants a single install: step that dumps its environment and a
directory listing into files at HARDCODED absolute paths (/tmp/...),
NOT at $WORKSPACE_DIR/..., precisely so the diagnostic capture itself
doesn't depend on the thing under test. Then after bringup we sbx exec
into the sandbox to read those files.
"""
from __future__ import annotations
import os
import subprocess
import tempfile
import textwrap
import time

import pytest

from helpers import sbx
from helpers.sbx_kit import (
    KitFixture,
    wait_running,
    wait_exec_ready,
)

pytestmark = pytest.mark.sbx


# Plant two probe files at /tmp/* — chosen because /tmp is universally
# writable in the install context regardless of whether the workspace
# mount is up.
INSTALL_ENV_FILE = "/tmp/devm-probe-install-env.txt"
INSTALL_WS_LS_FILE = "/tmp/devm-probe-install-ws-ls.txt"
INSTALL_PROBE_RESULT = "/tmp/devm-probe-install-result.txt"


def _probe_kit(workspace_path: str) -> str:
    """Materialize a kit whose install: dumps env + workspace listing
    to /tmp. The kit's workspace path is hardcoded into the directory-
    listing probe so we don't have to rely on $WORKSPACE_DIR to read
    the workspace (the entire point of the probe is to find out whether
    that var is reliable at install time).
    """
    return textwrap.dedent(f"""\
        schemaVersion: "1"
        kind: agent
        name: installprobe
        displayName: install-env probe
        description: pure-sbx test of install-step environment
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
            - command: 'env > {INSTALL_ENV_FILE} 2>&1 || true;
              ls -la {workspace_path} > {INSTALL_WS_LS_FILE} 2>&1 || true;
              echo INSTALL_RAN_OK > {INSTALL_PROBE_RESULT}'
          startup:
            - command: ['sh', '-c', 'true']
              user: "1000"
              description: noop
    """)


def _materialize_probe_kit() -> KitFixture:
    workspace = tempfile.mkdtemp(prefix="sbx-probe-installenv-ws-")
    kit_dir = tempfile.mkdtemp(prefix="sbx-probe-installenv-kit-")
    # Drop a sentinel file in the workspace so a successful ls captures
    # something distinct (rules out "ls succeeded but workspace was empty
    # by coincidence").
    with open(os.path.join(workspace, "WORKSPACE_SENTINEL"), "w") as f:
        f.write("present\n")
    with open(os.path.join(kit_dir, "spec.yaml"), "w") as f:
        f.write(_probe_kit(workspace))
    return KitFixture(kit_dir=kit_dir, workspace=workspace)


def _read_file_inside(sandbox_name: str, path: str, timeout: float = 5.0) -> str:
    """Return file contents as str, or '' if missing/empty."""
    p = subprocess.run(
        ["sbx", "exec", sandbox_name, "cat", path],
        capture_output=True, timeout=timeout,
    )
    if p.returncode != 0:
        return ""
    return p.stdout.decode()


@pytest.mark.timeout(180)
def test_install_step_env_and_mount(sandbox_name):
    """Locks down THREE properties of sbx install: context for devm's
    renderer to rely on:

      1. Whether install runs at all (sentinel file written from install)
      2. Whether $WORKSPACE_DIR is set when install runs
      3. Whether the workspace mount is visible when install runs

    The test asserts only on the conjunction we care about: install
    runs AND the workspace mount is visible (otherwise the renderer
    cannot point install at $WORKSPACE_DIR/.devm/scripts/*.sh). The
    $WORKSPACE_DIR question is reported as a printed diagnostic plus
    a soft assertion — it's the easy fix if it's unset, but the
    workspace-mount question is the load-bearing one for our rendered
    install line.
    """
    kit = _materialize_probe_kit()
    try:
        proc = subprocess.Popen(
            ["sbx", "run",
             "--kit", kit.kit_dir,
             "--name", sandbox_name,
             "installprobe",
             kit.workspace],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
        )
        try:
            wait_running(sandbox_name)
            wait_exec_ready(sandbox_name)
            # Give the install step a beat to run; it's a single env
            # dump + ls so this is generous.
            time.sleep(2)

            # 1. Install ran at all?
            result = _read_file_inside(sandbox_name, INSTALL_PROBE_RESULT).strip()
            assert result == "INSTALL_RAN_OK", (
                f"install probe sentinel missing or unexpected: {result!r}\n"
                f"either install: never ran, or it ran and failed before the "
                f"final `echo INSTALL_RAN_OK` step"
            )

            # 3. Workspace mount visible? (Asserting first because this
            # is the load-bearing fact.)
            ws_ls = _read_file_inside(sandbox_name, INSTALL_WS_LS_FILE)
            assert "WORKSPACE_SENTINEL" in ws_ls, (
                f"sentinel file not visible in install-time `ls` of workspace; "
                f"either the workspace mount isn't up during install or it's "
                f"mounted elsewhere.\nls output was:\n{ws_ls}"
            )

            # 2. Whether $WORKSPACE_DIR was set. Reported as diagnostic —
            # if absent, the fix is to use an absolute path in the
            # renderer rather than $WORKSPACE_DIR.
            env_dump = _read_file_inside(sandbox_name, INSTALL_ENV_FILE)
            workspace_dir_set = any(
                line.startswith(f"WORKSPACE_DIR={kit.workspace}")
                for line in env_dump.splitlines()
            )
            print(f"\n[diagnostic] WORKSPACE_DIR set in install env: {workspace_dir_set}")
            if not workspace_dir_set:
                # Print the env so the diagnostic is actionable.
                print(f"[diagnostic] install env was:\n{env_dump}")
            # Hard assertion: if WORKSPACE_DIR isn't set, the renderer's
            # `bash "$WORKSPACE_DIR/.devm/scripts/bootstrap.sh"` form
            # cannot work and must switch to an absolute path. Failing
            # this assertion pins the bug.
            assert workspace_dir_set, (
                "$WORKSPACE_DIR is NOT set in install env — the rendered "
                "install line must use an absolute path instead. See "
                "internal/render/spec.go bootstrap install command."
            )
        finally:
            # Tear down the sandbox before letting the Popen go.
            subprocess.run(["sbx", "stop", sandbox_name],
                           capture_output=True, timeout=15)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=5)
    finally:
        kit.cleanup()
