"""186: pop temp-session for out-of-mirror paths.

Covers:
- File-scope: sibling file is NOT synced (ignore filter works).
- Dir-scope: full contents sync; live-edit propagates.
- Re-pop reuses the same session.
- GC + lifecycle sweep (requires tuned env in daemon plist).
- Mac CLI fallback opens the same MacDir target.

The GC test requires the e2e daemon to have been booted with
DEVM_POP_SESSION_TTL_SECONDS and DEVM_POP_SESSION_GC_INTERVAL_SECONDS
set in its launchd plist (com.devm.e2e.service.plist). `just
e2e-bootstrap` does not currently inject these; until it does, the
test skips cleanly rather than waiting out the real 1h default.
"""
from __future__ import annotations

import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


def _pop_scratch_root() -> Path:
    # Identity: e2e — matches identity.E2E.RuntimeDir().
    return Path.home() / "Library" / "Application Support" / "devm-e2e" / "pop-tmp"


def _pop_sessions() -> list[Path]:
    """Live pop-session directories, ignoring macOS metadata (`.DS_Store`
    and friends Finder / Spotlight may drop into the parent scratch dir
    when it's under ~/Library/Application Support). Sessions are always
    directories (`<id>/`); filtering to `is_dir()` keeps the count
    stable across re-runs of the same test."""
    root = _pop_scratch_root()
    if not root.exists():
        return []
    return [p for p in root.iterdir() if p.is_dir()]


def _wait_for(cond, timeout: float, interval: float = 0.25) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if cond():
            return True
        time.sleep(interval)
    return False


@pytest.mark.timeout(300)
def test_pop_file_outside_mirror_syncs_and_isolates_siblings(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path), capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
    try:
        # Guest-side: two siblings.
        r = subprocess.run(
            [devm.path, "exec", "--", "bash", "-c",
             "mkdir -p /tmp/site && echo hello > /tmp/site/index.html && echo secret > /tmp/site/secret.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, r.stderr.decode()
        # Pop the single file — file-kind session.
        r = subprocess.run(
            [devm.path, "exec", "--", "pop", "/tmp/site/index.html"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()

        # Mac-side: exactly one pop-tmp session dir; contains index.html
        # with matching bytes; does NOT contain secret.txt.
        assert _wait_for(lambda: bool(_pop_sessions()), 10)
        sessions = _pop_sessions()
        assert len(sessions) == 1
        sdir = sessions[0]
        assert _wait_for(lambda: (sdir / "index.html").exists(), 15)
        assert (sdir / "index.html").read_bytes().rstrip() == b"hello"
        assert not (sdir / "secret.txt").exists(), (
            "file-scope session must not sync siblings (ignore filter '**' + '!index.html')"
        )

        # Live update: modify guest-side, observe Mac copy update.
        r = subprocess.run(
            [devm.path, "exec", "--", "bash", "-c", "echo updated > /tmp/site/index.html"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert _wait_for(lambda: (sdir / "index.html").read_bytes().rstrip() == b"updated", 15), \
            "live guest edit should propagate to Mac copy"

        # devm status includes the pop-session line.
        st = subprocess.run(
            [devm.path, "status"], cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert "Pop sessions: 1 active" in st.stdout.decode()
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), timeout=60, capture_output=True)


@pytest.mark.timeout(300)
def test_pop_dir_outside_mirror_syncs_contents(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path), capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
    try:
        r = subprocess.run(
            [devm.path, "exec", "--", "bash", "-c",
             "mkdir -p /tmp/site && echo a > /tmp/site/a.txt && echo b > /tmp/site/b.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, r.stderr.decode()
        r = subprocess.run(
            [devm.path, "exec", "--", "pop", "/tmp/site/"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()

        assert _wait_for(lambda: bool(_pop_sessions()), 10)
        sessions = _pop_sessions()
        assert len(sessions) == 1
        sdir = sessions[0]
        assert _wait_for(lambda: (sdir / "a.txt").exists() and (sdir / "b.txt").exists(), 15)

        # Live edit propagates.
        r = subprocess.run(
            [devm.path, "exec", "--", "bash", "-c", "echo a2 > /tmp/site/a.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert _wait_for(lambda: (sdir / "a.txt").read_bytes().rstrip() == b"a2", 15)
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), timeout=60, capture_output=True)


@pytest.mark.timeout(300)
def test_pop_repop_reuses_session(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path), capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
    try:
        r = subprocess.run(
            [devm.path, "exec", "--", "bash", "-c", "mkdir -p /tmp/x && echo one > /tmp/x/f.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, r.stderr.decode()
        for _ in range(2):
            r = subprocess.run(
                [devm.path, "exec", "--", "pop", "/tmp/x/f.txt"],
                cwd=str(workspace.path), capture_output=True, timeout=60,
            )
            assert r.returncode == 0, r.stderr.decode()

        assert _wait_for(lambda: bool(_pop_sessions()), 10)
        sessions = _pop_sessions()
        assert len(sessions) == 1, (
            f"re-pop of same path should reuse the same session; got {[s.name for s in sessions]}"
        )

        # Plant a marker in the session dir before another pop; the
        # second pop must not wipe it (proving the mutagen session was
        # reused, not recreated).
        sdir = sessions[0]
        (sdir / ".marker").write_text("still here")
        r = subprocess.run(
            [devm.path, "exec", "--", "pop", "/tmp/x/f.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert (sdir / ".marker").exists()
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), timeout=60, capture_output=True)


@pytest.mark.timeout(300)
def test_pop_session_gc_and_lifecycle(devm, workspace):
    plist = Path.home() / "Library" / "LaunchAgents" / "com.devm.e2e.service.plist"
    if not (plist.exists() and "DEVM_POP_SESSION_TTL_SECONDS" in plist.read_text()):
        pytest.skip("requires DEVM_POP_SESSION_TTL_SECONDS in daemon plist — see plan task 11")

    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path), capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
    try:
        r = subprocess.run(
            [devm.path, "exec", "--", "bash", "-c", "mkdir -p /tmp/gc && echo hi > /tmp/gc/f.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, r.stderr.decode()
        r = subprocess.run(
            [devm.path, "exec", "--", "pop", "/tmp/gc/f.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert _wait_for(lambda: len(_pop_sessions()) == 1, 15)

        # Touch every 2s for 6s: session survives.
        for _ in range(3):
            r = subprocess.run(
                [devm.path, "exec", "--", "bash", "-c", "echo x >> /tmp/gc/f.txt"],
                cwd=str(workspace.path), capture_output=True, timeout=30,
            )
            assert r.returncode == 0, r.stderr.decode()
            time.sleep(2)
        assert len(_pop_sessions()) == 1

        # Stop touching; wait past TTL (5s) plus GC interval (1s) + slack.
        time.sleep(10)
        assert _wait_for(lambda: not bool(_pop_sessions()), 5), \
            "GC should tear down idle session past TTL"

        # Lifecycle: re-pop, then devm stop, then re-check scratch swept.
        r = subprocess.run(
            [devm.path, "exec", "--", "pop", "/tmp/gc/f.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert _wait_for(lambda: len(_pop_sessions()) == 1, 15)
        r = subprocess.run([devm.path, "stop", "--yes"], cwd=str(workspace.path), capture_output=True, timeout=60)
        assert r.returncode == 0, r.stderr.decode()
        assert _wait_for(lambda: not bool(_pop_sessions()), 5), \
            "devm stop should sweep this project's pop sessions"
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), timeout=60, capture_output=True)


@pytest.mark.timeout(300)
def test_pop_mac_cli_out_of_mirror(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path), capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
    try:
        # Seed a guest file OUTSIDE the mirror.
        r = subprocess.run(
            [devm.path, "exec", "--", "bash", "-c", "mkdir -p /tmp/mac && echo m > /tmp/mac/thing.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, r.stderr.decode()
        r = subprocess.run(
            [devm.path, "pop", "mac", "/tmp/mac/thing.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()

        # Session was created; MacDir contains thing.txt with matching bytes.
        assert _wait_for(lambda: bool(_pop_sessions()), 15)
        sessions = _pop_sessions()
        assert len(sessions) == 1
        sdir = sessions[0]
        assert _wait_for(lambda: (sdir / "thing.txt").exists(), 15)
        assert (sdir / "thing.txt").read_bytes().rstrip() == b"m"
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), timeout=60, capture_output=True)
