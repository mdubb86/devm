"""230: an allowlist edit invalidates the denial rows it resolves, live —
no VM cycle, no iron-proxy respawn.

`devm denials` (test_87) pins the recording side: a blocked host shows up
with a count. This test pins the OTHER half of the contract —
PolicyAuthority.SetAllowlist's replay invalidation (see
internal/serviceapi/policyauthority.go, internal/serviceapi/denials.go's
invalidateResolved): when a live `devm reconcile --yes` widens
network.allow to cover a host that was previously denied, that host's
denial rows disappear from `devm denials --json`, while an unrelated
still-blocked host's rows — and its count — are left completely alone.
The allowlist edit itself is a KindNetworkAdd BucketLive change
(internal/reconcile/apply_live.go's applyNetworkChange -> in-process
PolicyAuthority.SetAllowlist), so this whole flow never touches
iron-proxy or the VM.

What this pins:
  - Two distinct blocked hosts each accumulate their own denial rows.
  - Adding ONE of them to network.allow and reconciling live surfaces
    the change as a live change (`+ allow network ...`), not an
    iron-proxy restart.
  - Only the newly-allowed host's rows are invalidated; the other
    host's row (and its count) survives untouched.
  - The newly-allowed host is actually reachable afterwards.
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


def _denials(devm, workspace) -> list[dict]:
    p = subprocess.run(
        [devm.path, "denials", "--json"],
        cwd=str(workspace.path), capture_output=True, timeout=15,
    )
    assert p.returncode == 0, f"devm denials failed:\n{p.stderr.decode()}"
    return json.loads(p.stdout.decode())


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_denials_replay_invalidation_on_allowlist_edit(workspace, devm, sandbox_name):
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

    # ---- Baseline: two blocked hosts, both recorded. example.org gets
    # hit twice so its count can prove it survives the invalidation
    # untouched; example.net once. ----
    for _ in range(2):
        assert _curl_status(sandbox, "https://example.org/") != 200, (
            "baseline: example.org must be blocked (only example.com is allowlisted)"
        )
    assert _curl_status(sandbox, "https://example.net/") != 200, (
        "baseline: example.net must be blocked (only example.com is allowlisted)"
    )

    before = {row["host"]: row for row in _denials(devm, workspace)}
    assert "example.org" in before and before["example.org"]["count"] >= 2, (
        f"expected >=2 recorded denials for example.org; got {before.get('example.org')}"
    )
    assert "example.net" in before and before["example.net"]["count"] >= 1, (
        f"expected >=1 recorded denial for example.net; got {before.get('example.net')}"
    )
    net_count_before = before["example.net"]["count"]

    # ---- Unlock + widen the allowlist to resolve example.org only. ----
    # Cold-start uchg-locked devm.yaml; unlock before rewriting it.
    subprocess.run(
        [devm.path, "unlock"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    workspace.write_devmyaml(
        no_repo=True,
        network={"allow": ["example.com", "example.org"]},
        packages=["curl"],
    )
    r = subprocess.run(
        [devm.path, "reconcile", "--yes"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    reconcile_out = r.stdout.decode()
    assert r.returncode == 0, f"devm reconcile failed:\n{r.stderr.decode()}"
    assert "+ allow network example.org" in reconcile_out, (
        f"expected the allowlist add to surface as a live change: {reconcile_out!r}"
    )
    # Live path: a network.allow edit dispatches in-process
    # (PolicyAuthority.SetAllowlist) with no iron-proxy respawn and no VM
    # cycle. Cheap markers only -- not pinning the full plan wording.
    assert "iron-proxy" not in reconcile_out.lower(), (
        f"a network.allow live-add must not touch iron-proxy: {reconcile_out!r}"
    )
    assert "restart" not in reconcile_out.lower(), (
        f"a network.allow live-add must not report a restart: {reconcile_out!r}"
    )

    # ---- Poll denials until example.org's rows are gone AND
    # example.net's rows remain, with its count untouched. ----
    deadline = time.monotonic() + 15
    by_host: dict = {}
    while time.monotonic() < deadline:
        by_host = {row["host"]: row for row in _denials(devm, workspace)}
        if "example.org" not in by_host and "example.net" in by_host:
            break
        time.sleep(0.5)
    assert "example.org" not in by_host, (
        f"example.org denial rows should be invalidated once allowlisted; got {by_host}"
    )
    assert "example.net" in by_host, (
        f"example.net denial rows should survive an unrelated allowlist edit; "
        f"got {by_host}"
    )
    assert by_host["example.net"]["count"] == net_count_before, (
        f"example.net's count must be preserved (still blocked, unrelated host); "
        f"before={net_count_before} after={by_host['example.net']['count']}"
    )

    # ---- example.org now gets through. A non-403/non-devm-blocked
    # outcome suffices -- example.org's own server's exact status isn't
    # devm's contract to pin. ----
    deadline = time.monotonic() + 10
    got = 0
    while time.monotonic() < deadline:
        got = _curl_status(sandbox, "https://example.org/")
        if got not in (0, 403):
            break
        time.sleep(0.5)
    assert got not in (0, 403), (
        f"example.org must be reachable after being added to network.allow; got {got}"
    )
