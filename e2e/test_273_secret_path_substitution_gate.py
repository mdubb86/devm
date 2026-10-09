"""273: the path-substitution gate rejects secrets whose value contains '/'.

iron-proxy substitutes __DEVM_SECRET_<name>__ placeholders in URL paths
(match_path). A bound value containing '/' would misroute the request,
so the policy authority answers 400 with X-Devm-Secret-Reject:
path-unsafe and X-Devm-Secret-Name: <name> instead of forwarding.

Three pins:
  - unsafe value in the path  -> devm-authored 400, value never echoed
  - safe value in the path    -> substituted, upstream sees it
  - unsafe value in the query -> substituted (query values are
    re-encoded after substitution), gate does not fire
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm

HOST = "httpbin.org"


def _setup(devm, workspace, secret_name: str, value: str) -> None:
    proc = subprocess.run(
        [devm.path, "secret", "set", secret_name],
        input=value.encode() + b"\n",
        capture_output=True, timeout=15, cwd=str(workspace.path),
    )
    assert proc.returncode == 0, f"secret set failed:\n{proc.stderr.decode()}"
    workspace.devm_yaml_path.write_text(
        f"""project:
  name: {workspace.slug}

env:
  GATE_TOKEN: !secret {secret_name}

network:
  allow:
    - host: {HOST}
      secrets: [{secret_name}]
"""
    )
    r = subprocess.run(
        [devm.path, "start"], cwd=str(workspace.path),
        capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"


def _guest_curl(devm, workspace, url: str) -> str:
    """Response headers + body of a GET issued from inside the guest."""
    r = subprocess.run(
        [devm.path, "exec", "bash", "-c", f"curl -sS -i --max-time 30 '{url}'"],
        cwd=str(workspace.path), capture_output=True, timeout=90,
    )
    assert r.returncode == 0, f"guest curl failed:\n{r.stderr.decode()}"
    return r.stdout.decode()


def _teardown(devm, workspace, secret_name: str) -> None:
    subprocess.run(
        [devm.path, "teardown", "--yes"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    subprocess.run(
        [devm.path, "secret", "delete", secret_name],
        cwd=str(workspace.path), capture_output=True, timeout=15,
    )


@pytest.mark.timeout(420)
def test_unsafe_secret_in_path_is_rejected(devm, workspace, sandbox_name, devm_installed):
    secret_name = f"e2e_secret_{sandbox_name.replace('-', '_')}"
    value = "ab/cd-unsafe"
    try:
        _setup(devm, workspace, secret_name, value)
        out = _guest_curl(
            devm, workspace,
            f"https://{HOST}/anything/__DEVM_SECRET_{secret_name}__/x",
        )
        assert " 400" in out.splitlines()[0], out
        assert "x-devm-secret-reject: path-unsafe" in out.lower(), out
        assert f"x-devm-secret-name: {secret_name}".lower() in out.lower(), out
        assert "misroute the request to a" in out, out
        assert value not in out, "secret value leaked into reject response"
    finally:
        _teardown(devm, workspace, secret_name)


@pytest.mark.timeout(420)
def test_safe_secret_in_path_is_substituted(devm, workspace, sandbox_name, devm_installed):
    secret_name = f"e2e_secret_{sandbox_name.replace('-', '_')}"
    value = "safevaluenoslash"
    try:
        _setup(devm, workspace, secret_name, value)
        out = _guest_curl(
            devm, workspace,
            f"https://{HOST}/anything/__DEVM_SECRET_{secret_name}__/x",
        )
        assert " 200" in out.splitlines()[0], out
        assert "x-devm-secret-reject" not in out.lower(), out
        assert f"/anything/{value}/x" in out, out
    finally:
        _teardown(devm, workspace, secret_name)


@pytest.mark.timeout(420)
def test_unsafe_secret_in_query_is_substituted(devm, workspace, sandbox_name, devm_installed):
    secret_name = f"e2e_secret_{sandbox_name.replace('-', '_')}"
    value = "ab/cd-unsafe"
    try:
        _setup(devm, workspace, secret_name, value)
        out = _guest_curl(
            devm, workspace,
            f"https://{HOST}/anything?token=__DEVM_SECRET_{secret_name}__",
        )
        assert " 200" in out.splitlines()[0], out
        assert "x-devm-secret-reject" not in out.lower(), out
        assert f"__DEVM_SECRET_{secret_name}__" not in out, out
    finally:
        _teardown(devm, workspace, secret_name)
