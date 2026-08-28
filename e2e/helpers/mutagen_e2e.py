"""Shared helpers for e2e tests that inspect mutagen session state
directly: Mac-side mirror paths, generated session config paths,
session naming, and `mutagen sync <verb>` invocations against the e2e
identity's mutagen data dir.

These duplicate the Go-side formulas (rather than shelling out to a
devm subcommand) so a test can independently verify the daemon did
what it claims — see internal/serviceapi/mutagen_sessions.go
(SessionName, SessionNamePrefix, mirror paths via
internal/serviceapi/volumes.go) and internal/serviceapi/mutagen.go
(mutagenSessionsDir, mutagenDataDir). Any change to those formulas
needs a matching update here.
"""
from __future__ import annotations
import json
import os
import subprocess
from pathlib import Path

RUNTIME_DIR = Path.home() / "Library" / "Application Support" / "devm-e2e"
MUTAGEN_BIN = RUNTIME_DIR / "bin" / "mutagen"
MUTAGEN_DATA_DIR = RUNTIME_DIR / "mutagen" / "data"
MUTAGEN_SESSIONS_DIR = RUNTIME_DIR / "mutagen" / "sessions"


def mirror_path(project_id: str, label: str) -> Path:
    """Mac-side mirror dir for one entity: <RuntimeDir>/<projectID>/<label>."""
    return RUNTIME_DIR / project_id / label


def session_config_path(project_id: str, label: str) -> Path:
    """Generated mutagen sync config yaml for one entity."""
    return MUTAGEN_SESSIONS_DIR / project_id / f"{label}.yml"


def session_name(project_id: str, label: str) -> str:
    return f"devm-{project_id}-{label}"


def session_prefix(project_id: str) -> str:
    return f"devm-{project_id}-"


def _mutagen_env() -> dict[str, str]:
    return {**os.environ, "MUTAGEN_DATA_DIRECTORY": str(MUTAGEN_DATA_DIR)}


def sync_list(name_prefix: str = "") -> list[dict]:
    """Run `mutagen sync list --template '{{json .}}'` against the e2e
    mutagen data dir and return the parsed rows (each a dict with at
    least "identifier", "name", "status"), optionally filtered to
    sessions whose name starts with name_prefix."""
    r = subprocess.run(
        [str(MUTAGEN_BIN), "sync", "list", "--template", "{{json .}}"],
        capture_output=True, text=True, timeout=20, env=_mutagen_env(),
    )
    if r.returncode != 0:
        raise RuntimeError(f"mutagen sync list failed: {r.stderr}")
    stdout = r.stdout.strip()
    rows = json.loads(stdout) if stdout else []
    if name_prefix:
        rows = [row for row in rows if row.get("name", "").startswith(name_prefix)]
    return rows


def sync_flush(session_id: str, timeout: float = 60.0) -> subprocess.CompletedProcess:
    """Run `mutagen sync flush <id>` — blocks until the sync cycle
    completes."""
    return subprocess.run(
        [str(MUTAGEN_BIN), "sync", "flush", session_id],
        capture_output=True, text=True, timeout=timeout, env=_mutagen_env(),
    )
