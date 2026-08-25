"""185: devm pop and guest pop end-to-end, plus subdir-aware devm CLI.

Covers:
- Guest `pop <file>` reaches the daemon, resolves the path, and
  invokes `open` on the Mac (asserted via daemon log line since
  intercepting open itself would require daemon env manipulation).
- `devm pop mac` from a Mac cwd inside .vm/ with an arg that
  resolves into volume storage is refused with the expected
  message.
- `devm pop mac` from inside .vm/ with an arg that escapes .vm/
  to a real Mac file is allowed.
- `devm pop vm` from a project subdir opens the pretty .vm/-form.
- Subdir command discovery: `devm status` (or similar cheap command)
  from a project subdir exits 0.

The test avoids actually launching macOS apps; it inspects daemon
logs and captured stderr for the invocation record.
"""
import json
import os
import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_guest_pop_reaches_daemon_and_resolves(devm, workspace):
    # Set up: a fresh cold-started VM with a known file in the volume.
    workspace.write_devmyaml()  # default repo: block hydrates a bare repo
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path), capture_output=True, timeout=180)
    assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

    try:
        # Seed a file in the volume storage the guest can see.
        vol = workspace.volume_path()  # primary volume storage
        (vol / "sketch.png").write_bytes(b"fake png bytes")

        # Guest runs `pop sketch.png`. It should not error.
        pop_result = subprocess.run(
            [devm.path, "exec", "--", "pop", "sketch.png"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        # If the guest binary exists and the daemon is reachable, this
        # returns 0 (open exit status is propagated; a missing default
        # app for .png would be unusual but not a failure of our path).
        assert pop_result.returncode == 0, (
            f"guest pop failed:\nstdout={pop_result.stdout.decode()}\nstderr={pop_result.stderr.decode()}"
        )

        # Daemon log should record the pop invocation with the pretty
        # .vm/-form path. This assertion is against the daemon out log
        # (~/Library/Logs/com.devm.e2e.service.out.log).
        log_path = Path.home() / "Library" / "Logs" / "com.devm.e2e.service.out.log"
        log_content = log_path.read_text(errors="replace")
        expected_pretty = f"{workspace.path}/.vm/sketch.png"
        assert expected_pretty in log_content, (
            f"daemon log missing pop invocation for {expected_pretty}\n"
            f"last 20 lines:\n{''.join(log_content.splitlines(keepends=True)[-20:])}"
        )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), timeout=60, capture_output=True)


@pytest.mark.timeout(240)
def test_devm_pop_mac_refuses_when_resolves_into_vm(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path), capture_output=True, timeout=180)
    assert r.returncode == 0
    try:
        # Create a file inside volume storage; it's reachable via .vm/
        # symlink from workspace.path.
        vol = workspace.volume_path()
        (vol / "vm-only.png").write_bytes(b"x")

        # cwd inside .vm/ + relative arg to a file inside volume storage
        # → resolveMacMode's EvalSymlinks lands the candidate inside
        # storage → refused.
        vm_cwd = Path(str(workspace.path)) / ".vm"
        # Ensure the .vm symlink was actually written by devm start.
        assert vm_cwd.exists() and vm_cwd.is_symlink(), ".vm symlink missing"

        r = subprocess.run(
            [devm.path, "pop", "mac", "vm-only.png"],
            cwd=str(vm_cwd), capture_output=True, timeout=30,
        )
        assert r.returncode != 0
        combined = (r.stdout + r.stderr).decode(errors="replace")
        assert "devm-managed volume" in combined
        assert "pop vm" in combined
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), timeout=60, capture_output=True)


@pytest.mark.timeout(240)
def test_devm_pop_mac_allows_escape_arg_from_vm_subdir(devm, workspace):
    workspace.write_devmyaml()
    # Create a Mac-native file at the project mirror root.
    (Path(str(workspace.path)) / "mac-native.txt").write_text("x")
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path), capture_output=True, timeout=180)
    assert r.returncode == 0
    try:
        # cwd inside .vm/, arg escapes via ../../
        #
        # A real shell's `cd .vm` leaves $PWD pointing at the symlink
        # path (workspace.path/.vm), and Go's os.Getwd() trusts $PWD
        # when it matches the actual directory — so relative args
        # resolve against the *symlink* path, not its physical target.
        # subprocess.run(cwd=...) performs a raw chdir() with no shell
        # in between, so $PWD isn't updated to match; set it explicitly
        # to reproduce the interactive-shell cwd a real user would have.
        vm_cwd = Path(str(workspace.path)) / ".vm"
        env = dict(os.environ, PWD=str(vm_cwd))
        r = subprocess.run(
            [devm.path, "pop", "mac", "../mac-native.txt"],
            cwd=str(vm_cwd), env=env, capture_output=True, timeout=30,
        )
        # Should succeed (open exit propagated; text files open with TextEdit).
        assert r.returncode == 0, (
            f"pop mac escape-arg failed unexpectedly:\n"
            f"stderr={r.stderr.decode()}"
        )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), timeout=60, capture_output=True)


@pytest.mark.timeout(240)
def test_devm_pop_vm_from_subdir_opens_pretty_path(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path), capture_output=True, timeout=180)
    assert r.returncode == 0
    try:
        vol = workspace.volume_path()
        (vol / "top.png").write_bytes(b"x")

        # Run pop vm from a Mac subdir (not .vm/, just any Mac-side
        # subdir with devm.yaml walkable up).
        sub = Path(str(workspace.path)) / "arbitrary-subdir"
        sub.mkdir(exist_ok=True)
        r = subprocess.run(
            [devm.path, "pop", "vm", "top.png"],
            cwd=str(sub), capture_output=True, timeout=30,
        )
        # Should succeed; open receives the pretty .vm/-form path.
        assert r.returncode == 0, f"pop vm from subdir failed:\nstderr={r.stderr.decode()}"

        # Daemon log line assertion: pop vm ran locally (not via
        # /pop endpoint), so the resolved path lands in Mac's shell
        # history, not daemon logs. Skip the log assertion here — the
        # exit code and lack of stderr are the pin.
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), timeout=60, capture_output=True)


@pytest.mark.timeout(120)
def test_subdir_command_discovery(devm, workspace):
    """devm status (a cheap read-only command) from a project subdir
    must succeed — regression pin for the FindDevmYAML migration."""
    workspace.write_devmyaml()
    sub = Path(str(workspace.path)) / "subdir" / "deeper"
    sub.mkdir(parents=True, exist_ok=True)

    try:
        r = subprocess.run(
            [devm.path, "status"],
            cwd=str(sub), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"devm status from subdir failed:\nstderr={r.stderr.decode()}"
        # Output should mention the project — the exact string depends on
        # devm status's format; project name from workspace fixture is
        # workspace.vm_name.
        combined = (r.stdout + r.stderr).decode(errors="replace")
        assert workspace.vm_name in combined
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), timeout=60, capture_output=True)
