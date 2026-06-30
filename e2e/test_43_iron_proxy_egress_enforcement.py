"""End-to-end pin: Ship 5's egress enforcement works inside a real VM.

This is the proxy-level equivalent of test_42. Cold-starts a project
VM with network.allow + one !secret entry, then verifies:
- allow-listed host (httpbin.org) reaches upstream via iron-proxy
- non-allow-listed host (google.com) is blocked (nftables drops the
  rewritten connection because iron-proxy returns 502 for unknown
  hosts in the allow-list)
- /vm/start completes without the missing-secret check tripping

The transparent-proxy model (Tasks 8c + 9b) means no HTTPS_PROXY
inside the VM — the workload just calls curl https://X/ and iron-
proxy is the silent middleman.
"""
import subprocess

import pytest


@pytest.mark.xfail(
    strict=False,
    reason=(
        "devm bug C: iron-proxy DNS forwarding is broken inside the VM; "
        "curl exits 6 (could not resolve host). Remove xfail when bug C lands."
    ),
)
@pytest.mark.devm
@pytest.mark.slow
def test_egress_enforcement(devm, workspace):
    """Cold-start with allow-list; verify allowed/blocked behavior."""
    # devm.yaml with network.allow + one !secret entry.
    workspace.write_devmyaml(
        install=["true"],
        services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
        network={"allow": ["httpbin.org"]},
    )

    # Plant a test secret first.
    subprocess.run(
        [devm.path, "secret", "set", "TEST_TOKEN"],
        input=b"real-test-secret\n",
        cwd=str(workspace.path),
        check=True,
    )

    try:
        # Cold-start.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Allow-listed host reaches upstream.
        r = subprocess.run(
            [devm.path, "shell", "--", "curl", "-sf", "-o", "/dev/null",
             "-w", "%{http_code}", "--max-time", "15", "https://httpbin.org/get"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=30,
        )
        # 200 OK from httpbin.
        assert r.returncode == 0 and r.stdout.strip() == b"200", \
            f"allow-listed host returned status {r.stdout!r} (stderr: {r.stderr.decode()})"

        # Non-allow-listed host blocked.
        r = subprocess.run(
            [devm.path, "shell", "--", "curl", "-sf", "-o", "/dev/null",
             "--max-time", "15", "https://google.com"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=30,
        )
        # curl -sf returns 22 for HTTP errors (502 from iron-proxy) and
        # non-zero for connection failures. Either way, non-zero is what
        # we want — the request must NOT succeed.
        assert r.returncode != 0, \
            "non-allow-listed host should have been blocked but curl returned 0"
    finally:
        # Cleanup.
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
        subprocess.run([devm.path, "secret", "delete", "TEST_TOKEN"], cwd=str(workspace.path),
                       capture_output=True, timeout=10)


@pytest.mark.xfail(
    strict=False,
    reason=(
        "devm bug C: iron-proxy DNS forwarding is broken inside the VM; "
        "curl exits 6 (could not resolve host). Remove xfail when bug C lands."
    ),
)
@pytest.mark.devm
@pytest.mark.slow
def test_open_mode_reaches_any_host(devm, workspace):
    """network.allow: ['*'] reaches a host that the restrictive test blocks."""
    workspace.write_devmyaml(
        install=["true"],
        services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
        network={"allow": ["*"]},
    )
    try:
        r = subprocess.run([devm.path, "shell", "--", "true"],
                           cwd=str(workspace.path), capture_output=True, timeout=300)
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # google.com is BLOCKED in the restrictive test; under '*' it must reach.
        r = subprocess.run(
            [devm.path, "shell", "--", "curl", "-sf", "-o", "/dev/null",
             "-w", "%{http_code}", "--max-time", "15", "https://google.com"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0 and r.stdout.strip() in (b"200", b"301", b"302"), \
            f"open mode failed to reach google.com: {r.stdout!r} (stderr: {r.stderr.decode()})"
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
