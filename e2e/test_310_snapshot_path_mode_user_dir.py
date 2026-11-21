"""310: declarative path-mode snapshot round-trip on a user-owned dir.

Declares a single `claude` snapshot entry with `path: /home/devm/.claude`
and NO `backup_claude`/`restore_claude` hooks in devm.sh — path mode is
fully declarative, devm drives the tar pipeline end to end.

Flow:
  1. Cold-start with `snapshots.claude.path: /home/devm/.claude`.
  2. Seed `/home/devm/.claude/sessions/s1` with known content at mode 0600.
  3. `devm snapshot create claude` — captures via the TarRunner.
  4. Mutate: overwrite s1's content and chmod to 0644.
  5. `devm snapshot restore claude` — restores via the TarRunner.
  6. Assert content (sha256) and mode (`%a`) match the pre-mutation values.

Pins that the declarative path-mode pipeline (tar + zstd) preserves
file bytes and the stored mode on a round trip without any user hooks.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_snapshot_path_mode_user_dir(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        no_repo=True,
        snapshots={
            "claude": {"path": "/home/devm/.claude"},
        },
    )
    # Path mode is fully declarative — no backup_claude/restore_claude.
    # devm.sh is still written so `set -eo pipefail` baseline matches the
    # rest of the suite and nothing surprises the install phase.
    workspace.write_devm_sh("#!/usr/bin/env bash\nset -eo pipefail\n")

    sandbox = TartSandbox(name=sandbox_name)

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=600,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    # Seed the user-owned directory with a marker file at mode 0600.
    r = sandbox.exec_shell(
        "install -d -m 0755 -o devm -g devm /home/devm/.claude/sessions && "
        "echo hello > /home/devm/.claude/sessions/s1 && "
        "chmod 0600 /home/devm/.claude/sessions/s1"
    )
    assert r.exit_code == 0, f"seed failed: {r.stderr}"

    # Capture pre-mutation content hash and mode.
    r = sandbox.exec_shell("sha256sum /home/devm/.claude/sessions/s1")
    assert r.exit_code == 0, f"pre-hash failed: {r.stderr}"
    before_hash = r.stdout.split()[0]

    r = sandbox.exec_shell("stat -c '%a' /home/devm/.claude/sessions/s1")
    assert r.exit_code == 0, f"pre-mode stat failed: {r.stderr}"
    before_mode = r.stdout.strip()
    assert before_mode == "600", f"seed mode wrong: got {before_mode!r}"

    # Snapshot create — fully declarative, no hooks involved.
    devm.snapshot("create", "claude", timeout=180)

    # Mutate the file: change contents AND permissions.
    r = sandbox.exec_shell(
        "echo mutated > /home/devm/.claude/sessions/s1 && "
        "chmod 0644 /home/devm/.claude/sessions/s1"
    )
    assert r.exit_code == 0, f"mutate failed: {r.stderr}"

    # Confirm the mutation actually took — otherwise a no-op "restore"
    # would pass vacuously.
    r = sandbox.exec_shell("sha256sum /home/devm/.claude/sessions/s1")
    assert r.exit_code == 0, f"post-mutate hash failed: {r.stderr}"
    assert r.stdout.split()[0] != before_hash, "mutation did not change content"
    r = sandbox.exec_shell("stat -c '%a' /home/devm/.claude/sessions/s1")
    assert r.exit_code == 0 and r.stdout.strip() == "644", (
        f"mutation did not change mode: {r.stdout!r}"
    )

    # Restore — pipes the archive through TarRunner's in-guest tar -x
    # with --same-permissions.
    devm.snapshot("restore", "claude", timeout=180)

    # Content and mode must both be back to the pre-mutation values.
    r = sandbox.exec_shell("sha256sum /home/devm/.claude/sessions/s1")
    assert r.exit_code == 0, f"post-restore hash failed: {r.stderr}"
    after_hash = r.stdout.split()[0]
    assert after_hash == before_hash, (
        f"content not restored: before={before_hash} after={after_hash}"
    )

    r = sandbox.exec_shell("stat -c '%a' /home/devm/.claude/sessions/s1")
    assert r.exit_code == 0, f"post-restore mode stat failed: {r.stderr}"
    after_mode = r.stdout.strip()
    assert after_mode == before_mode, (
        f"mode not restored: before={before_mode} after={after_mode}"
    )
