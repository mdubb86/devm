"""72: declared systemd service stays active and is reachable over HTTP.

Pin: services declared via services.X.exec: end up enabled under
systemd, become active after cold-start (the provisioner's service-health
assertion guarantees this), and the running unit serves HTTP traffic.

The workload is a host-process `nc` loop (test_157's pattern) rather
than a real HTTP daemon: nothing HTTP-serving ships in the base image
anymore, and this test's subject is systemd service lifecycle, not the
workload itself. `netcat-openbsd` is declared via `packages:`
(test_112's shape), with `network.allow` for the Debian mirrors the apt
install needs.

NOTE: these are literal two-character `\\r`/`\\n` escapes (doubled
backslashes), not actual CR/LF bytes — the string below becomes a
single-line shell command that `printf`'s format-string interprets at
runtime. Embedding real newlines here would span the ExecStart= line
the provisioner renders into the service's systemd unit file, which
systemd rejects ("bad unit file setting").
"""
import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm

PORT = 58080
BODY = "pong"
_RESPONSE = (
    "HTTP/1.1 200 OK\\r\\n"
    f"Content-Length: {len(BODY)}\\r\\n"
    "Connection: close\\r\\n"
    "\\r\\n"
    f"{BODY}"
)


@pytest.mark.timeout(300)
def test_systemd_service_active_and_reachable(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        packages=["netcat-openbsd"],
        network={"allow": ["deb.debian.org", "security.debian.org"]},
    )
    # Fresh `nc` per connection: `printf | nc -l` exits once the client
    # disconnects, so a `while true` wrapper keeps a listener up across
    # the repeated curl below (matches test_157's host-process pattern).
    script = f"while true; do printf '{_RESPONSE}' | nc -l -p {PORT}; done"
    workspace.add_systemd_service(
        name="echosvc",
        exec=["sh", "-c", script],
        restart="always",
        user="root",
    )

    sandbox = TartSandbox(name=sandbox_name)

    # Cold-start: provisions the VM, renders the service unit, enables
    # and starts it. The provisioner's health poll (10 s budget) confirms
    # active state before returning.
    proc = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True, timeout=300, check=False,
    )
    assert proc.returncode == 0, (
        f"cold-start failed: stderr={proc.stderr.decode()!r}"
    )

    current = sandbox.state()
    assert current == "running", (
        f"expected VM running after cold-start; got {current!r}"
    )

    # Confirm the service unit reached active state inside the VM.
    r = sandbox.exec("systemctl", "is-active", "echosvc")
    assert r.exit_code == 0 and r.stdout.strip() == "active", (
        f"echosvc did not become active: stdout={r.stdout!r} stderr={r.stderr!r}"
    )

    # HTTP reachability from inside the VM (loopback; no Mac-side routing needed).
    r = sandbox.exec("curl", "-sf", f"http://127.0.0.1:{PORT}/")
    assert r.exit_code == 0 and BODY in r.stdout, (
        f"echosvc unreachable on :{PORT}: stdout={r.stdout!r} stderr={r.stderr!r}"
    )
