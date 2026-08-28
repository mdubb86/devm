"""Helpers for mutagen contract tests.

Each contract test spawns its own mutagen daemon on a fresh, temporary
DataDir so tests can't cross-contaminate state. The daemon is a child of
the pytest process — we start it via `mutagen daemon start` and stop it
via `mutagen daemon stop` inside a context manager. No devm daemon
involved; contract tests pin mutagen's own CLI behavior.

The binary path is `<repo>/bin/mutagen` (produced by `just fetch-mutagen`)
so contract tests don't depend on a devm-e2e bootstrap. This mirrors
`bin/iron-proxy` used by the iron-contract suite.
"""
from __future__ import annotations

import contextlib
import json
import os
import subprocess
from collections.abc import Iterator
from pathlib import Path


def binary_path() -> Path:
    """Return the path to bin/mutagen, or raise if it hasn't been staged.

    Not tolerated: falling back to a system mutagen or a devm-installed
    one. Contract tests pin behavior of the SPECIFIC mutagen version devm
    embeds — running against a different version silently changes what's
    tested. `just fetch-mutagen` is the only supported source.
    """
    here = Path(__file__).resolve().parent.parent.parent  # repo root
    candidate = here / "bin" / "mutagen"
    if not candidate.exists():
        raise RuntimeError(
            f"mutagen binary not found at {candidate}. "
            f"Run `just fetch-mutagen` first — contract tests need a "
            f"repo-local mutagen, not the devm-installed one."
        )
    return candidate


def agents_bundle_path() -> Path:
    """Path to mutagen-agents.tar.gz staged next to bin/mutagen."""
    return binary_path().parent / "mutagen-agents.tar.gz"


def env_for(data_dir: Path) -> dict[str, str]:
    """Env dict that isolates a mutagen invocation to data_dir. Callers
    that need to override HOME or add other vars should merge on top."""
    return {**os.environ, "MUTAGEN_DATA_DIRECTORY": str(data_dir)}


def run(
    args: list[str],
    data_dir: Path,
    env: dict[str, str] | None = None,
    check: bool = True,
    timeout: float = 15.0,
) -> subprocess.CompletedProcess:
    """Run `mutagen <args>` against data_dir. Convenience wrapper around
    subprocess.run — passes MUTAGEN_DATA_DIRECTORY and defaults to
    check=True so a non-zero exit raises with captured stderr.

    `start_new_session=True` is REQUIRED for `daemon start` to actually
    spawn the daemon: mutagen fork/execs `mutagen daemon run` and detaches
    it into a new process group, and if the CLI itself doesn't already
    live in one, the detachment silently fails — daemon start returns 0
    but writes nothing to the DataDir. Every other subcommand is unaffected,
    but it's cheaper to set it uniformly than to remember which subcommand
    needs it.
    """
    proc_env = env_for(data_dir)
    if env:
        proc_env.update(env)
    return subprocess.run(
        [str(binary_path()), *args],
        capture_output=True,
        text=True,
        timeout=timeout,
        env=proc_env,
        check=check,
        start_new_session=True,
    )


def sync_list(data_dir: Path) -> list[dict]:
    """Return the full JSON payload of `mutagen sync list` — every field
    the template exposes. Contract tests use this to pin the exact field
    names and types in the JSON output (e.g. `status` vs `paused`)."""
    r = run(
        ["sync", "list", "--template", "{{json .}}"],
        data_dir=data_dir,
    )
    stdout = r.stdout.strip()
    return json.loads(stdout) if stdout else []


@contextlib.contextmanager
def short_data_dir() -> Iterator[Path]:
    """Yield an isolated MUTAGEN_DATA_DIRECTORY on a SHORT filesystem path.

    Unix domain sockets on macOS have a hard path-length ceiling of 104
    bytes (SUN_PATH). Mutagen's daemon binds `<DataDir>/daemon/daemon.sock`
    and silently fails to bind (daemon exits 0, socket never appears) if
    the full path overflows. Pytest's tmp_path lives under
    `/private/var/folders/…/pytest-of-<user>/pytest-N/<test-name>/`,
    which routinely exceeds 100 bytes before adding the daemon.sock leaf.

    Use `mkdtemp` under `/tmp` (`/private/tmp/`) directly so the base
    path is short enough that daemon.sock fits. Caller is responsible
    for cleanup on exit.
    """
    import shutil
    import tempfile
    d = Path(tempfile.mkdtemp(prefix="mut-contract-", dir="/tmp"))
    try:
        yield d
    finally:
        shutil.rmtree(d, ignore_errors=True)


@contextlib.contextmanager
def daemon() -> Iterator[Path]:
    """Yield a live mutagen daemon on a fresh, isolated, short-path DataDir.

    Starts the daemon via `mutagen daemon start` and stops it via
    `mutagen daemon stop` on exit — no matter what the test does. If
    daemon stop fails (e.g. the daemon already died), swallow — the
    DataDir is torn down anyway.
    """
    with short_data_dir() as data_dir:
        run(["daemon", "start"], data_dir=data_dir)
        try:
            yield data_dir
        finally:
            try:
                run(["daemon", "stop"], data_dir=data_dir, check=False, timeout=10)
            except Exception:  # noqa: BLE001
                pass
