"""Shared fixtures for the devm e2e suite.

The fixtures register cleanup intent in the env-shared registry file
BEFORE creating any resource. If a fixture's finalizer doesn't run
(pytest SIGKILL, wedged worker), the bash wrapper's EXIT trap sweeps
the registry. See internal design notes.
"""
from __future__ import annotations
import json
import os
import re
import secrets
import shutil
import signal
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Callable, Iterator

import pytest

from helpers import Devm, Workspace, registry
from helpers.tart import TartSandbox


def pytest_collection_modifyitems(config, items):
    """Auto-mark tests that must NOT run under pytest-xdist parallelism:

      - `pty`: uses helpers.Shell (pexpect + pty.forkpty). xdist workers
        have a background RPC thread; forkpty in a multi-threaded process
        races on lock inheritance (Python's own DeprecationWarning on
        forkpty spells this out).

      - `install`: exercises devm's install lifecycle — `devm install`,
        `devm uninstall`, or `devm service restart` — which mutates
        global system state (LaunchDaemon plist, /etc/resolver/test,
        the system CA, the tart devm-base image). Concurrent runs of
        these tests step on each other's install/uninstall sequences.
        Semantic pair of the `devm` marker: install = installing devm
        itself; devm = using it. Run via `just e2e-install`.

    Both markers land in run.sh's single-process phase. Detection is a
    source-grep at collect time; matches beat missed marks because the
    cost of a single-process-run test is only latency, not correctness.
    """
    # `install` marker: tests that MUTATE the shared daemon during their
    # run. Merely NEEDING the daemon isn't enough; the session-scoped
    # _daemon_matches_devm_bin fixture handles that precondition once
    # at session start.
    _install_hints = (
        'devm.path, "install"',
        'devm.path, "uninstall"',
        '"service", "restart"',
    )
    for item in items:
        try:
            src = Path(item.fspath).read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        if "Shell(" in src or "from helpers import Shell" in src:
            item.add_marker(pytest.mark.pty)
        if any(h in src for h in _install_hints):
            item.add_marker(pytest.mark.install)




# --- session ---

@pytest.fixture(scope="session")
def devm_path() -> str:
    """Path to the freshly-built devm binary (run.sh exports DEVM_BIN)."""
    p = os.environ.get("DEVM_BIN")
    if not p:
        raise RuntimeError("DEVM_BIN not set (run.sh sets it; check the wrapper)")
    return p


# --- per-test ---

# (workspace is below; declared as a forward-ref dependency of `devm`.)
@pytest.fixture
def devm(devm_path, workspace) -> Devm:
    """Devm CLI wrapper bound to the test's workspace as cwd, so
    `devm.reconcile()` / `stop()` / `teardown()` read the test's
    devm.yaml (not e2e/'s)."""
    return Devm(devm_path, cwd=str(workspace.path))


def _slug_from_node(name: str) -> str:
    """`test_01_cold_start` -> `cold-start` (within 20 chars, alnum + hyphens)."""
    s = re.sub(r"^test_", "", name)
    s = re.sub(r"^\d+_", "", s)
    s = s.replace("_", "-")
    s = re.sub(r"[^a-z0-9-]", "", s.lower())
    return s[:20].rstrip("-") or "x"


@pytest.fixture
def sandbox_name(request) -> Iterator[str]:
    """Unique sandbox name; registered for cleanup before yield."""
    slug = _slug_from_node(request.node.name)
    name = f"e2e-{slug}-{secrets.token_hex(2)}"
    registry.append("sandbox", name)
    try:
        yield name
    finally:
        registry.remove("sandbox", name)


def _port_offset_from_file(filename: str) -> int:
    """Derive a deterministic, collision-free port_offset from the test
    file's NN_ prefix (e.g., `test_08_reconcile_live_port.py` -> 50800).

    Hash-of-sandbox-name with 70 buckets has a ~73% chance of collision
    across 14 parallel tests (birthday paradox), which makes the suite
    flaky. Using the test number from the filename gives every test its
    own bucket. Canonical ports we use are all coprime-mod-100 with each
    other (3000, 8080, 8081, 9090) so 100-port spacing rules out cross-
    canonical host-port collisions too.

    Note: take the filename, not request.node.name — the latter is the
    function name (e.g., `test_reconcile_live_port`), which has no digits.
    """
    m = re.match(r"^test_(\d+)_", filename)
    if not m:
        return 50000
    return 50000 + int(m.group(1)) * 100


@pytest.fixture
def workspace(request, devm_path, sandbox_name) -> Iterator[Workspace]:
    """Temp workspace dir bound to the test's sandbox_name.

    Cleans up the VM AND iron-proxy for the project at the end via
    `devm teardown --yes`. Tests don't need their own try/finally to
    do this — the fixture is authoritative. Tests that legitimately
    want to leave the sandbox in a specific end state (rare) can call
    `devm teardown` themselves; the finally here is idempotent.
    """
    slug = _slug_from_node(request.node.name)
    # Resolve symlinks so the path matches what devm (Go) sees inside
    # the spawned shell. On macOS, tempfile.mkdtemp returns
    # `/var/folders/...` but pexpect.spawn(cwd=...) → chdir → Go
    # os.Getwd surfaces the canonical `/private/var/folders/...`. If we
    # yield the unresolved form, every test that compares against
    # workspace.path (CLAUDE_CONFIG_DIR, $WORKSPACE, …) trips on the
    # symlink. Linux is a no-op (mkdtemp already canonical).
    path = Path(tempfile.mkdtemp(prefix=f"devm-e2e-{slug}-")).resolve()
    registry.append("workspace", str(path))
    try:
        port_offset = _port_offset_from_file(Path(request.node.fspath).name)
        ws = Workspace(path, slug=slug, vm_name=sandbox_name, port_offset=port_offset)
        ws.write_devmyaml()  # minimal config; tests can call write_devmyaml again with extras
        yield ws
    finally:
        # Guaranteed teardown: stops the VM AND its iron-proxy child
        # (which the tart-VM-only sweep in registry can't touch — it's
        # a Mac process, not a tart entity). Best-effort — if the daemon
        # is down or the sandbox is already absent, teardown noops with
        # non-zero, which we swallow.
        try:
            import subprocess as _sp
            _sp.run(
                [devm_path, "teardown", "--yes"],
                cwd=str(path),
                capture_output=True,
                timeout=60,
            )
        except Exception:
            pass
        shutil.rmtree(path, ignore_errors=True)
        registry.remove("workspace", str(path))


@pytest.fixture
def sudo_capable():
    """Skips the test if sudo can't realistically prompt the user.

    Skipped conditions:
      - non-macOS (sudo-required tests in this suite are macOS-only)
      - /dev/tty unavailable (CI / no controlling terminal — sudo
        would hang on a prompt it can't deliver)

    No priming: sudo opens /dev/tty directly for its Touch ID (or
    password) prompt, independent of pytest's capture of stdin/stdout,
    so tests that need sudo just call it and the prompt fires
    naturally. On macOS Touch ID doesn't share the sudo timestamp
    cache anyway — priming would just add an extra interaction per
    run.
    """
    import platform as _platform

    if _platform.system() != "Darwin":
        pytest.skip("sudo-required test runs on macOS only")
    try:
        open("/dev/tty").close()
    except OSError:
        pytest.skip("no /dev/tty — sudo can't prompt, skipping interactive test")


_LAUNCH_DAEMON_PLIST = Path("/Library/LaunchDaemons/com.devm.service.plist")


def _daemon_program_path() -> str | None:
    """Extract the ProgramArguments[0] from the running LaunchDaemon.
    Returns None if no daemon is registered.
    """
    r = subprocess.run(
        ["launchctl", "print", "system/com.devm.service"],
        capture_output=True, text=True, timeout=10,
    )
    if r.returncode != 0:
        return None
    for line in r.stdout.splitlines():
        line = line.strip()
        if line.startswith("program = "):
            return line[len("program = "):].strip()
    return None


def _install_devm(devm_path: str) -> None:
    r = subprocess.run(
        [devm_path, "install"],
        capture_output=True, timeout=780,
    )
    if r.returncode != 0:
        pytest.exit(
            f"session prerequisite `devm install` failed:\n"
            f"stdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )
    deadline = time.monotonic() + 15
    sock = Path(os.path.expanduser(
        "~/Library/Application Support/devm/devm.sock"
    ))
    while time.monotonic() < deadline:
        if sock.exists():
            return
        time.sleep(0.25)


def _uninstall_devm(devm_path: str) -> None:
    subprocess.run(
        [devm_path, "uninstall"],
        capture_output=True, timeout=30,
    )


@pytest.fixture(autouse=True)
def _daemon_matches_devm_bin(request, devm_path):
    """Verify-only safety net: the LaunchDaemon program path must match
    DEVM_BIN. The actual install happens once up-front in run.sh; if we
    ever get here and the daemon doesn't match, either a previous test
    uninstalled it and didn't restore, or the install failed silently.
    Either way, aborting the session immediately with an actionable
    message is better than 40 misleading per-test failures.

    Contract tests (marked `contract`) don't touch the devm daemon at
    all — they pin behavior of external tools like tart or iron-proxy
    directly, and skip the precondition entirely.
    """
    if request.node.get_closest_marker("contract") is not None:
        yield
        return

    import platform as _platform
    if _platform.system() != "Darwin":
        yield
        return

    # Isolated mode: run.sh started a foreground `devm serve --foreground`
    # in a private $DEVM_RUNTIME_DIR — there IS no LaunchDaemon plist for
    # the test daemon, and the check below would spuriously find the
    # user's REAL launchd-managed daemon (pointed at their real binary)
    # and abort. Run.sh already health-checked the isolated daemon before
    # invoking pytest, so no verification is needed here.
    if os.environ.get("E2E_ISOLATE") == "1":
        yield
        return

    current_program = _daemon_program_path()
    if current_program == devm_path and _LAUNCH_DAEMON_PLIST.exists():
        yield
        return

    pytest.exit(
        f"devm daemon doesn't match DEVM_BIN. Either run.sh's up-front\n"
        f"`devm install` didn't run (running pytest directly?), or a\n"
        f"previous test uninstalled the daemon without restoring it.\n\n"
        f"  DEVM_BIN            = {devm_path}\n"
        f"  daemon program path = {current_program!r}\n"
        f"  plist exists        = {_LAUNCH_DAEMON_PLIST.exists()}\n\n"
        f"Fix by re-running the full suite via `just e2e-devm` (which\n"
        f"invokes run.sh and does the pre-install), or manually run\n"
        f"`{devm_path} install` before invoking pytest.",
        returncode=2,
    )


# Kept for backward compatibility with tests that still list
# `devm_installed` as a param — it's now a session-level guarantee,
# so per-test it's a no-op.
@pytest.fixture
def devm_installed(_daemon_matches_devm_bin):
    yield


@pytest.fixture
def tart_sandbox(devm, sandbox_name, workspace) -> TartSandbox:
    """Cold-starts the project VM via `devm shell -- true` (a no-op command
    that triggers cold-start + provisioning then exits).

    Tests that need to run commands inside the VM use the returned
    TartSandbox handle. Teardown is automatic via the existing
    `sandbox_name` fixture's registry cleanup — but tests can also
    call `devm.teardown(yes=True)` explicitly to verify teardown
    behavior."""
    subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path), timeout=300,
    )
    # We don't fail on non-zero; some tests may want to assert on
    # cold-start failures themselves. The fixture just gives them a
    # handle to inspect state.

    yield TartSandbox(name=sandbox_name)


@pytest.fixture
def restart_isolated_daemon(devm_path) -> Callable[[], None]:
    """Callable that kills and relaunches the isolated `devm serve
    --foreground` daemon in place, against the same DEVM_RUNTIME_DIR /
    DEVM_DNS_ADDR run.sh already exported.

    Only meaningful in isolated e2e mode (`E2E_ISOLATE=1`) — there's no
    launchd/sudo involved, so a plain kill+respawn stands in for what
    `devm service restart` does in install mode (see test_44/test_73).
    Relaunching re-runs the daemon's startup adoption path
    (DiscoverIronProxies + Supervisor.Adopt), which is what puts a
    still-running iron-proxy into the *adopted* state — per a verified
    spike, adopted proxies are NOT auto-restarted by the supervisor if
    they later die, unlike a freshly-spawned one. Skips (not fails)
    when not in isolated mode, since it has nothing to act on there.
    """
    if os.environ.get("E2E_ISOLATE") != "1":
        pytest.skip("restart_isolated_daemon requires E2E_ISOLATE=1")

    runtime_dir = os.environ.get("DEVM_RUNTIME_DIR")
    if not runtime_dir:
        pytest.skip("DEVM_RUNTIME_DIR not set (expected in isolated mode)")
    pid_file = Path(runtime_dir) / "daemon.pid"
    log_file = Path(runtime_dir) / "daemon.log"

    def _daemon_running() -> bool:
        r = subprocess.run(
            [devm_path, "status", "--json"],
            capture_output=True, timeout=10,
        )
        if r.returncode != 0:
            return False
        try:
            body = json.loads(r.stdout.decode())
        except ValueError:
            return False
        return body.get("daemon", {}).get("running") is True

    def _restart() -> None:
        old_pid_text = pid_file.read_text().strip() if pid_file.exists() else ""
        if old_pid_text:
            old_pid = int(old_pid_text)
            try:
                os.kill(old_pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                try:
                    os.kill(old_pid, 0)
                except ProcessLookupError:
                    break
                time.sleep(0.2)
            else:
                try:
                    os.kill(old_pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass

        # Relaunch against the SAME DEVM_RUNTIME_DIR/DEVM_DNS_ADDR — both
        # already exported into this process's environ by run.sh, and
        # subprocess.Popen inherits the current environ by default.
        with open(log_file, "a", encoding="utf-8") as log_f:
            log_f.write("\n=== e2e: restart_isolated_daemon relaunching ===\n")
            log_f.flush()
            proc = subprocess.Popen(
                [devm_path, "serve", "--foreground"],
                stdout=log_f, stderr=subprocess.STDOUT,
            )
        # Overwrite daemon.pid so both this fixture (on a second call)
        # and run.sh's isolated on_exit trap reap the NEW pid instead
        # of the one we just killed.
        pid_file.write_text(str(proc.pid))

        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if _daemon_running():
                return
            if proc.poll() is not None:
                raise RuntimeError(
                    f"relaunched isolated daemon exited early (rc={proc.returncode}); "
                    f"see {log_file}"
                )
            time.sleep(0.2)
        raise RuntimeError(
            f"relaunched isolated daemon never reported running; see {log_file}"
        )

    return _restart


@pytest.fixture(scope="session")
def inspector_vm() -> Iterator[TartSandbox]:
    """Clone cirruslabs/debian to a session-shared VM and boot it.

    Read-only tart contract tests share this VM to avoid 30-60s of
    clone+boot per test. Lifecycle tests (clone, pull, run, delete)
    create their own VMs with unique names.
    """
    import platform
    if platform.system() != "Darwin":
        pytest.skip("tart contract tests run on macOS only")
    if shutil.which("tart") is None:
        pytest.skip("tart not on PATH")

    name = f"inspect-{secrets.token_hex(2)}"
    template = "ghcr.io/cirruslabs/debian:latest"

    registry.append("sandbox", name)
    try:
        subprocess.run(["tart", "pull", template], check=True, timeout=300)
        subprocess.run(["tart", "clone", template, name], check=True, timeout=60)

        proc = subprocess.Popen(
            ["tart", "run", "--no-graphics", name],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
        try:
            vm = TartSandbox(name=name)
            assert vm.wait_running(timeout=120), f"{name} never reached running"
            for _ in range(60):
                if vm.ip():
                    break
                time.sleep(1)
            else:
                raise RuntimeError(f"{name} never got an IP")
            yield vm
        finally:
            subprocess.run(["tart", "stop", name], capture_output=True, timeout=30)
            proc.wait(timeout=30)
    finally:
        subprocess.run(["tart", "delete", name], capture_output=True, timeout=10)
        registry.remove("sandbox", name)


@pytest.fixture
def phase():
    """Helper for sub-test phase timing.

        phase("cold-start-1")
        ...
        phase("reconcile")

    Prints `[phase] <label>  Xs (total Ys)` to stdout. Opt-in per
    test; intended for the slow recreate tests where breakdowns matter.
    """
    start = [time.monotonic()]
    last = [time.monotonic()]

    def mark(label: str) -> None:
        now = time.monotonic()
        delta = now - last[0]
        total = now - start[0]
        print(f"[phase] {label:<24} {delta:6.1f}s   (total {total:.1f}s)")
        last[0] = now

    return mark
