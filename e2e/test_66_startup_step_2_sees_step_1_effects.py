"""66: startup service with after: sees effects from the service it depends on.

Pins that a service with `after: [dep]` starts only after `dep` has
completed, so it can rely on dep's filesystem side effects.

In Ship 4, startup is systemd-managed: each service is a separate
`[Service]` unit. Sequential ordering uses the `after:` field which
renders to `After=<dep>.service` in the unit file. There is no "list
of startup steps" — ordering is expressed via explicit dependencies.

Setup:
  - service "step1": touch /tmp/s1-ran; restart: no
  - service "step2": test -f /tmp/s1-ran && touch /tmp/s2-saw-s1;
    restart: no; after: [step1]

After bringup, /tmp/s2-saw-s1 must exist — proves step2 ran AND saw
step1's marker. If systemd started the services in parallel (ignoring
After=), /tmp/s2-saw-s1 could be absent.

Devm dependency: the `after:` field in devm.yaml must render to a
systemd After= dependency so users can express ordered startup.
"""
from __future__ import annotations

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_startup_service_after_sees_dep_effects(workspace, devm, tart_sandbox):
    workspace.write_devmyaml(
        services={
            "step1": {
                "exec": ["sh", "-c", "touch /tmp/s1-ran"],
                "restart": "no",
            },
            "step2": {
                "exec": ["sh", "-c",
                         "test -f /tmp/s1-ran && touch /tmp/s2-saw-s1"],
                "restart": "no",
                "after": ["step1"],
            },
        },
    )

    assert tart_sandbox.state() == "running", (
        f"expected VM running; got {tart_sandbox.state()!r}"
    )

    # step1's marker must be present.
    r = tart_sandbox.exec_shell("test -f /tmp/s1-ran && echo present")
    assert r.ok and "present" in r.stdout, (
        f"/tmp/s1-ran missing — step1 service didn't run or write marker. "
        f"stdout={r.stdout!r} stderr={r.stderr!r}"
    )

    # The contract pin: step2 saw step1's effect.
    r = tart_sandbox.exec_shell("test -f /tmp/s2-saw-s1 && echo present")
    assert r.ok and "present" in r.stdout, (
        f"/tmp/s2-saw-s1 absent — step2 either didn't run or ran before "
        f"step1 completed. The after: field may not be rendering to a "
        f"systemd After= dependency. stdout={r.stdout!r} stderr={r.stderr!r}"
    )
