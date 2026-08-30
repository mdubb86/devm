"""131: network.allow REMOVAL takes effect on iron-proxy config on disk.

The apply-iron-proxy snapshot-Cfg drift bug (fixed in the sibling
commit): before the fix, updateSnapshotAfterSpawn updated only
SecretHashes and ProxyVersion, leaving snap.Cfg frozen. On the next
reconcile the diff engine compared devm.yaml against stale snap.Cfg
and saw no change for removals — iron-proxy config on disk kept the
removed host.

This test exercises the exact remove pattern that surfaced the bug
in production (buzztrack, 2026-07-30):

  1. Cold-start with network.allow = [api.github.com].
  2. Unlock + edit devm.yaml to network.allow = [api.github.com,
     httpbin.org]. Reconcile — apply-iron-proxy fires, iron-proxy
     config gets both hosts, snap.Cfg advances (with the fix).
  3. Unlock + edit devm.yaml back to network.allow = [api.github.com]
     (drop httpbin.org). Reconcile.
  4. Assert iron-proxy config on disk NO LONGER contains httpbin.org.
     Pre-fix: this fails because reconcile silently no-op'd the
     removal and iron-proxy config still lists httpbin.org.
  5. Assert daemon snapshot's Cfg.Network.Allow reflects the removal.

On-disk assertions — no timing, no traffic injection, no external
service dependencies beyond the initial cold-start's need for
reachability. Direct proof of the fix.

Distinct from test_44 and test_130 — those pinned adopt/spawn
mechanics; this pins snapshot-Cfg baseline advancement across
consecutive reconciles.
"""
from __future__ import annotations

import json
import subprocess
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


def _iron_proxy_domains(project_id: str) -> list[str]:
    """The domains list from iron-proxy's on-disk allowlist transform."""
    cfg = _iron_proxy_config(project_id)
    if cfg is None:
        return []
    for transform in cfg.get("transforms", []):
        if transform.get("name") == "allowlist":
            return list(transform.get("config", {}).get("domains", []))
    return []


def _snapshot_allow_hosts(project_id: str) -> list[str]:
    """Hosts in daemon's snapshot Cfg.Network.Allow."""
    path = _runtime_dir() / "state" / f"{project_id}.json"
    if not path.exists():
        return []
    snap = json.loads(path.read_text())
    allow = snap.get("cfg", {}).get("Network", {}).get("Allow", []) or []
    return [entry["Host"] for entry in allow]


@pytest.mark.timeout(420)
@pytest.mark.slow
def test_iron_proxy_allow_removal_takes_effect(devm, workspace, sandbox_name, devm_installed):
    # Initial config with ONE allow entry so cold-start builds a real
    # iron-proxy allow list.
    workspace.write_devmyaml(
        # Opt out of the default repos.main (github.com/octocat/Hello-World):
        # this test's allowlist is narrow (api.github.com only, no wildcard
        # for github.com), and hydration would clone that repo and get
        # blocked by the same egress policy this test exercises.
        no_repo=True,
        install=["true"],
        services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
        network={"allow": ["api.github.com"]},
    )

    try:
        # 1. Cold-start: iron-proxy spawned with the initial allow list.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Baseline: iron-proxy config has exactly api.github.com.
        assert "api.github.com" in _iron_proxy_domains(workspace.slug), \
            "cold-start should have populated iron-proxy config with api.github.com"

        # 2. Add httpbin.org.
        devm.unlock()
        workspace.write_devmyaml(
            no_repo=True,
            install=["true"],
            services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
            network={"allow": ["api.github.com", "httpbin.org"]},
        )
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
        assert r.returncode == 0, f"reconcile-add failed:\n{r.stderr.decode()}"

        domains_after_add = _iron_proxy_domains(workspace.slug)
        assert "httpbin.org" in domains_after_add, \
            f"reconcile-add should have written httpbin.org to iron-proxy config; got {domains_after_add}"

        # 3. Remove httpbin.org.
        devm.unlock()
        workspace.write_devmyaml(
            no_repo=True,
            install=["true"],
            services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
            network={"allow": ["api.github.com"]},
        )
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
        assert r.returncode == 0, f"reconcile-remove failed:\n{r.stderr.decode()}"

        # 4. THE assertion: iron-proxy config no longer has httpbin.org.
        # Pre-fix: this fails because reconcile silently no-op'd the removal
        # (diff engine compared against stale snap.Cfg and saw no change).
        domains_after_remove = _iron_proxy_domains(workspace.slug)
        assert "httpbin.org" not in domains_after_remove, (
            f"iron-proxy config still has httpbin.org after removal reconcile — "
            f"this is THE bug (updateSnapshotAfterSpawn didn't advance snap.Cfg, "
            f"so subsequent removal diffs against stale baseline and no-ops).\n"
            f"domains on disk: {domains_after_remove}"
        )
        assert "api.github.com" in domains_after_remove, \
            f"api.github.com should still be present; got {domains_after_remove}"

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
