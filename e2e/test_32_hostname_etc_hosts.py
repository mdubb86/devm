"""32: devm-startup.sh splices service hostnames into /etc/hosts.

A service with `hostname: <name>` triggers devm to render
.devm/hosts.fragment with a `127.0.0.1 <name>` line. devm-startup.sh
runs as startup step 1 inside the sandbox and splices the fragment
between `# BEGIN devm hostnames` / `# END devm hostnames` markers
in /etc/hosts so the hostname resolves to loopback (where caddy
listens, when caddy is present).

What this pins:
  - .devm/hosts.fragment is rendered on the host with the expected line.
  - After cold-start, /etc/hosts inside the sandbox contains the
    BEGIN/END markers and the `127.0.0.1 <name>` line.
  - `getent hosts <name>` resolves to 127.0.0.1 inside the sandbox.

What it doesn't cover:
  - Live changes (adding/removing a hostname on a running sandbox) —
    masks: and services: shape changes are still teardown-bucket today.
  - Caddy reverse-proxy behavior — covered by Caddyfile unit tests in
    internal/render. We just confirm /etc/hosts here.
"""
import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_hostname_lands_in_etc_hosts(workspace, devm, tart_sandbox, sandbox_name):
    hostname = f"{workspace.slug}-api.local"
    workspace.write_devmyaml(
        services={
            "api": {"port": 8080, "hostname": hostname},
        },
    )

    # Host-side: render writes the fragment when devm shell starts.
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # Host-side: the rendered fragment exists with the expected line.
        fragment = (workspace.path / ".devm" / "hosts.fragment").read_text()
        assert f"127.0.0.1 {hostname}\n" == fragment, (
            f"unexpected fragment content: {fragment!r}"
        )

        # In-sandbox: /etc/hosts contains the BEGIN/END markers and the line.
        r = tart_sandbox.exec("cat", "/etc/hosts", timeout=10)
        assert r.ok, f"cat /etc/hosts failed: {r.stderr!r}"
        etc_hosts = r.stdout
        assert "# BEGIN devm hostnames" in etc_hosts, (
            f"missing BEGIN marker:\n{etc_hosts}"
        )
        assert "# END devm hostnames" in etc_hosts, (
            f"missing END marker:\n{etc_hosts}"
        )
        assert f"127.0.0.1 {hostname}" in etc_hosts, (
            f"missing hosts line for {hostname}:\n{etc_hosts}"
        )

        # In-sandbox: resolver actually sees the entry.
        sh.run_check(
            f"getent hosts {hostname} | grep -q '^127\\.0\\.0\\.1'",
            expect_zero=True, timeout=10,
        )

        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
