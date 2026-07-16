"""113: `direct: true` without a `hostname` is a hard config error.

A direct service has nothing to resolve to VM_IP without a hostname (the
DNS resolver keys off hostname, not service name), so devm rejects the
config at load time rather than silently no-op'ing the feature. See
`internal/schema/schema.go` `Service.Validate` — `direct: true` requires
`hostname`, checked before any VM work.

No VM required — schema validation is pure config-load, same shape as
test_81's `devm validate` coverage.

What this pins:
  - `devm validate` exits non-zero for a `direct: true` service with no
    `hostname`.
  - The error names the offending service and states the requirement:
    `services.<name>: direct: true requires a hostname`.
  - A `direct: true` service WITH a hostname is accepted (doesn't
    regress the positive path — no VM needed to prove schema-level
    acceptance).

What it doesn't cover (tested elsewhere, requires a VM):
  - Everything downstream of a valid `direct: true` config — DNS
    answering VM_IP, the `svc_ingress` firewall rule, Caddyfile
    exclusion, live reconcile — see test_110/111/112.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


def _run(devm, *args, **kw):
    kw.setdefault("capture_output", True)
    kw.setdefault("timeout", 30)
    return subprocess.run([devm.path, *args], **kw)


@pytest.mark.timeout(30)
def test_direct_without_hostname_fails_validation(devm, workspace):
    workspace.write_devmyaml(
        services={
            "db": {"port": 54322, "direct": True},
        },
    )

    r = _run(devm, "validate", cwd=str(workspace.path))
    assert r.returncode != 0, (
        "validate should reject `direct: true` with no hostname; "
        f"got exit 0. stdout={r.stdout.decode()!r}"
    )
    out = r.stdout.decode() + r.stderr.decode()
    assert "direct: true requires a hostname" in out, (
        f"error should name the direct/hostname requirement; got:\n{out}"
    )
    assert "services.db" in out, (
        f"error should name the offending service (services.db); got:\n{out}"
    )


@pytest.mark.timeout(30)
def test_direct_with_hostname_passes_validation(devm, workspace):
    workspace.write_devmyaml(
        services={
            "db": {"port": 54322, "hostname": "db.test", "direct": True},
        },
    )

    r = _run(devm, "validate", cwd=str(workspace.path))
    assert r.returncode == 0, (
        f"validate should accept `direct: true` WITH a hostname: "
        f"stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
    )
