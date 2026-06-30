"""72: declared systemd service stays active and is reachable over HTTP.

Pin: services declared via services.X.exec: end up enabled under
systemd, become active after cold-start (the provisioner's service-health
assertion guarantees this), and the running unit serves HTTP traffic.

Caddy is the workload because:
  - it is present in the devm base image (devm already depends on it
    for in-VM .test routing)
  - it serves HTTP (easy reachability check via curl)
  - it is managed by systemd (real Linux service unit)

Reframe from brief (Bug F): the Caddyfile is written via install: (runs
inside the VM via tart exec) rather than workspace.path (which requires
the virtiofs share to be mounted inside the VM — Bug F). Service name is
"caddysvc" (not "caddy") to avoid clobbering the base caddy unit that
devm uses for .test routing on ports 80/443.
"""
import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_caddy_systemd_service_active_and_reachable(workspace, devm, sandbox_name):
    # Write the Caddyfile via install: — runs inside the VM, so no
    # virtiofs mount is needed (avoids Bug F).
    # admin off: the system caddy (devm's .test router) already owns
    # 127.0.0.1:2019 (caddy admin API default); our second caddy instance
    # must disable it or bind a different port.
    workspace.write_devmyaml(
        install=[
            "printf '{\\n  admin off\\n}\\n:8080 {\\n  respond \"pong\" 200\\n}\\n'"
            " > /tmp/caddyfile.test",
        ],
    )
    # Declare the caddy service via exec: fields; the provisioner renders
    # these into /etc/systemd/system/caddysvc.service. Use "caddysvc"
    # (not "caddy") to avoid overwriting the base caddy unit that devm
    # uses for in-VM .test routing on ports 80/443.
    workspace.add_systemd_service(
        name="caddysvc",
        exec=["/usr/bin/caddy", "run",
              "--config", "/tmp/caddyfile.test",
              "--adapter", "caddyfile"],
        restart="always",
        user="root",
    )

    sandbox = TartSandbox(name=sandbox_name)

    # Cold-start: provisions the VM, renders the service unit, enables
    # and starts it. The provisioner's health poll (10 s budget) confirms
    # active state before returning.
    proc = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path),
        capture_output=True, timeout=300, check=False,
    )
    assert proc.returncode == 0, (
        f"cold-start failed: stderr={proc.stderr.decode()!r}"
    )

    assert sandbox.state() == "running", (
        f"expected VM running after cold-start; got {sandbox.state()!r}"
    )

    # Confirm the service unit reached active state inside the VM.
    r = sandbox.exec("systemctl", "is-active", "caddysvc")
    assert r.exit_code == 0 and r.stdout.strip() == "active", (
        f"caddysvc did not become active: stdout={r.stdout!r} stderr={r.stderr!r}"
    )

    # HTTP reachability from inside the VM (loopback; no Mac-side routing needed).
    r = sandbox.exec("curl", "-sf", "http://127.0.0.1:8080/")
    assert r.exit_code == 0 and "pong" in r.stdout, (
        f"caddy unreachable on :8080: stdout={r.stdout!r} stderr={r.stderr!r}"
    )
