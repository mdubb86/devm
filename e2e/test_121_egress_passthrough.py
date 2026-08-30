"""121: `devm passthrough` / `devm restrict` — time-bounded authority escape hatch.

Enforced-egress baseline: cold-start with `network.allow: [example.com]`
and any curl to a non-allowlisted host (`example.org` here) is rejected
by iron-proxy. `devm passthrough --for 5s` flips the authority mode to
passthrough (iron-proxy remains in the traffic path, MITM'ing + audit-logging
+ secret-substituting); `devm restrict` closes it early. Timer-driven
restore closes it on expiry.

What this pins:
  - passthrough opens: curl to a non-allowlisted host succeeds.
  - restrict closes: curl fails again immediately after `devm restrict`.
  - timer restores: curl fails again after `--for` window expires.
  - status --json reports policy + passthrough_expires_at.
  - reconcile during the window does NOT close it.
"""
from __future__ import annotations

import json
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


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


def _cold_start(devm, workspace, sandbox_name):
    from helpers.tart import TartSandbox
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
    sandbox = TartSandbox(name=sandbox_name)
    assert sandbox.state() == "running", (
        f"expected VM running after cold-start; got {sandbox.state()!r}"
    )
    return sandbox


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_passthrough_open_and_restrict(workspace, devm, sandbox_name):
    """Baseline denies example.org; passthrough allows it; restrict denies again."""
    workspace.write_devmyaml(
        # Opt out of the default repos.main (github.com/octocat/Hello-World):
        # this test's allowlist is deliberately narrow to example.com, and
        # hydration would clone that repo and get blocked by the same
        # egress policy this test exercises.
        no_repo=True,
        network={"allow": ["example.com"]},
        packages=["curl"],
    )
    sandbox = _cold_start(devm, workspace, sandbox_name)

    # ---- Baseline: example.org is NOT allowlisted, curl fails. ----
    assert _curl_status(sandbox, "https://example.org/") != 200, (
        "baseline: example.org must be blocked (only example.com is allowlisted)"
    )

    # ---- Passthrough opens the window; example.org now reachable. ----
    r = subprocess.run(
        [devm.path, "passthrough", "--for", "60s"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"devm passthrough failed:\n{r.stderr.decode()}"

    # Small poll window: setPolicy is async over a UDS.
    deadline = time.monotonic() + 5
    got = 0
    while time.monotonic() < deadline:
        got = _curl_status(sandbox, "https://example.org/")
        if got == 200:
            break
        time.sleep(0.5)
    assert got == 200, f"passthrough must let example.org through; got {got}"

    # ---- Restrict closes early. ----
    r = subprocess.run(
        [devm.path, "restrict"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"devm restrict failed:\n{r.stderr.decode()}"

    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        if _curl_status(sandbox, "https://example.org/") != 200:
            return
        time.sleep(0.5)
    pytest.fail("devm restrict must re-block example.org")


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_passthrough_timer_restores(workspace, devm, sandbox_name):
    """After --for expires, egress is auto-restored to enforced."""
    workspace.write_devmyaml(
        no_repo=True,
        network={"allow": ["example.com"]},
        packages=["curl"],
    )
    sandbox = _cold_start(devm, workspace, sandbox_name)

    r = subprocess.run(
        [devm.path, "passthrough", "--for", "3s"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"devm passthrough failed:\n{r.stderr.decode()}"

    # Wait past deadline + a small margin for timer + softnet write.
    time.sleep(5)
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        if _curl_status(sandbox, "https://example.org/") != 200:
            return
        time.sleep(0.5)
    pytest.fail("timer-driven restore must re-block example.org after --for expires")


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_passthrough_status_json_reports_state(workspace, devm, sandbox_name):
    """`devm status --json` reports egress.policy + passthrough_expires_at."""
    workspace.write_devmyaml(
        no_repo=True,
        network={"allow": ["example.com"]},
        packages=["curl"],
    )
    _cold_start(devm, workspace, sandbox_name)

    def status() -> dict:
        r = subprocess.run(
            [devm.path, "status", "--json"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0, f"devm status failed:\n{r.stderr.decode()}"
        return json.loads(r.stdout)

    # Egress nests under `project`, alongside `routing` — matches the
    # rest of the per-project block in devm status --json.
    before = status()
    before_egress = before.get("project", {}).get("egress", {})
    assert before_egress.get("policy") == "restricted", (
        f"pre-passthrough status must report restricted; got {before_egress!r}"
    )

    r = subprocess.run(
        [devm.path, "passthrough", "--for", "60s"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0

    during = status()
    egress = during.get("project", {}).get("egress") or {}
    assert egress.get("policy") == "passthrough", (
        f"during-passthrough status must report passthrough; got {egress!r}"
    )
    assert egress.get("passthrough_expires_at"), (
        "passthrough_expires_at must be set while a window is open"
    )

    # Clean up: restrict early so the timer doesn't fire post-teardown.
    subprocess.run(
        [devm.path, "restrict"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_passthrough_survives_reconcile(workspace, devm, sandbox_name):
    """`devm reconcile` mid-window does NOT close the passthrough window.

    Passthrough is orthogonal to reconcile — a reconcile that runs mid-
    window updates the underlying allowlist config but leaves the current
    policy state alone.
    """
    workspace.write_devmyaml(
        no_repo=True,
        network={"allow": ["example.com"]},
        packages=["curl"],
    )
    sandbox = _cold_start(devm, workspace, sandbox_name)

    r = subprocess.run(
        [devm.path, "passthrough", "--for", "60s"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0

    # Trigger a reconcile with a trivial env change so it has work to do
    # (a no-op reconcile bails early before touching softnet anyway, but
    # this guarantees the full apply path runs).
    # Cold-start uchg-locked devm.yaml; unlock before rewriting it.
    subprocess.run(
        [devm.path, "unlock"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    workspace.write_devmyaml(
        no_repo=True,
        network={"allow": ["example.com"]},
        packages=["curl"],
        env={"PASSTHROUGH_RECONCILE_MARKER": "1"},
    )
    r = subprocess.run(
        [devm.path, "reconcile", "--yes"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    assert r.returncode == 0, f"devm reconcile failed:\n{r.stderr.decode()}"

    # Window must still be open — example.org still reaches through.
    assert _curl_status(sandbox, "https://example.org/") == 200, (
        "reconcile must NOT close the passthrough window"
    )

    # Clean up.
    subprocess.run(
        [devm.path, "restrict"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
