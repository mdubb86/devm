"""132: !secret ref REMOVAL takes effect on iron-proxy config on disk.

Same class of bug as test_131: pre-fix, updateSnapshotAfterSpawn
didn't advance snap.Cfg for secrets either, so removing a !secret
reference from env: silently no-op'd on the subsequent reconcile
(iron-proxy config on disk kept its secrets transform for the
removed reference).

Sequence:
  1. Plant a secret in the keychain via `devm secret set NAME`.
  2. Cold-start with env: NAME_ENV: !secret NAME scoped to a
     network.allow host.
  3. Assert iron-proxy config on disk has a secrets transform for NAME.
  4. Unlock + edit devm.yaml to drop the !secret NAME reference
     (either remove NAME_ENV entirely or replace with a literal).
     Reconcile.
  5. Assert iron-proxy config on disk NO LONGER has the secrets
     transform for NAME.
  6. Assert snap.Cfg.Env / Services[*].Env no longer carries the
     secret ref for the removed name.

Same on-disk determinism as test_131.
"""
from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest
import yaml

pytestmark = pytest.mark.devm


def _runtime_dir() -> Path:
    return Path.home() / "Library" / "Application Support" / "devm-e2e"


def _iron_proxy_config(project_id: str) -> dict | None:
    path = _runtime_dir() / "iron-proxy" / f"{project_id}.yaml"
    if not path.exists():
        return None
    return yaml.safe_load(path.read_text())


def _has_secret_transform_for(project_id: str, secret_name: str) -> bool:
    """True iff iron-proxy config on disk has a secrets transform bound to secret_name."""
    cfg = _iron_proxy_config(project_id)
    if cfg is None:
        return False
    env_var = f"DEVM_SECRET_{secret_name.upper()}"
    for transform in cfg.get("transforms", []):
        if transform.get("name") != "secrets":
            continue
        for entry in transform.get("config", {}).get("secrets", []):
            if entry.get("source", {}).get("var") == env_var:
                return True
    return False


def _snapshot_secret_names(project_id: str) -> set[str]:
    """Secret names referenced anywhere in snap.Cfg.Env / Services[*].Env."""
    path = _runtime_dir() / "state" / f"{project_id}.json"
    if not path.exists():
        return set()
    snap = json.loads(path.read_text())
    cfg = snap.get("cfg", {})
    names: set[str] = set()

    def collect(env_map: dict | None) -> None:
        if not env_map:
            return
        for v in env_map.values():
            if isinstance(v, dict) and v.get("Secret"):
                sec = v["Secret"]
                if isinstance(sec, dict) and sec.get("Name"):
                    names.add(sec["Name"])

    collect(cfg.get("Env"))
    for svc in (cfg.get("Services") or {}).values():
        if isinstance(svc, dict):
            collect(svc.get("Env"))
    return names


@pytest.mark.timeout(420)
@pytest.mark.slow
def test_iron_proxy_secret_removal_takes_effect(devm, workspace, sandbox_name, devm_installed):
    secret_name = f"e2e_secret_{sandbox_name.replace('-', '_')}"
    secret_value = "s3kr3tv4l"

    # Plant the secret in the keychain — the CLI reads it back at
    # reconcile time to compose the request to the daemon.
    proc = subprocess.run(
        [devm.path, "secret", "set", secret_name],
        input=secret_value.encode() + b"\n",
        capture_output=True, timeout=15,
        cwd=str(workspace.path),
    )
    assert proc.returncode == 0, f"secret set failed:\n{proc.stderr.decode()}"

    workspace.devmyaml_path.write_text(
        f"""project:
  name: {workspace.slug}

install:
  - "true"

services:
  sleep:
    exec: ["/bin/sleep", "infinity"]
    restart: always

network:
  allow:
    - host: api.github.com
      secrets: [{secret_name}]

env:
  MY_SECRET_ENV: !secret {secret_name}
"""
    )

    try:
        # 1. Cold-start with the secret ref present.
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Baseline: iron-proxy config on disk has the secrets transform.
        assert _has_secret_transform_for(workspace.slug, secret_name), (
            f"cold-start should have written a secrets transform for "
            f"{secret_name!r} to iron-proxy config"
        )
        assert secret_name in _snapshot_secret_names(workspace.slug), \
            "cold-start snapshot should carry the secret ref"

        # 2. Remove the !secret ref by rewriting devm.yaml without it.
        devm.unlock()
        workspace.devmyaml_path.write_text(
            f"""project:
  name: {workspace.slug}

install:
  - "true"

services:
  sleep:
    exec: ["/bin/sleep", "infinity"]
    restart: always

network:
  allow:
    - api.github.com

env:
  MY_LITERAL_ENV: "no-secret"
"""
        )
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
        assert r.returncode == 0, f"reconcile-remove failed:\n{r.stderr.decode()}"

        # 3. THE assertion: iron-proxy config no longer has the secrets
        # transform for the removed secret. Pre-fix: this fails because
        # reconcile silently no-op'd the removal.
        assert not _has_secret_transform_for(workspace.slug, secret_name), (
            f"iron-proxy config on disk still has secrets transform for "
            f"{secret_name!r} after removal reconcile — this is THE bug "
            f"(updateSnapshotAfterSpawn didn't advance snap.Cfg for secret "
            f"refs, so subsequent removal diffs against stale baseline and "
            f"no-ops)."
        )

        # 4. Snapshot no longer carries the secret ref.
        snap_names = _snapshot_secret_names(workspace.slug)
        assert secret_name not in snap_names, (
            f"snapshot still has secret ref {secret_name!r} after removal; "
            f"got names: {snap_names}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        # Keychain cleanup via devm's first-class subcommand — same
        # account naming as `devm secret set` (projectID/secretName under
        # kSecAttrService="devm"). Matches test_101's cleanup pattern.
        # Runs after teardown; workspace.path + devm.yaml still exist so
        # `currentProjectID()` can read the project name.
        subprocess.run(
            [devm.path, "secret", "delete", secret_name],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
