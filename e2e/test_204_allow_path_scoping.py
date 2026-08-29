"""204: Path-scoped network.allow entries gate egress by request path.

Run against the bootstrapped devm-e2e install via `just e2e`.

An allow entry may carry a path pattern after the hostname
("host/path" or "host/path/*"). devm emits such entries as iron-proxy
allowlist `rules` ({host, paths}) instead of `domains`, so the host is
reachable only on matching paths.

  1. Cold-start with network.allow = [api.github.com/octocat].
  2. On-disk iron-proxy config: the entry appears as a rules entry
     with host api.github.com and path /octocat; the domains list does
     NOT contain any api.github.com form.
  3. Guest curl to the allowed path returns 200.
  4. Guest curl to a different path on the SAME host is rejected.
"""
from __future__ import annotations

import subprocess
from pathlib import Path

import pytest
import yaml

pytestmark = pytest.mark.devm


def _iron_proxy_allowlist_cfg(project_id: str) -> dict:
    path = (
        Path.home() / "Library" / "Application Support" / "devm-e2e"
        / "iron-proxy" / f"{project_id}.yaml"
    )
    assert path.exists(), f"expected iron-proxy config at {path}"
    cfg = yaml.safe_load(path.read_text())
    for transform in cfg.get("transforms", []):
        if transform.get("name") == "allowlist":
            return transform.get("config", {})
    raise AssertionError(f"no allowlist transform in {path}")


def _guest_curl_status(devm, workspace, url: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        [devm.path, "shell", "--", "curl", "-sf", "-o", "/dev/null",
         "-w", "%{http_code}", "--max-time", "15", url],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=30,
    )


@pytest.mark.slow
@pytest.mark.timeout(420)
def test_allow_path_scoping(devm, workspace):
    # no_repo=True so the fixture doesn't auto-inject the default
    # Hello-World repo — that would need github.com in the allow list,
    # unrelated to what this test pins.
    workspace.write_devmyaml(
        no_repo=True,
        install=["true"],
        network={"allow": ["api.github.com/octocat"]},
    )

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    # On-disk mechanism: rules entry, and no api.github.com in domains.
    allow_cfg = _iron_proxy_allowlist_cfg(workspace.slug)
    rules = allow_cfg.get("rules", [])
    assert {"host": "api.github.com", "paths": ["/octocat"]} in rules, (
        f"expected path rule in iron-proxy config, got rules={rules!r}"
    )
    domains = allow_cfg.get("domains", [])
    assert not any("api.github.com" in d for d in domains), (
        f"path-scoped host must not appear in domains, got {domains!r}"
    )

    # Allowed path reaches upstream.
    r = _guest_curl_status(devm, workspace, "https://api.github.com/octocat")
    assert r.returncode == 0 and r.stdout.strip() == b"200", (
        f"allowed path returned rc={r.returncode} status={r.stdout!r} "
        f"(stderr: {r.stderr.decode()})"
    )

    # Different path on the same host is rejected. curl -sf exits
    # non-zero both for iron-proxy's HTTP reject and for connection
    # failures — either way the request must not succeed.
    r = _guest_curl_status(devm, workspace, "https://api.github.com/meta")
    assert r.returncode != 0, (
        "disallowed path on an allow-listed host should have been "
        f"blocked but curl returned 0 (status={r.stdout!r})"
    )
