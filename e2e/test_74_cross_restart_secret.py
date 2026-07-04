"""74: host-scoped secret survives a daemon restart.

Pin: a secret set via `devm secret set`, referenced in devm.yaml via
`!secret`, is (a) wired into iron-proxy at cold-start and (b) still
accessible after a daemon stop+restart cycle — specifically the
iron-proxy config file retains the secret transform and the adopted
process keeps the substitution mapping intact.

DESIGN REFRAME (vs. original brief):
The original brief proposed a Mac-side echo server on an ephemeral port
to capture the Authorization header after iron-proxy substitutes the
real secret value in outbound requests from the VM. That approach
cannot work with the transparent-proxy model:

  - The VM's nftables only DNAT ports 80 and 443 to iron-proxy.
  - An echo server bound to an arbitrary ephemeral port on the Mac is
    unreachable via 127.0.0.1:test_port from inside the VM (127.0.0.1
    is the VM's own loopback; no DNAT applies for the ephemeral port).
  - Port 80 on the Mac requires root, which is unavailable in tests.

Instead, this test pins the invariants that are directly observable
across the restart boundary:

  1. After cold-start, the iron-proxy config file contains the `secrets`
     transform wired to 127.0.0.1, confirming the daemon built and
     wrote the correct config from the CLI-resolved keychain value.
  2. The VM's AUTH_TOKEN env var holds the opaque token form
     (__DEVM_SECRET_<name>__) rather than the real value — confirming
     the token-placeholder wiring is correct.
  3. After `devm service restart`, iron-proxy is adopted (same PID;
     independent adoption contract pinned by test_44). The config file
     is intact and a second `devm shell` still works with the same
     token form in env — confirming no secret configuration was lost
     across the restart.
"""
import subprocess
import time
import textwrap
from pathlib import Path

import pytest
import yaml

pytestmark = pytest.mark.devm

_RUNDIR = Path.home() / "Library" / "Application Support" / "devm"


def _iron_proxy_pid_for(project_id: str) -> int | None:
    """Return the PID of the iron-proxy process for this project, or None."""
    r = subprocess.run(
        ["ps", "-axo", "pid=,command="],
        capture_output=True, text=True, check=True,
    )
    needle = f"/iron-proxy/{project_id}.yaml"
    for line in r.stdout.splitlines():
        if needle in line:
            return int(line.strip().split(None, 1)[0])
    return None


def _iron_proxy_config(project_id: str) -> dict | None:
    """Return parsed iron-proxy config YAML for the project, or None if absent."""
    path = _RUNDIR / "iron-proxy" / f"{project_id}.yaml"
    if not path.exists():
        return None
    return yaml.safe_load(path.read_text())


def _has_secret_transform(config: dict, secret_name: str) -> bool:
    """Return True if the iron-proxy config has a secrets transform for secret_name.

    The transform encodes the secret via its env-var name (DEVM_SECRET_<NAME>),
    never the real value — confirming the secret was wired at spawn time.
    """
    env_var = f"DEVM_SECRET_{secret_name.upper()}"
    for transform in config.get("transforms", []):
        if transform.get("name") != "secrets":
            continue
        for entry in transform.get("config", {}).get("secrets", []):
            if entry.get("source", {}).get("var") == env_var:
                return True
    return False


@pytest.mark.timeout(420)
@pytest.mark.slow
def test_secret_survives_daemon_restart(workspace, devm, sandbox_name, devm_installed):
    secret_name = f"e2e_secret_{sandbox_name.replace('-', '_')}"
    secret_value = "s3kr3tv4l"
    token_form = f"__DEVM_SECRET_{secret_name}__"

    # The workspace fixture already wrote a minimal devm.yaml (with the
    # correct project.id), so `devm secret set` can locate the project.
    proc = subprocess.run(
        [devm.path, "secret", "set", secret_name],
        input=secret_value.encode() + b"\n",
        capture_output=True, timeout=15,
        cwd=str(workspace.path),
    )
    assert proc.returncode == 0, proc.stderr.decode()

    try:
        # Rewrite devm.yaml with the !secret reference and network.allow
        # binding. yaml.safe_dump cannot emit YAML tags, so we write
        # the file directly.
        workspace.devmyaml_path.write_text(textwrap.dedent(f"""\
            project:
              id: {workspace.slug}
              vm_name: {workspace.vm_name}
            env:
              AUTH_TOKEN: !secret {secret_name}
            network:
              allow:
              - host: 127.0.0.1
                secrets:
                - {secret_name}
        """))

        # Cold-start: CLI resolves the secret from the macOS keychain and
        # sends the resolved value to the daemon, which writes the
        # iron-proxy config file and spawns iron-proxy with
        # DEVM_SECRET_<NAME>=<value> in its process env.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path),
            capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Verify: iron-proxy config file has the secrets transform.
        cfg = _iron_proxy_config(workspace.slug)
        assert cfg is not None, (
            f"iron-proxy config not found for project {workspace.slug!r}"
        )
        assert _has_secret_transform(cfg, secret_name), (
            f"iron-proxy config lacks secrets transform for {secret_name!r} "
            f"after cold-start; config={cfg}"
        )

        pid_before = _iron_proxy_pid_for(workspace.slug)
        assert pid_before is not None, (
            "iron-proxy should be running after cold-start"
        )

        # Verify: the VM's AUTH_TOKEN env var is the opaque token form,
        # not the real secret value (substitution only happens in headers
        # of outbound HTTP requests that pass through iron-proxy).
        r = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c", "echo $AUTH_TOKEN"],
            cwd=str(workspace.path),
            capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"pre-restart env check failed:\n{r.stderr.decode()}"
        env_val = r.stdout.decode().strip()
        assert env_val == token_form, (
            f"AUTH_TOKEN should be token form {token_form!r}, got {env_val!r}"
        )

        # Restart the daemon. `service restart` shells out to sudo internally
        # for launchctl kickstart; sudo_capable has verified /dev/tty is
        # available for prompting.
        r = subprocess.run(
            [devm.path, "service", "restart"],
            capture_output=True, timeout=60,
        )
        assert r.returncode == 0, (
            f"service restart failed: {r.stderr.decode()!r}"
        )

        # Give the new daemon a moment to run DiscoverIronProxies + Adopt.
        time.sleep(2)

        # Iron-proxy must be the same process after restart (adoption).
        pid_after = _iron_proxy_pid_for(workspace.slug)
        assert pid_after == pid_before, (
            f"iron-proxy PID changed across daemon restart: "
            f"before={pid_before} after={pid_after}"
        )

        # Config file must be intact with the same secrets transform.
        cfg_after = _iron_proxy_config(workspace.slug)
        assert cfg_after is not None, (
            "iron-proxy config file missing after daemon restart"
        )
        assert _has_secret_transform(cfg_after, secret_name), (
            f"iron-proxy config lost secrets transform after daemon restart; "
            f"config={cfg_after}"
        )

        # Second devm shell after restart: VM still reachable and
        # AUTH_TOKEN is still the token form (the adopted iron-proxy
        # retains its process env, so substitution config is intact).
        r = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c", "echo $AUTH_TOKEN"],
            cwd=str(workspace.path),
            capture_output=True, timeout=60,
        )
        assert r.returncode == 0, (
            f"post-restart devm shell failed:\n{r.stderr.decode()}"
        )
        env_val_after = r.stdout.decode().strip()
        assert env_val_after == token_form, (
            f"AUTH_TOKEN changed after restart: {env_val_after!r} "
            f"(expected {token_form!r})"
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
