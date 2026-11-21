"""Pin: mutagen's --configuration-file YAML schema. devm renders this
file per-session (internal/mutagen/config_file.go) and passes it to
`mutagen sync create --configuration-file <path>`.

Mutagen's strict-decoder rejects unknown top-level keys, so any drift
between what devm writes and what mutagen accepts fails loud AT session
create — better than silent misconfiguration.

Actual shape mutagen 0.18.1 accepts:

  sync:
    defaults:
      mode: two-way-resolved
      scanMode: accelerated
      ignore:
        vcs: false
        paths:
          - "**/node_modules/"

Two-part pin:
  (a) The known-good shape above IS accepted.
  (b) The known-BAD shape devm shipped last iteration (top-level
      `version:` + top-level `ignores:`, `mode` as sibling of `defaults`)
      is REJECTED with a schema-decode error.

The BAD-shape test doubles as the regression pin for the config-file
rewrite that already landed.
"""
from __future__ import annotations

import shutil
import tempfile
from pathlib import Path

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


GOOD_YAML = """\
sync:
  defaults:
    mode: two-way-resolved
    scanMode: accelerated
    ignore:
      vcs: false
      paths:
        - "**/node_modules/"
"""


BAD_YAML_WITH_VERSION_AND_TOP_IGNORES = """\
version: 1
sync:
  defaults:
    ignore:
      vcs: false
    scanMode: accelerated
  mode: two-way-resolved
ignores:
  - "**/node_modules/"
"""


def _make_endpoints() -> tuple[Path, Path]:
    a = Path(tempfile.mkdtemp(prefix="mut-alpha-", dir="/tmp"))
    b = Path(tempfile.mkdtemp(prefix="mut-beta-", dir="/tmp"))
    return a, b


def test_config_file_good_shape_accepted():
    alpha, beta = _make_endpoints()
    cfg = Path(tempfile.mkstemp(suffix=".yml", prefix="mut-good-", dir="/tmp")[1])
    cfg.write_text(GOOD_YAML)
    try:
        with mc.daemon() as data_dir:
            r = mc.run(
                [
                    "sync", "create",
                    "--configuration-file", str(cfg),
                    "--name", "cfg-good",
                    str(alpha), str(beta),
                ],
                data_dir=data_dir,
            )
            assert r.returncode == 0, (
                f"the shape devm renders today must be accepted. "
                f"stderr={r.stderr!r}"
            )
    finally:
        shutil.rmtree(alpha, ignore_errors=True)
        shutil.rmtree(beta, ignore_errors=True)
        cfg.unlink(missing_ok=True)


def test_config_file_bad_shape_rejected_at_decode():
    alpha, beta = _make_endpoints()
    cfg = Path(tempfile.mkstemp(suffix=".yml", prefix="mut-bad-", dir="/tmp")[1])
    cfg.write_text(BAD_YAML_WITH_VERSION_AND_TOP_IGNORES)
    try:
        with mc.daemon() as data_dir:
            r = mc.run(
                [
                    "sync", "create",
                    "--configuration-file", str(cfg),
                    "--name", "cfg-bad",
                    str(alpha), str(beta),
                ],
                data_dir=data_dir,
                check=False,
            )
            assert r.returncode != 0, (
                f"the BAD shape (top-level `version:` and `ignores:`) must be "
                f"REJECTED — that's the regression pin for the config-file "
                f"rewrite. stderr={r.stderr!r}"
            )
            # Any of these strings in stderr confirms it was rejected at
            # yaml-decode, not at some other point.
            assert (
                "field version not found" in r.stderr
                or "field ignores not found" in r.stderr
                or "unable to unmarshal" in r.stderr
            ), (
                f"expected a yaml-decode error naming one of the bad "
                f"top-level fields; got: {r.stderr!r}"
            )
    finally:
        shutil.rmtree(alpha, ignore_errors=True)
        shutil.rmtree(beta, ignore_errors=True)
        cfg.unlink(missing_ok=True)
