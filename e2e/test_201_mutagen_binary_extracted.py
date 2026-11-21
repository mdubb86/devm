"""201: the embedded mutagen binary is extracted correctly, with a
correct sidecar sha.

Task 1's `mutagen.Ensure` (internal/mutagen/path.go) extracts the
embedded, gzipped v0.18.1 binary to `<runtimeDir>/bin/mutagen` (mode
0755) and writes a `<same-path>.sha256` sidecar next to it, so future
daemon starts can skip the re-extract when the sidecar already matches
the embedded blob. `AdoptMutagenDaemon` calls `Ensure` on every devm
daemon startup, before spawning/adopting the mutagen daemon.

Note on the sidecar's contents: it is the sha256 of the *compressed*
embedded blob (`internal/mutagen/embed/mutagen.gz`), NOT of the
decompressed on-disk binary — see `internal/mutagen/embed.go`'s
`EmbeddedSha256()` and `path_test.go`'s
`TestEnsure_ExtractsBinaryAndSidecar`. So this test pins the sidecar
against the repo's checked-in embed blob (the actual identity
`Ensure` idempotence keys off), not against a hash of the extracted
binary — hashing the extracted binary and comparing it to the sidecar
would be asserting something the implementation never promises.

Pins:
  - `~/Library/Application Support/devm-e2e/bin/mutagen` exists, mode 0755.
  - Its `.sha256` sidecar exists and is a 64-char lowercase hex string.
  - That sidecar matches sha256(internal/mutagen/embed/mutagen.gz) from
    this checkout — i.e., the running e2e daemon's extracted binary
    really came from the currently-committed embed, not a stale one.
  - The extracted binary reports version 0.18.1.
"""
from __future__ import annotations

import hashlib
import os
import stat
import subprocess
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm

_BIN_PATH = os.path.expanduser("~/Library/Application Support/devm-e2e/bin/mutagen")
_SIDECAR_PATH = _BIN_PATH + ".sha256"
_EMBED_BLOB = Path(__file__).resolve().parents[1] / "internal" / "mutagen" / "embed" / "mutagen.gz"


@pytest.mark.timeout(60)
def test_mutagen_binary_extracted(devm_path, devm_installed):
    subprocess.run([devm_path, "status"], capture_output=True, timeout=20)

    assert os.path.exists(_BIN_PATH), f"mutagen binary not extracted at {_BIN_PATH}"
    mode = stat.S_IMODE(os.stat(_BIN_PATH).st_mode)
    assert mode == 0o755, f"mutagen binary mode = {oct(mode)}, expected 0755"

    assert os.path.exists(_SIDECAR_PATH), f"sidecar sha missing at {_SIDECAR_PATH}"
    sidecar_sha = Path(_SIDECAR_PATH).read_text().strip()
    assert len(sidecar_sha) == 64 and all(c in "0123456789abcdef" for c in sidecar_sha), (
        f"sidecar sha not a 64-char lowercase hex string: {sidecar_sha!r}"
    )

    assert _EMBED_BLOB.exists(), f"repo embed blob not found at {_EMBED_BLOB}"
    expected_sha = hashlib.sha256(_EMBED_BLOB.read_bytes()).hexdigest()
    assert sidecar_sha == expected_sha, (
        f"sidecar sha {sidecar_sha!r} does not match sha256 of the checked-in "
        f"embed blob {expected_sha!r} — the running e2e daemon's extracted "
        f"mutagen came from a different build than this checkout."
    )

    r = subprocess.run([_BIN_PATH, "version"], capture_output=True, text=True, timeout=10)
    assert r.returncode == 0, f"mutagen version failed:\nstdout={r.stdout}\nstderr={r.stderr}"
    combined = r.stdout + r.stderr
    assert "0.18.1" in combined, f"expected version 0.18.1 in output: {combined!r}"
