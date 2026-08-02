"""156: old services.<name>.masks: shape errors as unknown field.

Zero backwards compat: writing a yaml with the pre-v0.9.18 per-
service masks shape must fail schema validation with the standard
'unknown field' error naming services.<svc>.masks. This is the
whole migration path — the error tells the user to move to the
top-level shape.

We test via `devm validate` (or `devm reconcile`, which loads the
config) because that's the user-facing surface where the error
would appear.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
@pytest.mark.slow
def test_mask_old_per_service_shape_rejected(devm, workspace, sandbox_name):
    # Write yaml directly (bypassing workspace.write_devmyaml which
    # would go through the schema layer we're trying to test).
    yaml_body = """project:
  name: {name}
services:
  api:
    port: 8080
    masks:
      - path: node_modules
""".format(name=workspace.slug)
    devm.unlock()  # devm.yaml may be locked from any prior write
    workspace.devmyaml_path.write_text(yaml_body)
    try:
        r = subprocess.run(
            [devm.path, "validate"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode != 0, (
            "validate succeeded on old-shape yaml — cutover broken"
        )
        stderr = r.stderr.decode()
        # CheckUnknownKeys produces "unknown field" messages naming
        # the offending path.
        assert "masks" in stderr, f"error doesn't mention masks:\n{stderr}"
        assert "service" in stderr.lower(), (
            f"error doesn't scope the failure to the service subtree:\n{stderr}"
        )
    finally:
        # No sandbox was created — nothing to teardown, but be safe.
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
