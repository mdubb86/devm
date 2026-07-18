"""Daemon-driven softnet egress e2e (Plan 3, Task 9).

End-to-end validation of the whole Plan 3 cutover, driving the REAL
devm daemon (not raw tart, unlike test_softnet_15/16 and
test_tart_contract_14). The daemon now:

  - launches every VM with `tart run --net-softnet` (softnet is the
    sole NIC — a gvisor netstack living in the devm binary itself,
    resolved via a `softnet`-named symlink on the child's $PATH)
  - drives egress LOCKED -> OPEN (provisioning window) -> ENFORCED
    over a unix control socket
  - points ENFORCED egress at iron-proxy's loopback listeners
  - no longer bakes guest-side nftables/dnsmasq egress rules

This is the proxy-level equivalent of test_43
(test_43_iron_proxy_egress_enforcement.py), adapted to additionally
assert the softnet-specific invariants: sole-NIC softnet subnet, and
absence of the old guest nftables egress tables.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(300)
def test_daemon_softnet_egress(workspace, devm, sandbox_name):
    # devm.yaml with a restrictive network.allow — same allow-listed
    # host test_43 uses (known-good against the real upstream).
    workspace.write_devmyaml(
        install=["true"],
        services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
        network={"allow": ["api.github.com"]},
    )

    sandbox = TartSandbox(name=sandbox_name)

    # --- 1: cold-start through the daemon; `devm shell`/`tart exec`
    # --- control path survives the softnet cutover. ---
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
    assert sandbox.state() == "running", (
        f"expected VM running after cold-start; got {sandbox.state()!r}"
    )

    # `tart exec` reaches the guest (proves the control path the
    # daemon's provisioning/enforcement steps depend on is intact).
    r = sandbox.exec("echo", "tart-exec-ok")
    assert r.ok and "tart-exec-ok" in r.stdout, (
        f"tart exec failed after cold-start: rc={r.exit_code} "
        f"stdout={r.stdout!r} stderr={r.stderr!r}"
    )

    # --- 2: sole NIC is the softnet device, 192.168.127.0/24. ---
    r = sandbox.exec_shell("ls /sys/class/net")
    assert r.ok, f"ls /sys/class/net failed: {r.stderr}"
    ifaces = [ln.strip() for ln in r.stdout.splitlines() if ln.strip() and ln.strip() != "lo"]
    assert len(ifaces) == 1, (
        f"expected exactly one non-lo NIC under softnet; got {ifaces!r}\n"
        f"(full listing: {r.stdout!r})"
    )
    nic = ifaces[0]

    r = sandbox.exec_shell(f"ip -4 -o addr show dev {nic}")
    assert r.ok, f"ip addr show {nic} failed: {r.stderr}"
    assert "192.168.127." in r.stdout, (
        f"softnet NIC {nic!r} does not have a 192.168.127.x address: {r.stdout!r}"
    )

    # --- 3: allowed egress succeeds; non-allow-listed egress is
    # --- blocked. Mirrors test_43's assertions, through the daemon's
    # --- ENFORCED-flip on the softnet control socket. ---
    r = subprocess.run(
        [devm.path, "shell", "--", "curl", "-sf", "-o", "/dev/null",
         "-w", "%{http_code}", "--max-time", "15", "https://api.github.com/octocat"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=30,
    )
    assert r.returncode == 0 and r.stdout.strip() == b"200", (
        f"allow-listed host returned status {r.stdout!r} (stderr: {r.stderr.decode()})"
    )

    r = subprocess.run(
        [devm.path, "shell", "--", "curl", "-sf", "-o", "/dev/null",
         "--max-time", "15", "https://google.com"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=30,
    )
    # curl -sf returns 22 for HTTP errors (iron-proxy's 502 for hosts
    # not on the allow list) and non-zero for connection failures.
    # Either way, non-zero is what we want here.
    assert r.returncode != 0, (
        "non-allow-listed host should have been blocked but curl returned 0"
    )

    # --- 4: no guest nftables egress table — softnet is the sole
    # --- egress gate now; the old guest-side devm_filter/devm_nat
    # --- tables were retired under Plan 3. ---
    r = sandbox.exec("sudo", "nft", "list", "ruleset")
    combined = r.stdout + r.stderr
    assert "devm_filter" not in combined and "devm_nat" not in combined, (
        f"guest nftables still has a devm egress table (should be gone under softnet):\n{combined}"
    )
