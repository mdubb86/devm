"""249: adding a dotfile-rooted volume (e.g. /home/devm/.codex) live
via `devm reconcile` derives the sanitized label ("codex") in every
code path — schema validation, mutagen session setup, AND the live
reconcile apply — so the Mac-side mirror lands at
<RuntimeDir>/<project>/codex, never <...>/.codex.

Pins the shape shelfmates hit in v0.23.3: a second copy of
resolveVolumeLabel in internal/reconcile/apply_live.go used raw
filepath.Base(v.Path) and drifted from the sanitizing version in
serviceapi/mutagen_sessions.go. The live-add path wrote storage to
<project>/.codex before erroring — had the session come up under
that label, adopting an empty (hidden) directory would have made the
guest's credential the only copy. Both writable and readable through
this bug because dotfile-rooted volumes are exactly where credential
mounts live (~/.claude, ~/.codex, etc.).

The v0.22.2 fix consolidated derivation into schema.SanitizeDerivedLabel
but only for the cold-start path; the reconcile duplicate was
overlooked. This test locks the fix by adding a dotfile volume LIVE
(not at cold-start), so a future drift-copy of the label logic that
skips the sanitize would fail here.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
@pytest.mark.slow
def test_dotfile_volume_live_add_mirror_path_is_sanitized(devm, workspace, sandbox_name):
    workspace.write_devmyaml(no_repo=True)
    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=240,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Add a dotfile-rooted volume live. Under the pre-fix v0.23.3
        # reconcile path, this would have derived label ".codex" and
        # created the Mac mirror at <runtime>/<project>/.codex; mutagen
        # then refused the invalid session name, but not before the
        # mirror dir existed on disk — a subsequent successful adoption
        # would have used that empty hidden dir as authoritative.
        devm.unlock()
        workspace.patch_devmyaml(volumes={"codex": "/home/devm/.codex"})
        devm.approve()
        r = devm.reconcile(yes=True, timeout=120, check=False)
        assert r.returncode == 0, (
            f"live reconcile add of dotfile volume failed (rc={r.returncode}):\n"
            f"stdout: {r.stdout.decode()!r}\n"
            f"stderr: {r.stderr.decode()!r}"
        )

        # The Mac mirror must live at the SANITIZED label — "codex",
        # not ".codex". This is the whole point of the fix.
        sanitized_mirror = mirror_path(workspace.vm_name, "codex")
        assert sanitized_mirror.is_dir(), (
            f"Mac mirror must land at the sanitized label path "
            f"{sanitized_mirror} — the reconcile-live label derivation "
            "must run through SanitizeDerivedLabel like the cold-start path does."
        )

        # And the un-sanitized path MUST NOT exist. A drift copy of the
        # label logic that skipped sanitize would land the mirror here.
        dot_mirror = mirror_path(workspace.vm_name, ".codex")
        assert not dot_mirror.exists(), (
            f"Mac mirror leaked at unsanitized dotfile path {dot_mirror} — "
            "reconcile.apply_live's label derivation must not use raw "
            "filepath.Base(v.Path); use v.ResolveLabel() (single source of truth)."
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
