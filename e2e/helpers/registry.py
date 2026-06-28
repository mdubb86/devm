"""Crash-safe cleanup registry shared with the bash wrapper.

Each fixture appends to the registry BEFORE creating its resource so
the sweep in run.sh can clean up anything fixtures didn't get to
(pytest SIGKILL, wedged worker, etc.). Format: one line per
resource, "<kind>\\t<value>\\n". Kinds: sandbox, workspace.
"""
import os
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
    """Remove a registry entry. No-op if E2E_REGISTRY is unset or file missing."""
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
