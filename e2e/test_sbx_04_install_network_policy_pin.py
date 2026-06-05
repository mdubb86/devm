"""sbx-04: characterize allowedDomains enforcement at install: time.

Pure-sbx test (no devm). Empirically pins how sbx treats
network.allowedDomains during the install: phase — which turned out
NOT to match my initial theory of the 2026-06-05 dogfood failure.

What this test originally expected:
  Theory: `install: apt-get update` would fail under a tight
  allowed_domains list that didn't include the distro's apt mirrors,
  because sbx enforces the policy from the moment the container
  starts.

What this test actually found:
  Apt-get update SUCCEEDED under a tight allowed_domains list
  containing ONLY github.com (no apt mirrors). Two possible
  explanations:
    * sbx doesn't enforce allowedDomains during install: phase
      (policy comes up later, e.g. before the entrypoint runs)
    * sbx has an internal always-allow set covering distro
      essentials (apt, certs, etc.) that allowedDomains augments
      rather than replaces

  Either way, devm does NOT need to "always-include the Ubuntu
  mirrors" in the rendered spec.yaml — sbx is already letting apt
  through. The test stays as a property pin: if sbx ever tightens
  install: policy enforcement, we'll see this test break and know
  to add devm-mandated apt defaults.
"""
from __future__ import annotations
import os
import subprocess
import tempfile
import textwrap
import time

import pytest

from helpers import sbx


# Canonical Ubuntu apt repository domains (Ubuntu 26.04 base — what
# docker/sandbox-templates:shell currently ships). Update this list if
# the base image changes distro.
UBUNTU_APT_DOMAINS = [
    "archive.ubuntu.com",
    "security.ubuntu.com",
    "ports.ubuntu.com",
]


def _kit_spec(allowed_domains: list[str]) -> str:
    domains_yaml = "\n".join(f"    - {d}" for d in allowed_domains)
    return textwrap.dedent(f"""\
        schemaVersion: "1"
        kind: agent
        name: aptprobe
        displayName: install-time apt probe
        description: pure-sbx test of apt-get requirements under a policy
        agent:
          image: docker/sandbox-templates:shell
          aiFilename: CLAUDE.md
          persistence: persistent
          entrypoint:
            run: ["sh", "-c", "exec sleep infinity </dev/null"]
        network:
          allowedDomains:
{domains_yaml}
        environment:
          variables:
            IS_SANDBOX: "1"
        commands:
          install:
            - command: 'apt-get update'
          startup:
            - command: ['sh', '-c', 'true']
              user: "1000"
              description: noop
    """)


def _materialize_kit(allowed_domains: list[str]) -> tuple[str, str]:
    workspace = tempfile.mkdtemp(prefix="sbx-aptprobe-ws-")
    kit_dir = tempfile.mkdtemp(prefix="sbx-aptprobe-kit-")
    with open(os.path.join(kit_dir, "spec.yaml"), "w") as f:
        f.write(_kit_spec(allowed_domains))
    return workspace, kit_dir


def _cleanup(workspace: str, kit_dir: str) -> None:
    import shutil
    shutil.rmtree(workspace, ignore_errors=True)
    shutil.rmtree(kit_dir, ignore_errors=True)


def _run_until_done(sandbox_name: str, workspace: str, kit_dir: str,
                    timeout: float = 90.0) -> tuple[int, str]:
    """Spawn `sbx run` and either wait until the sandbox is exec-ready
    OR until sbx run exits non-zero. Returns (final_rc, stderr).

    final_rc < 0 sentinel = sandbox reached exec-ready, sbx run still
    alive — we then kill it deliberately and return -1.
    """
    proc = subprocess.Popen(
        ["sbx", "run",
         "--kit", kit_dir,
         "--name", sandbox_name,
         "aptprobe",
         workspace],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    deadline = time.monotonic() + timeout
    try:
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                # sbx run exited — capture and return.
                stderr = proc.stderr.read().decode() if proc.stderr else ""
                return proc.returncode, stderr
            # Did the sandbox reach exec-ready? (Apt succeeded path.)
            if sbx.sandbox_state(sandbox_name) == "running":
                p = subprocess.run(
                    ["sbx", "exec", sandbox_name, "true"],
                    capture_output=True, timeout=5,
                )
                if p.returncode == 0:
                    # Bringup OK — apt worked.
                    return -1, ""
            time.sleep(0.5)
        # Timed out without either signal.
        return -2, "timeout waiting for outcome"
    finally:
        try:
            proc.kill()
        except Exception:
            pass
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            pass


@pytest.mark.timeout(240)
def test_permissive_policy_allows_apt(sandbox_name):
    """Apt mirrors in allowed_domains → install succeeds, sandbox up."""
    workspace, kit_dir = _materialize_kit(
        allowed_domains=["github.com"] + UBUNTU_APT_DOMAINS,
    )
    try:
        rc, stderr = _run_until_done(sandbox_name, workspace, kit_dir)
        assert rc == -1, (
            f"expected exec-ready (apt succeeded) but got rc={rc}; "
            f"stderr:\n{stderr}"
        )
    finally:
        subprocess.run(["sbx", "stop", sandbox_name], capture_output=True, timeout=15)
        subprocess.run(["sbx", "rm", "-f", sandbox_name], capture_output=True, timeout=15)
        _cleanup(workspace, kit_dir)


@pytest.mark.timeout(240)
def test_tight_policy_still_lets_apt_through_at_install(sandbox_name):
    """Empirical: apt-get update SUCCEEDS even when allowedDomains
    excludes every apt mirror. Either install: runs before the network
    policy is enforced, or sbx has an internal essentials-allow set
    that covers apt. Locks the current behavior so any future sbx
    change here is loud."""
    workspace, kit_dir = _materialize_kit(
        allowed_domains=["github.com"],  # NO apt mirrors
    )
    try:
        rc, stderr = _run_until_done(sandbox_name, workspace, kit_dir)
        assert rc == -1, (
            f"sbx tightened install-time policy: apt-get update now fails "
            f"under a tight allowedDomains list (rc={rc}, stderr={stderr!r}). "
            f"devm must now mandate the distro mirrors in render/spec.go. "
            f"Update bootstrap.sh's strict failure mode and add the default "
            f"allow list."
        )
    finally:
        subprocess.run(["sbx", "stop", sandbox_name], capture_output=True, timeout=15)
        subprocess.run(["sbx", "rm", "-f", sandbox_name], capture_output=True, timeout=15)
        _cleanup(workspace, kit_dir)
