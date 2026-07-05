"""Shared fixtures for the devm e2e suite.

The fixtures register cleanup intent in the env-shared registry file
BEFORE creating any resource. If a fixture's finalizer doesn't run
(pytest SIGKILL, wedged worker), the bash wrapper's EXIT trap sweeps
the registry. See internal design notes.
"""
from __future__ import annotations
import os
import re
import secrets
import shutil
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Iterator

import pytest

from helpers import Devm, Workspace, registry
from helpers.tart import TartSandbox


def pytest_collection_modifyitems(config, items):
    """Auto-mark tests that must NOT run under pytest-xdist parallelism:

      - `pty`: uses helpers.Shell (pexpect + pty.forkpty). xdist workers
        have a background RPC thread; forkpty in a multi-threaded process
        races on lock inheritance (Python's own DeprecationWarning on
        forkpty spells this out).

      - `serial`: mutates global system state via `devm install` /
        `devm uninstall` (LaunchDaemon plist, /etc/resolver/test, the
        system CA, the tart devm-base image). Concurrent runs of these
        tests step on each other's install/uninstall sequences.

    Both markers land in run.sh's serial phase (`-m "pty or serial"`).
    Detection is a source-grep at collect time; matches beat missed marks
    because the cost of a serial-run non-serial test is only latency, not
    correctness.
    """
    # `serial` marker is for tests that MUTATE the shared daemon during
    # their run — install, uninstall, or service restart — because those
    # kill the socket other tests are actively using. Merely NEEDING the
    # daemon isn't enough; the session-scoped _daemon_matches_devm_bin
    # fixture handles that precondition once at session start.
    _serial_hints = (
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
        if any(h in src for h in _serial_hints):
            item.add_marker(pytest.mark.serial)




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
def workspace(request, sandbox_name) -> Iterator[Workspace]:
    """Temp workspace dir bound to the test's sandbox_name."""
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
        shutil.rmtree(path, ignore_errors=True)
        registry.remove("workspace", str(path))


@pytest.fixture
def sudo_capable():
    """Skips the test if sudo can't realistically prompt the user.

    Skipped conditions:
      - non-macOS (sudo-required tests in this suite are macOS-only)
      - /dev/tty unavailable (CI / no controlling terminal — sudo
        would hang on a prompt it can't deliver)

    If sudo IS capable but not cached, the test will trigger Touch ID
    (or password) prompts naturally during its install/uninstall
    calls — sudo opens /dev/tty directly for the prompt, independent
    of pytest's capture of stdin/stdout. No priming machinery: on
    macOS, Touch ID doesn't share the sudo timestamp cache anyway,
    so priming just adds an extra interaction.
    """
    import platform as _platform

    if _platform.system() != "Darwin":
        pytest.skip("sudo-required test runs on macOS only")
    try:
        open("/dev/tty").close()
    except OSError:
        pytest.skip("no /dev/tty — sudo can't prompt, skipping interactive test")
    _require_sudo_primed()


_LAUNCH_DAEMON_PLIST = Path("/Library/LaunchDaemons/com.devm.service.plist")


def _require_sudo_primed():
    """Fail-fast: sudo credentials must already be cached before a test
    that uses sudo runs. Without this, sudo either tries to prompt
    (works but requires interaction and blocks whichever test noticed
    first) or hangs waiting on a stdin pytest has captured. Either way,
    the user is better off being told UP FRONT to prime sudo.
    """
    r = subprocess.run(
        ["sudo", "-n", "true"],
        capture_output=True, timeout=5,
    )
    if r.returncode != 0:
        pytest.exit(
            "sudo credentials are not cached. This test needs sudo but the\n"
            "cache is cold, and prompting from inside pytest is unreliable\n"
            "(captured stdin, no controlling TTY on some setups). Run:\n"
            "\n"
            "    sudo -v && just e2e-one \"...\"\n"
            "\n"
            "sudo -v primes the credential cache with a single prompt in\n"
            "your interactive shell; subsequent sudo calls inside pytest\n"
            "then use the cache without prompting.",
            returncode=2,
        )


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
    """Precondition run before every test: the LaunchDaemon must be
    installed from DEVM_BIN. Fast path is a single `launchctl print`
    (<100ms) that no-ops when the daemon already matches; slow path
    only fires when a previous test (install/uninstall cycle) left
    the state wrong.

    Every devm test talks to the daemon over the Unix socket — none
    of them get meaningful signal from a stale or absent daemon. This
    makes them all robust to being run in any order (the
    install/uninstall tests leave the host uninstalled at teardown,
    but the next test's autouse re-installs before it runs).

    Contract tests (marked `contract`) don't touch the devm daemon at
    all — they pin behavior of external tools like tart or iron-proxy
    directly. Skip the precondition entirely for them so a stale
    daemon doesn't block an unrelated test from running.

    Skip conditions match `sudo_capable`: no /dev/tty → don't try
    to fix, trust ambient state.
    """
    if request.node.get_closest_marker("contract") is not None:
        yield
        return

    import platform as _platform
    if _platform.system() != "Darwin":
        yield
        return
    try:
        open("/dev/tty").close()
    except OSError:
        yield
        return

    current_program = _daemon_program_path()

    if current_program == devm_path and _LAUNCH_DAEMON_PLIST.exists():
        yield
        return

    # Daemon needs installing or reinstalling. Need sudo — if it isn't
    # primed, skip THIS test (not session-abort). Callers who
    # deliberately opt into sudo work via `sudo_capable` still get the
    # session-abort behavior from `_require_sudo_primed` because that
    # fires in `sudo_capable` before the fixture runs at all.
    check = subprocess.run(
        ["sudo", "-n", "true"], capture_output=True, timeout=5,
    )
    if check.returncode != 0:
        pytest.skip(
            "devm daemon doesn't match DEVM_BIN and sudo cache is cold — "
            "cannot sync. Run `sudo -v && just e2e-devm` (or `bin/devm "
            "install` to install matching bin/devm) to enable this test."
        )

    if current_program is None or not _LAUNCH_DAEMON_PLIST.exists():
        _install_devm(devm_path)
    else:
        _uninstall_devm(devm_path)
        _install_devm(devm_path)
    yield


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
