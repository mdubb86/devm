"""131: network.allow REMOVAL takes effect, end to end.

Policy lives in the daemon's PolicyAuthority, not in iron-proxy's
on-disk config. iron-proxy's YAML carries a single unconditional `grpc`
transform that delegates every request's allow/deny decision to the
daemon over a unix socket (see internal/serviceapi/ironproxy.go); there
is no `allowlist` transform with a `domains` list to inspect anymore.
This test pins two things that still matter with that architecture:

  1. Snapshot advancement: the apply-iron-proxy snapshot-Cfg drift bug
     (fixed in the sibling commit) — before the fix,
     updateSnapshotAfterSpawn updated only SecretHashes and
     ProxyVersion, leaving snap.Cfg frozen. On the next reconcile the
     diff engine compared devm.yaml against stale snap.Cfg and saw no
     change for removals. Pinned directly by reading the daemon's
     on-disk snapshot state (`_snapshot_allow_hosts`).
  2. Enforcement effect: a removal must actually take effect at the
     surface a guest sees — curl inside the VM. Pinned behaviorally:
     a host removed from network.allow starts getting devm's
     self-describing reject (X-Devm-Blocked: egress-policy) again.

This test exercises the exact remove pattern that surfaced the
snapshot drift bug in production (buzztrack, 2026-07-30):

  1. Cold-start with network.allow = [api.github.com]. httpbin.org is
     blocked from inside the guest.
  2. Unlock + edit devm.yaml to network.allow = [api.github.com,
     httpbin.org]. Reconcile — apply-iron-proxy fires, snap.Cfg
     advances (with the fix), and httpbin.org becomes reachable from
     the guest.
  3. Unlock + edit devm.yaml back to network.allow = [api.github.com]
     (drop httpbin.org). Reconcile.
  4. Assert httpbin.org is blocked again from inside the guest, with
     devm's marker header on the reject — proof the ENFORCEMENT layer
     (not just the snapshot) saw the removal. Pre-fix: this fails
     because reconcile silently no-op'd the removal.
  5. Assert daemon snapshot's Cfg.Network.Allow reflects the removal.

Distinct from test_44 and test_130 — those pinned adopt/spawn
mechanics; this pins snapshot-Cfg baseline advancement across
consecutive reconciles, and that the removal reaches the policy
authority's enforcement.
"""
from __future__ import annotations

import json
import subprocess
import time
from pathlib import Path

import pytest
import yaml

pytestmark = pytest.mark.devm


def _runtime_dir() -> Path:
    """The devm-e2e daemon's runtime dir (mirrors test_74's _RUNDIR)."""
    return Path.home() / "Library" / "Application Support" / "devm-e2e"


def _iron_proxy_config(project_id: str) -> dict | None:
    path = _runtime_dir() / "iron-proxy" / f"{project_id}.yaml"
    if not path.exists():
        return None
    return yaml.safe_load(path.read_text())


def _snapshot_allow_hosts(project_id: str) -> list[str]:
    """Hosts in daemon's snapshot Cfg.Network.Allow."""
    path = _runtime_dir() / "state" / f"{project_id}.json"
    if not path.exists():
        return []
    snap = json.loads(path.read_text())
    allow = snap.get("cfg", {}).get("Network", {}).get("Allow", []) or []
    return [entry["Host"] for entry in allow]


def _curl_status(sandbox, url: str) -> int:
    """Run curl -o /dev/null -s -w '%{http_code}' <url> inside the guest,
    return the parsed HTTP status (int). 0 on connection failure.
    """
    r = sandbox.exec_shell(
        f"curl -o /dev/null -s -w '%{{http_code}}' --max-time 5 {url}"
    )
    try:
        return int(r.stdout.strip() or "0")
    except ValueError:
        return 0


@pytest.mark.timeout(420)
@pytest.mark.slow
def test_iron_proxy_allow_removal_takes_effect(devm, workspace, sandbox_name, devm_installed):
    from helpers.tart import TartSandbox

    # Initial config with ONE allow entry so cold-start builds a real
    # policy allowlist. packages=curl so the guest can probe egress.
    workspace.write_devmyaml(
        # Opt out of the default repos.main (github.com/octocat/Hello-World):
        # this test's allowlist is narrow (api.github.com only, no wildcard
        # for github.com), and hydration would clone that repo and get
        # blocked by the same egress policy this test exercises.
        no_repo=True,
        install=["true"],
        services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
        network={"allow": ["api.github.com"]},
        packages=["curl"],
    )

    try:
        # 1. Cold-start: iron-proxy spawned, policy authority holds the
        # initial allow list.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
        sandbox = TartSandbox(name=sandbox_name)

        # The on-disk iron-proxy config no longer carries an allowlist
        # transform with a domains list — it delegates unconditionally
        # to the daemon's PolicyAuthority via a `grpc` transform. Pin
        # that shape once, here.
        cfg = _iron_proxy_config(workspace.slug)
        assert cfg is not None, "iron-proxy config should exist after cold-start"
        transforms = cfg.get("transforms", [])
        assert transforms and transforms[0].get("name") == "grpc", (
            f"iron-proxy config must delegate policy via a grpc transform; "
            f"got transforms: {transforms}"
        )
        assert transforms[0].get("config", {}).get("target"), (
            f"grpc transform must carry a non-empty dial target; got {transforms[0]}"
        )

        # Baseline: httpbin.org is NOT allowlisted, curl is blocked.
        assert _curl_status(sandbox, "https://httpbin.org/get") != 200, (
            "baseline: httpbin.org must be blocked (only api.github.com is allowlisted)"
        )

        # 2. Add httpbin.org.
        devm.unlock()
        workspace.write_devmyaml(
            no_repo=True,
            install=["true"],
            services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
            network={"allow": ["api.github.com", "httpbin.org"]},
            packages=["curl"],
        )
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
        assert r.returncode == 0, f"reconcile-add failed:\n{r.stderr.decode()}"

        # Respawn + policy update are async; poll for the new host to
        # become reachable.
        deadline = time.monotonic() + 15
        got = 0
        while time.monotonic() < deadline:
            got = _curl_status(sandbox, "https://httpbin.org/get")
            if got == 200:
                break
            time.sleep(0.5)
        assert got == 200, (
            f"reconcile-add should have made httpbin.org reachable from the "
            f"guest; last status {got}"
        )

        # 3. Remove httpbin.org.
        devm.unlock()
        workspace.write_devmyaml(
            no_repo=True,
            install=["true"],
            services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
            network={"allow": ["api.github.com"]},
            packages=["curl"],
        )
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
        assert r.returncode == 0, f"reconcile-remove failed:\n{r.stderr.decode()}"

        # 4. THE assertion: httpbin.org is blocked again from inside the
        # guest. Pre-fix: this fails because reconcile silently no-op'd
        # the removal (diff engine compared against stale snap.Cfg and
        # saw no change) and enforcement kept allowing httpbin.org.
        deadline = time.monotonic() + 15
        got = 200
        while time.monotonic() < deadline:
            got = _curl_status(sandbox, "https://httpbin.org/get")
            if got != 200:
                break
            time.sleep(0.5)
        assert got != 200, (
            f"httpbin.org still reachable from the guest after removal reconcile — "
            f"this is THE bug (updateSnapshotAfterSpawn didn't advance snap.Cfg, "
            f"so subsequent removal diffs against stale baseline and no-ops). "
            f"last status {got}"
        )

        # And the block must be devm's own reject, not some other
        # failure mode — proof the policy authority (not just the
        # snapshot on disk) saw the removal.
        r = sandbox.exec_shell(
            "curl -s -D - -o /dev/null --max-time 5 https://httpbin.org/get"
        )
        assert "x-devm-blocked: egress-policy" in r.stdout.lower(), (
            f"httpbin.org's block after removal must carry devm's marker "
            f"header:\n{r.stdout}"
        )

        # 5. Snapshot's Cfg.Network.Allow also reflects the removal.
        snap_hosts = _snapshot_allow_hosts(workspace.slug)
        assert "httpbin.org" not in snap_hosts, (
            f"snapshot Cfg.Network.Allow still has httpbin.org — merge helper "
            f"didn't advance snap.Cfg. Hosts: {snap_hosts}"
        )
        assert "api.github.com" in snap_hosts, \
            f"api.github.com should still be in snapshot; got {snap_hosts}"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
