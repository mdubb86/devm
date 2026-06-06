"""agent.persistence field is REJECTED by sbx 0.31+.

After upgrading from sbx 0.28.3 to 0.31.3 (2026-06-05), all devm e2e
tests started failing with:

    ERROR: resolve kits: kit "...": artifact: invalid spec.yaml:
    yaml: unmarshal errors:
      line 10: field persistence not found in type spec.agentBlock

The Docker docs still document `agent.persistence: persistent|ephemeral`
but the binary rejects it. This test materializes minimal kits with
different agent-block shapes and pins which sbx 0.31 actually accepts.
internal/render/spec.go was updated based on this evidence to drop
the field.

Variants tested:
  1. baseline_with_persistence    — pre-0.31 devm shape (must FAIL)
  2. baseline_without_persistence — current devm shape (must PASS)

If sbx un-rejects `persistence` in a future release, variant 1 starts
passing, and we can revisit the render.
"""
from __future__ import annotations
import os
import subprocess
import tempfile
import textwrap

import pytest

pytestmark = pytest.mark.sbx


def _kit_dir_with_spec(spec: str) -> str:
    d = tempfile.mkdtemp(prefix="sbx-schema-kit-")
    with open(os.path.join(d, "spec.yaml"), "w") as f:
        f.write(spec)
    return d


def _spec_resolves(kit_dir: str, sandbox_name: str) -> tuple[bool, str]:
    """Try `sbx run --kit <kit> --name <name> probespec <ws>` long enough
    to surface a spec-parse failure (which happens instantly) but not
    long enough to actually bring up a sandbox. Returns (parsed_ok, stderr).
    """
    ws = tempfile.mkdtemp(prefix="sbx06-ws-")
    try:
        proc = subprocess.run(
            ["sbx", "run", "--kit", kit_dir,
             "--name", sandbox_name, "probespec", ws],
            capture_output=True, text=True, timeout=10, stdin=subprocess.DEVNULL,
        )
    except subprocess.TimeoutExpired as e:
        # Timed out → spec must have parsed (otherwise sbx errors instantly).
        # Kill any orphan + return success.
        subprocess.run(["sbx", "rm", "-f", sandbox_name],
                       capture_output=True, timeout=10)
        return True, e.stderr or ""
    finally:
        subprocess.run(["sbx", "rm", "-f", sandbox_name],
                       capture_output=True, timeout=10)
        import shutil
        shutil.rmtree(ws, ignore_errors=True)
    err = proc.stderr or proc.stdout
    parsed = "invalid spec.yaml" not in err
    return parsed, err


BASELINE_AGENT = textwrap.dedent("""\
    schemaVersion: "1"
    kind: agent
    name: probespec
    displayName: probe spec
    description: probe spec schema
    agent:
      image: docker/sandbox-templates:shell
      aiFilename: CLAUDE.md
      %s
      entrypoint:
        run: ["sh", "-c", "exec sleep infinity </dev/null"]
    environment:
      variables:
        IS_SANDBOX: "1"
    commands:
      install:
        - command: 'true'
      startup:
        - command: ['sh', '-c', 'true']
          user: "1000"
          description: noop
""")


def test_persistence_field_rejected(sandbox_name):
    """The current devm shape — `persistence: persistent` — is rejected
    by sbx 0.31.3. Pin the exact error string so we know what we're
    working around."""
    spec = BASELINE_AGENT % "persistence: persistent"
    kit = _kit_dir_with_spec(spec)
    ok, err = _spec_resolves(kit, sandbox_name)
    assert not ok, (
        f"sbx now accepts persistence: persistent? "
        f"If so, the 0.31 rejection was fixed upstream — re-enable in "
        f"render/spec.go.\nstderr:\n{err}"
    )
    assert "persistence" in err, (
        f"sbx rejected the spec but not for persistence — different "
        f"breakage. stderr:\n{err}"
    )


def test_no_persistence_field_accepted(sandbox_name):
    """Removing persistence — does sbx accept the spec? If yes, devm
    can just drop the field (default behavior in 0.31 is fine).
    """
    spec = BASELINE_AGENT % ""  # nothing in place of persistence: ...
    kit = _kit_dir_with_spec(spec)
    ok, err = _spec_resolves(kit, sandbox_name)
    assert ok, (
        f"Even without persistence, sbx rejects the spec. Different "
        f"schema change too. stderr:\n{err}"
    )
