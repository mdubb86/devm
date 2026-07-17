"""101: `devm reconcile` is the ONLY thing that heals a missing iron-proxy
— and it does so without losing secret wiring, while adoption itself
never churns a still-alive proxy.

Merges what were test_101 (plain heal), test_102 (secret-structure
survives heal), and test_106 (adopted-but-alive proxy isn't churned)
— all three share the same cold-start/cross-adoption-seam choreography.
Uses test_102's config shape (secret + host-scoped allow) as a superset
of test_101's plain-allow case, with the allow-listed host pointed at
a real reachable one (api.github.com, per test_43's proven-reliable
host) instead of 127.0.0.1, so test_101's post-heal functional curl
check still applies.

Pins the collapsed self-heal model (no daemon-start auto-heal, no
on-demand heal from `shell`/`status`/etc — see test_103 for those).
Sequence:

  1. Plant a secret, reference it via `!secret` scoped to an
     allow-listed, reachable host (api.github.com).
  2. Cold-start — confirm the on-disk iron-proxy config has the
     `secrets` transform and the VM env holds the opaque
     `__DEVM_SECRET_<name>__` token. Capture the proxy PID.
  3. `restart_isolated_daemon()` — the proxy becomes *adopted*. A
     verified spike (test_100) found the supervisor does NOT
     auto-restart an adopted proxy if it dies, only a freshly-spawned
     one — so we must cross this seam before killing it, or the
     supervisor would silently undo the kill and the test would prove
     nothing. Assert the PID is UNCHANGED across this restart — this
     is test_106's entire assertion, for free, in the same boot,
     before the kill happens: an adopted-but-alive proxy must not be
     churned.
  4. Kill the (now adopted) iron-proxy; confirm it stays dead.
  5. `devm reconcile` — assert it heals: the proxy PID reappears, the
     healed on-disk config still carries the secrets transform, the VM
     env still reports the same opaque token form, AND the
     allow-listed host reaches upstream through the healed proxy again
     (mirrors test_43's egress check).
"""
from __future__ import annotations

import subprocess
import textwrap
import time
from pathlib import Path

import pytest
import yaml

from helpers.exec_retry import devm_exec_with_retry
from helpers.proxy import kill_project_proxy, project_proxy_pid, project_proxy_running

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
def test_reconcile_heals_missing_proxy(devm, workspace, sandbox_name, devm_installed, restart_isolated_daemon):
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
              - host: api.github.com
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

        pid_before = project_proxy_pid(workspace.slug)
        assert pid_before is not None, (
            f"iron-proxy should be running for project {workspace.slug!r} after cold-start"
        )

        # Cross the adoption seam: a freshly-spawned proxy WOULD be
        # auto-restarted by the supervisor if killed; an adopted one
        # won't. Also asserts the negative-space case (test_106): an
        # adopted-but-alive proxy must not be churned by adoption
        # itself.
        restart_isolated_daemon()
        assert project_proxy_running(workspace.slug), (
            "iron-proxy should survive the daemon restart (adopted)"
        )
        pid_after_adopt = project_proxy_pid(workspace.slug)
        assert pid_after_adopt == pid_before, (
            f"adopted-but-alive iron-proxy was churned across daemon restart: "
            f"before={pid_before} after={pid_after_adopt} — it should be left "
            f"alone, not respawned"
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
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        subprocess.run(
            [devm.path, "secret", "delete", secret_name],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
