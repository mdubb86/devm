"""102: `devm reconcile` heals a missing iron-proxy WITHOUT losing secret wiring.

Structural sibling of test_101 for the secret-bearing case, and a
kill+reconcile sibling of test_74 (which pins the same invariants
across a full daemon restart rather than a proxy kill+reconcile).
Structure only — see test_74's docstring for why value-delivery isn't
observable e2e.

Sequence:
  1. Plant a secret, reference it via `!secret` scoped to a host.
  2. Cold-start — confirm the on-disk iron-proxy config has the
     `secrets` transform and the VM env holds the opaque
     `__DEVM_SECRET_<name>__` token.
  3. Cross the adoption seam (`restart_isolated_daemon`), kill the
     proxy, confirm it stays dead.
  4. `devm reconcile` — the CLI resolves the secret from the keychain
     again and re-wires it into the healed iron-proxy. Assert the
     healed on-disk config still has the transform, and the VM env
     still reports the same token form.
"""
from __future__ import annotations

import subprocess
import textwrap
import time
from pathlib import Path

import pytest
import yaml

from helpers.proxy import kill_project_proxy, project_proxy_running

pytestmark = pytest.mark.devm

_RUNDIR_CANDIDATES = [
    Path.home() / "Library" / "Application Support" / "devm",
]


def _runtime_dir() -> Path:
    """Resolve the runtime dir the daemon under test writes into.

    Isolated e2e mode points DEVM_RUNTIME_DIR at a private directory
    (see conftest.restart_isolated_daemon); install mode uses the
    real Application Support path. Mirrors test_74's _RUNDIR but
    honors the isolated-mode override so this test works under
    `just e2e-one` (E2E_ISOLATE=1 by default).
    """
    import os
    env = os.environ.get("DEVM_RUNTIME_DIR")
    if env:
        return Path(env)
    return _RUNDIR_CANDIDATES[0]


def _iron_proxy_config(project_id: str) -> dict | None:
    path = _runtime_dir() / "iron-proxy" / f"{project_id}.yaml"
    if not path.exists():
        return None
    return yaml.safe_load(path.read_text())


def _has_secret_transform(config: dict, secret_name: str) -> bool:
    env_var = f"DEVM_SECRET_{secret_name.upper()}"
    for transform in config.get("transforms", []):
        if transform.get("name") != "secrets":
            continue
        for entry in transform.get("config", {}).get("secrets", []):
            if entry.get("source", {}).get("var") == env_var:
                return True
    return False


@pytest.mark.timeout(300)
def test_reconcile_heals_secret_structure(devm, workspace, sandbox_name, devm_installed, restart_isolated_daemon):
    secret_name = f"e2e_secret_{sandbox_name.replace('-', '_')}"
    secret_value = "s3kr3tv4l"
    token_form = f"__DEVM_SECRET_{secret_name}__"

    proc = subprocess.run(
        [devm.path, "secret", "set", secret_name],
        input=secret_value.encode() + b"\n",
        capture_output=True, timeout=15,
        cwd=str(workspace.path),
    )
    assert proc.returncode == 0, proc.stderr.decode()

    try:
        workspace.devmyaml_path.write_text(textwrap.dedent(f"""\
            project:
              name: {workspace.vm_name}
            env:
              AUTH_TOKEN: !secret {secret_name}
            network:
              allow:
              - host: 127.0.0.1
                secrets:
                - {secret_name}
        """))

        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        cfg = _iron_proxy_config(workspace.slug)
        assert cfg is not None, f"iron-proxy config not found for {workspace.slug!r}"
        assert _has_secret_transform(cfg, secret_name), (
            f"iron-proxy config lacks secrets transform after cold-start; config={cfg}"
        )

        r = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c", "echo $AUTH_TOKEN"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"pre-kill env check failed:\n{r.stderr.decode()}"
        assert r.stdout.decode().strip() == token_form, (
            f"AUTH_TOKEN should be token form {token_form!r}, got {r.stdout.decode()!r}"
        )

        # Cross the adoption seam, then kill the proxy and confirm it
        # stays dead (same reasoning as test_101).
        restart_isolated_daemon()
        assert project_proxy_running(workspace.slug), (
            "iron-proxy should survive the daemon restart (adopted)"
        )
        kill_project_proxy(workspace.slug)
        assert not project_proxy_running(workspace.slug)
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

        # Structure: healed config still carries the secrets transform.
        cfg_after = _iron_proxy_config(workspace.slug)
        assert cfg_after is not None, "iron-proxy config missing after reconcile heal"
        assert _has_secret_transform(cfg_after, secret_name), (
            f"healed iron-proxy config lost the secrets transform for "
            f"{secret_name!r}; config={cfg_after}"
        )

        # And the VM still sees the same opaque token form — the
        # reconcile-resolved secret was wired back in correctly.
        r = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c", "echo $AUTH_TOKEN"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"post-heal env check failed:\n{r.stderr.decode()}"
        env_val_after = r.stdout.decode().strip()
        assert env_val_after == token_form, (
            f"AUTH_TOKEN changed after heal: {env_val_after!r} (expected {token_form!r})"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        subprocess.run(
            [devm.path, "secret", "delete", secret_name],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
