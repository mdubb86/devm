"""Crash-safe cleanup registry shared with the bash wrapper.

Each fixture appends to the registry BEFORE creating its resource so
the sweep in run.sh can clean up anything fixtures didn't get to
(pytest SIGKILL, wedged worker, etc.). Format: one line per
resource, "<kind>\\t<value>\\n". Kinds: sandbox, workspace.

remove() verifies the resource is actually gone before dropping the
registry entry — if a fixture says "I cleaned up" but the VM/workspace
is still there, it force-cleans and prints a WARN. This is the
lie-detector for tests/fixtures that skip real teardown.
"""
import json
import os
import shutil
import subprocess
import sys
import threading

_lock = threading.Lock()  # serialises remove() within one process


def _path() -> str | None:
    return os.environ.get("E2E_REGISTRY") or None


def append(kind: str, value: str) -> None:
    """Append a registry entry. No-op if E2E_REGISTRY is unset."""
    p = _path()
    if p is None:
        return
    # O_APPEND single-line writes are atomic across pytest-xdist workers.
    with open(p, "a", encoding="utf-8") as f:
        f.write(f"{kind}\t{value}\n")


def remove(kind: str, value: str) -> None:
    """Remove a registry entry after verifying the resource is gone.

    If the resource is still there, force-clean it and print a WARN —
    the fixture that appended this entry didn't actually tear down.
    Leaks like this are how our tart list ended up with 200+ orphans.
    """
    _verify_gone_or_force_clean(kind, value)

    p = _path()
    if p is None or not os.path.exists(p):
        return
    target = f"{kind}\t{value}"
    with _lock:
        with open(p, "r", encoding="utf-8") as f:
            lines = f.read().splitlines()
        kept = [line for line in lines if line.strip() and line != target]
        with open(p, "w", encoding="utf-8") as f:
            for line in kept:
                f.write(line + "\n")


def _verify_gone_or_force_clean(kind: str, value: str) -> None:
    if kind == "sandbox":
        _verify_sandbox_gone(value)
    elif kind == "workspace":
        _verify_workspace_gone(value)


def _verify_sandbox_gone(name: str) -> None:
    if not shutil.which("tart"):
        return
    r = subprocess.run(
        ["tart", "list", "--format=json"],
        capture_output=True, timeout=10,
    )
    if r.returncode != 0:
        return
    try:
        vms = json.loads(r.stdout)
    except Exception:
        return
    for vm in vms:
        if vm.get("Name") == name:
            sys.stderr.write(
                f"WARN: registry.remove(sandbox={name!r}): VM still exists "
                f"after fixture teardown — forcing delete. The test or a "
                f"fixture that appended this entry did not actually clean up.\n"
            )
            subprocess.run(["tart", "stop", name], capture_output=True, timeout=30)
            subprocess.run(["tart", "delete", name], capture_output=True, timeout=15)
            return


def _verify_workspace_gone(path: str) -> None:
    if os.path.exists(path):
        sys.stderr.write(
            f"WARN: registry.remove(workspace={path!r}): path still exists "
            f"after fixture teardown — forcing rmtree.\n"
        )
        shutil.rmtree(path, ignore_errors=True)
