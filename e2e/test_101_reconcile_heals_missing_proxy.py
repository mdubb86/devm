"""101: `devm reconcile` is the ONLY thing that heals a missing iron-proxy.

Pins the collapsed self-heal model (no daemon-start auto-heal, no
on-demand heal from `shell`/`status`/etc — see test_103/test_104 for
those). Sequence:

  1. Cold-start (no secrets) with an allow-listed host.
  2. `restart_isolated_daemon()` — the proxy becomes *adopted*. A
     verified spike (test_100) found the supervisor does NOT
     auto-restart an adopted proxy if it dies, only a freshly-spawned
     one — so we must cross this seam before killing it, or the
     supervisor would silently undo the kill and the test would prove
     nothing.
  3. Kill the (now adopted) iron-proxy; confirm it stays dead.
  4. `devm reconcile` — assert it heals: the proxy PID reappears and
     the allow-listed host reaches upstream through it again (mirrors
     test_43's egress check).

Uses api.github.com (test_43's host + assertion shape) rather than
httpbin.org (test_92's host) — httpbin.org is a shared free service
observed to intermittently 503 independent of devm/iron-proxy;
api.github.com has proven reliable across this suite.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.exec_retry import devm_exec_with_retry
from helpers.proxy import kill_project_proxy, project_proxy_running

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_reconcile_heals_missing_proxy(devm, workspace, sandbox_name, devm_installed, restart_isolated_daemon):
    workspace.write_devmyaml(
        install=["true"],
        services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
        network={"allow": ["api.github.com"]},
    )

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    assert project_proxy_running(workspace.slug), (
        "iron-proxy should be running after cold-start"
    )

    # Cross the adoption seam: a freshly-spawned proxy WOULD be
    # auto-restarted by the supervisor if killed; an adopted one won't.
    restart_isolated_daemon()
    assert project_proxy_running(workspace.slug), (
        "iron-proxy should survive the daemon restart (adopted)"
    )

    kill_project_proxy(workspace.slug)
    assert not project_proxy_running(workspace.slug), (
        "iron-proxy should be dead immediately after SIGKILL"
    )
    # It must STAY dead — nothing outside `devm reconcile` heals it.
    time.sleep(3)
    assert not project_proxy_running(workspace.slug), (
        "iron-proxy respawned on its own; self-heal must be reconcile-only"
    )

    reconcile = subprocess.run(
        [devm.path, "reconcile"],
        cwd=str(workspace.path), capture_output=True, timeout=120,
    )
    assert reconcile.returncode == 0, (
        f"devm reconcile failed:\nstdout={reconcile.stdout.decode()!r}\n"
        f"stderr={reconcile.stderr.decode()!r}"
    )

    assert project_proxy_running(workspace.slug), (
        f"iron-proxy should be running again after reconcile; "
        f"reconcile stdout={reconcile.stdout.decode()!r}"
    )

    # And it's functional, not just alive: allow-listed host still
    # reaches upstream through the healed proxy.
    allowed = devm_exec_with_retry(
        devm.path,
        ["curl", "-sf", "-o", "/dev/null", "-w", "%{http_code}",
         "--max-time", "15", "https://api.github.com/octocat"],
        cwd=str(workspace.path), timeout=60,
    )
    assert allowed.returncode == 0 and allowed.stdout.decode().strip() == "200", (
        f"expected 200 through the healed iron-proxy; got {allowed.stdout!r} "
        f"(stderr={allowed.stderr.decode()!r})"
    )
