"""87: iron-proxy denials are tracked per-project and readable via `devm denials`.

When a workload inside the sandbox tries to reach a host that isn't in
`network.allow`, iron-proxy denies the request. Without a feedback
channel, users would have to grep proxy logs to figure out what to add
to their allow-list — this pins the feedback loop.

What this pins:
  - `devm denials --json` returns the blocked host with a positive count
    after we curl it from inside the sandbox.
  - Multiple hits to the same host accumulate on a single row.
  - Multiple distinct blocked hosts each show up.
  - Allow-listed traffic does NOT get counted.
"""
from __future__ import annotations

import json
import subprocess

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(300)
def test_denials_tracked_per_project(workspace, devm):
    workspace.write_devmyaml(
        install=["true"],
        # Narrow allow-list so google.com / example.com trigger a reject.
        network={"allow": ["api.github.com"]},
    )

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=240,
    )
    assert start.returncode == 0, (
        f"devm start failed: rc={start.returncode}\n"
        f"stderr={start.stderr.decode()!r}"
    )

    # Trigger denials: two hits to google.com, one to example.com.
    # devm_exec_with_retry swallows the known Tart gRPC transport
    # flake ("SendHeader called multiple times") on the wrapper only —
    # curl itself is idempotent so a retry is safe. Without the retry,
    # one flaky exec call means one fewer reject reaches iron-proxy
    # and the count assertion falls short.
    for host, times in [("google.com", 2), ("example.com", 1)]:
        for _ in range(times):
            devm_exec_with_retry(
                devm.path,
                ["curl", "-sf", "-o", "/dev/null",
                 "--max-time", "10", f"https://{host}/"],
                cwd=str(workspace.path),
                timeout=30,
            )

    # Also make an allow-listed request; it must NOT appear in denials.
    devm_exec_with_retry(
        devm.path,
        ["curl", "-sf", "-o", "/dev/null",
         "--max-time", "15", "https://api.github.com/octocat"],
        cwd=str(workspace.path),
        timeout=30,
    )

    p = subprocess.run(
        [devm.path, "denials", "--json"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
    )
    assert p.returncode == 0, (
        f"devm denials failed: rc={p.returncode}\n"
        f"stderr={p.stderr.decode()!r}"
    )
    payload = json.loads(p.stdout.decode())
    by_host = {row["host"]: row for row in payload}

    assert "google.com" in by_host, (
        f"google.com should be recorded as denied; got hosts={list(by_host)}"
    )
    assert by_host["google.com"]["count"] >= 2, (
        f"google.com count should be >=2, got {by_host['google.com']}"
    )
    assert "example.com" in by_host, (
        f"example.com should be recorded as denied; got hosts={list(by_host)}"
    )
    assert by_host["example.com"]["count"] >= 1, (
        f"example.com count should be >=1, got {by_host['example.com']}"
    )
    assert "api.github.com" not in by_host, (
        f"allow-listed api.github.com must not appear as a denial; got {by_host}"
    )

    # Sanity: sorted by count desc.
    counts = [row["count"] for row in payload]
    assert counts == sorted(counts, reverse=True), (
        f"payload must be sorted by count desc; got {counts}"
    )


@pytest.mark.timeout(30)
def test_denials_empty_before_any_requests(workspace, devm):
    """Fresh project (no VM started, no requests) reports zero denials
    without an error — the endpoint returns [] not a 4xx."""
    workspace.write_devmyaml()

    p = subprocess.run(
        [devm.path, "denials", "--json"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
    )
    assert p.returncode == 0, (
        f"devm denials failed on fresh project: rc={p.returncode}\n"
        f"stderr={p.stderr.decode()!r}"
    )
    payload = json.loads(p.stdout.decode())
    assert payload == [], (
        f"fresh project should have no denials; got {payload}"
    )
