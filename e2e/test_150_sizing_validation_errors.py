"""150: devm.yaml schema loud-fails for sizing errors.

Covers the three Review Focus loud-fail paths:
  1. Old `docker: true` as a scalar — yaml decode error naming docker.
  2. Typo'd nested key `docker.buildCahe: "15%"` — CheckUnknownKeys
     error naming the typo and listing valid fields.
  3. Malformed value `swap: "abc"` — ParseSizeOrPercent error batched
     from Config.Validate.

Each case asserts that `devm start` exits non-zero with a message that
NAMES the offending field or value in stderr. No silent defaults.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


def _run_start(devm_path, workspace_path):
    return subprocess.run(
        [devm_path, "start"],
        cwd=str(workspace_path),
        capture_output=True,
        timeout=60,
    )


@pytest.mark.timeout(120)
def test_old_docker_bool_fails_loudly(devm, workspace):
    (workspace.path / "devm.yaml").write_text(
        "project:\n"
        "  name: sizing-loud\n"
        "repos:\n"
        "  main:\n"
        "    url: https://github.com/octocat/Hello-World.git\n"
        "docker: true\n"
    )
    r = _run_start(devm.path, workspace.path)
    assert r.returncode != 0, "docker: true as scalar must fail loudly"
    err = r.stderr.decode().lower()
    assert "docker" in err, f"error must name docker; got stderr={r.stderr!r}"


@pytest.mark.timeout(120)
def test_typo_nested_field_fails_loudly(devm, workspace):
    (workspace.path / "devm.yaml").write_text(
        "project:\n"
        "  name: sizing-loud\n"
        "repos:\n"
        "  main:\n"
        "    url: https://github.com/octocat/Hello-World.git\n"
        "docker:\n"
        "  enabled: true\n"
        "  buildCahe: \"15%\"\n"
    )
    r = _run_start(devm.path, workspace.path)
    assert r.returncode != 0, "docker.buildCahe typo must fail loudly"
    err = r.stderr.decode()
    assert "buildCahe" in err, (
        f"error must name the typo verbatim; got stderr={err!r}"
    )
    assert "buildCache" in err or "enabled" in err, (
        f"error should list valid docker keys; got stderr={err!r}"
    )


@pytest.mark.timeout(120)
def test_malformed_swap_fails_loudly(devm, workspace):
    (workspace.path / "devm.yaml").write_text(
        "project:\n"
        "  name: sizing-loud\n"
        "repos:\n"
        "  main:\n"
        "    url: https://github.com/octocat/Hello-World.git\n"
        "swap: \"abc\"\n"
    )
    r = _run_start(devm.path, workspace.path)
    assert r.returncode != 0, "swap: abc must fail loudly"
    err = r.stderr.decode()
    assert "swap" in err and "abc" in err, (
        f"error must name both the field and the bad value; got stderr={err!r}"
    )
