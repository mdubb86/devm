"""77: `services.X.systemd:` full-override — the verbatim unit content
lands at /etc/systemd/system/<X>.service and gets enabled at cold-start.

The declarative service form (exec/restart/after/user/…) renders a unit
via render.RenderService. When `systemd:` is set instead, RenderService
returns that block verbatim so the user has full control of the unit
file — bypassing devm's default After=devm-ready.target etc.

Full-override is mutually exclusive with exec/restart/after/workdir/user
(rejected at schema.Validate). This test pins the happy path.

What this pins:
  - `systemd:` string content becomes the on-disk unit at
    /etc/systemd/system/<name>.service (verbatim, no wrapper).
  - The provisioner enables + starts the resulting unit; the service
    survives the health-check window.
  - A marker file written by the ExecStart is present.

What it doesn't cover (tested elsewhere):
  - Mutual-exclusion validation — covered by unit tests in
    internal/schema.
  - Declarative form + fields — test_72 (caddy) exercises those.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


UNIT_TEXT = """[Unit]
Description=devm e2e full-override probe
After=devm-ready.target
Requires=devm-ready.target

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'echo full-override-ok > /home/admin/systemd-override-marker && sync'
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
"""


@pytest.mark.timeout(180)
def test_service_systemd_full_override_lands_verbatim(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        services={"override": {"systemd": UNIT_TEXT}},
    )

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    tart_sandbox = TartSandbox(name=sandbox_name)

    # Unit content landed verbatim.
    r = tart_sandbox.exec_shell("sudo cat /etc/systemd/system/override.service")
    assert r.ok, f"unit file missing: {r.stderr}"
    assert "full-override-ok" in r.stdout, (
        f"unit content wasn't rendered verbatim:\n{r.stdout}"
    )
    # Devm didn't inject its declarative-form wrapper — the unit content
    # is our string, only trailing whitespace normalised.
    assert r.stdout.rstrip() == UNIT_TEXT.rstrip(), (
        f"unit content differs from declared string:\n"
        f"got:\n{r.stdout!r}\nwant:\n{UNIT_TEXT!r}"
    )

    # The service ran and wrote the marker.
    r = tart_sandbox.exec_shell("cat /home/admin/systemd-override-marker")
    assert r.ok, f"marker missing — service didn't execute: {r.stderr}"
    assert r.stdout.strip() == "full-override-ok", (
        f"marker content wrong: {r.stdout!r}"
    )
