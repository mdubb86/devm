"""81: happy-path coverage for four devm CLI subcommands that had no e2e.

  - `devm status` — prints VM state; supports --json.
  - `devm validate` — schema-checks devm.yaml.
  - `devm skills list / get` — surfaces embedded reference docs.
  - `devm secret list` — enumerates keychain-stored secrets for a project.

Each command is exercised standalone (no cold-start required). Failure
shapes (bad yaml, missing keychain entry) are pinned lightly to prove
the negative path exits non-zero — full error-formatting details are
tested by internal Go unit tests.

What this pins:
  - `devm status` exits 0 on a valid devm.yaml even before a cold-start.
  - `devm status --json` returns valid JSON.
  - `devm validate` exits 0 on a well-formed devm.yaml, non-zero on
    a broken one (unknown field is the reliable trigger — devm's
    schema rejects unknown yaml keys).
  - `devm skills list` prints every embedded skill name.
  - `devm skills get <name>` prints that skill's body.
  - `devm secret set` + `devm secret list` show the set name; delete removes it.

What it doesn't cover (tested elsewhere):
  - Secret injection into iron-proxy requests — test_43.
  - Skill drift vs current architecture — internal/skills unit tests.
"""
from __future__ import annotations

import json
import subprocess

import pytest

pytestmark = pytest.mark.devm


def _run(devm, *args, **kw):
    kw.setdefault("capture_output", True)
    kw.setdefault("timeout", 30)
    return subprocess.run([devm.path, *args], **kw)


@pytest.mark.timeout(60)
def test_devm_status_prints_and_json(devm, workspace):
    workspace.write_devmyaml()

    r = _run(devm, "status", cwd=str(workspace.path))
    assert r.returncode == 0, f"devm status failed: {r.stderr.decode()}"
    # Text output isn't empty and mentions the VM name.
    assert workspace.vm_name in r.stdout.decode(), (
        f"expected vm name in text output; got:\n{r.stdout.decode()}"
    )

    r = _run(devm, "status", "--json", cwd=str(workspace.path))
    assert r.returncode == 0, f"devm status --json failed: {r.stderr.decode()}"
    doc = json.loads(r.stdout.decode())
    assert isinstance(doc, dict), f"expected a JSON object; got {type(doc)}"


@pytest.mark.timeout(30)
def test_devm_validate_accepts_valid_rejects_broken(devm, workspace):
    workspace.write_devmyaml()
    r = _run(devm, "validate", cwd=str(workspace.path))
    assert r.returncode == 0, f"validate should accept minimal yaml: {r.stderr.decode()}"

    # Break the yaml with an unknown top-level key. Devm rejects unknown
    # fields (per user's own requirement — see feedback_no_unknown_keys).
    (workspace.path / "devm.yaml").write_text(
        (workspace.path / "devm.yaml").read_text()
        + "\nnot_a_real_field: nope\n"
    )
    r = _run(devm, "validate", cwd=str(workspace.path))
    assert r.returncode != 0, "validate should reject unknown yaml keys"
    stderr = r.stderr.decode() + r.stdout.decode()
    assert "not_a_real_field" in stderr, (
        f"error should name the offending key; got:\n{stderr}"
    )


@pytest.mark.timeout(30)
def test_devm_skills_list_and_get(devm):
    r = _run(devm, "skills", "list")
    assert r.returncode == 0, f"skills list failed: {r.stderr.decode()}"
    out = r.stdout.decode()
    # Every canonical skill should appear.
    for name in ("devm", "schema", "lifecycle", "service", "routing", "secrets", "errors"):
        assert name in out, f"skill {name!r} missing from `skills list`:\n{out}"

    r = _run(devm, "skills", "get", "schema")
    assert r.returncode == 0, f"skills get schema failed: {r.stderr.decode()}"
    body = r.stdout.decode()
    assert "devm.yaml schema reference" in body, (
        f"schema skill body wrong shape:\n{body[:200]}"
    )


@pytest.mark.timeout(60)
def test_devm_secret_set_list_delete(devm, workspace):
    workspace.write_devmyaml()

    secret_name = "e2e-secret-81"

    # Set (stdin-piped value).
    r = subprocess.run(
        [devm.path, "secret", "set", secret_name],
        input=b"the-value\n", capture_output=True,
        cwd=str(workspace.path), timeout=15,
    )
    assert r.returncode == 0, f"secret set failed: {r.stderr.decode()}"

    try:
        # List includes it.
        r = _run(devm, "secret", "list", cwd=str(workspace.path))
        assert r.returncode == 0, f"secret list failed: {r.stderr.decode()}"
        assert secret_name in r.stdout.decode(), (
            f"set secret didn't appear in list:\n{r.stdout.decode()}"
        )
    finally:
        # Delete.
        r = _run(devm, "secret", "delete", secret_name, cwd=str(workspace.path))
        assert r.returncode == 0, f"secret delete failed: {r.stderr.decode()}"

    # List no longer includes it.
    r = _run(devm, "secret", "list", cwd=str(workspace.path))
    assert r.returncode == 0
    assert secret_name not in r.stdout.decode(), (
        f"deleted secret still visible in list:\n{r.stdout.decode()}"
    )
