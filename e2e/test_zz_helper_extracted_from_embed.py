"""Pin the fresh-tarball install case: devm ships as a single binary,
`devm install` must extract the embedded devm-helper next to itself.

Regression pin for the v0.9.1 bug where `devm upgrade` on a
non-brew install ($HOME/.local/bin/devm) emitted:
    note: devm-helper binary not found at $HOME/.local/bin/devm-helper;
          skipping network-isolation helper install
Because the release tarball only shipped devm, not devm-helper. The
fix embeds devm-helper into devm and extracts it at install time.

Named `test_zz_…` so it sorts LAST — its finally-block uninstalls
devm-e2e, which trips `_daemon_matches_devm_bin`'s session-abort for
any install-marker test that runs after it (same reason
test_zz_install_uninstall_lifecycle.py sorts last).
"""
from __future__ import annotations

import platform
import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


_DEVM_E2E_HELPER = Path("/usr/local/bin/devm-e2e-helper")
_DEVM_E2E_HELPER_SIDECAR = Path("/usr/local/bin/devm-e2e-helper.sha256")


@pytest.mark.slow
@pytest.mark.timeout(900)  # base image build can take up to 10 min
def test_helper_extracted_from_embed(devm, sudo_capable):
    if platform.system() != "Darwin":
        pytest.skip("install/uninstall lifecycle runs on macOS only")

    # Pre-clean: uninstall, then delete the helper binary + sidecar so
    # the install can't short-circuit on a stale on-disk copy.
    subprocess.run([devm.path, "uninstall"], capture_output=True, timeout=30)
    subprocess.run(
        ["sudo", "rm", "-f", str(_DEVM_E2E_HELPER), str(_DEVM_E2E_HELPER_SIDECAR)],
        capture_output=True, timeout=10,
    )
    assert not _DEVM_E2E_HELPER.exists(), (
        f"pre-condition failed: {_DEVM_E2E_HELPER} still present after rm"
    )

    try:
        # --- INSTALL ---
        # Only devm-e2e is on disk (from `just e2e-bootstrap` OR the
        # session's earlier state). devm-e2e must extract the embedded
        # helper as part of install.
        r = subprocess.run(
            [devm.path, "install"],
            capture_output=True, timeout=780, check=False,
        )
        assert r.returncode == 0, (
            f"install failed:\nstdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )

        # Give launchd a beat to bootstrap the helper daemon.
        time.sleep(1)

        # --- ASSERTIONS ---
        assert _DEVM_E2E_HELPER.exists(), (
            f"install did not extract devm-helper to {_DEVM_E2E_HELPER}"
        )
        assert _DEVM_E2E_HELPER_SIDECAR.exists(), (
            f"install did not write sha256 sidecar at {_DEVM_E2E_HELPER_SIDECAR}"
        )
        mode = _DEVM_E2E_HELPER.stat().st_mode & 0o777
        assert mode == 0o755, (
            f"devm-helper mode is {oct(mode)}, expected 0o755"
        )

        # The sidecar records the sha256 of the gzipped embed blob, not
        # the extracted binary, so we can't cheaply re-derive it here
        # without shelling into devm to print helper.EmbeddedSha256.
        # Settle for: the sidecar exists and is 64 hex chars.
        sidecar_content = _DEVM_E2E_HELPER_SIDECAR.read_text().strip()
        assert len(sidecar_content) == 64, (
            f"sidecar content is {len(sidecar_content)} chars, expected 64 (hex sha256)"
        )
        int(sidecar_content, 16)  # must parse as hex; raises ValueError otherwise

        # The on-disk helper must be a valid Mach-O executable — a
        # cheap validity check that catches "we wrote garbage."
        r_file = subprocess.run(
            ["file", str(_DEVM_E2E_HELPER)],
            capture_output=True, text=True, timeout=5,
        )
        assert "Mach-O" in r_file.stdout, (
            f"devm-helper is not a Mach-O binary: {r_file.stdout!r}"
        )
    finally:
        # Clean up so the next install-marker test has the runtime dir
        # torn down. Same finally-block pattern as
        # test_zz_install_uninstall_lifecycle.py.
        subprocess.run(
            [devm.path, "uninstall"],
            capture_output=True, timeout=30,
        )
