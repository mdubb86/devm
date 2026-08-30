"""229: a policy-blocked request inside the guest gets devm's
self-describing reject, not an anonymous 403.

The daemon is the realtime egress authority (iron-proxy grpc transform →
daemon TransformService). What a blocked client sees is devm's contract
with every guest tool and agent:

  HTTP/1.1 403
  X-Devm-Blocked: egress-policy
  Content-Type: application/json
  {"blocked_by":"devm-egress-policy","host":...,"method":...,"url":...,"hint":...}

An allowed host still round-trips 200, proving the grpc transform path
serves normal traffic.
"""
from __future__ import annotations

import json
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_blocked_curl_sees_devm_reject(devm, workspace, sandbox_name):
    from helpers.tart import TartSandbox

    workspace.write_devmyaml(
        network={"allow": ["example.com"]},
        packages=["curl"],
    )
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
    sandbox = TartSandbox(name=sandbox_name)

    # Blocked host over HTTPS: guest trusts the devm CA, so the MITM'd
    # reject arrives as clean HTTP with devm's marker header and body.
    r = sandbox.exec_shell(
        "curl -s -D /tmp/hdrs --max-time 10 -o /tmp/body "
        "-w '%{http_code}' https://example.org/some/path; "
        "echo; cat /tmp/hdrs; echo ---BODY---; cat /tmp/body"
    )
    out = r.stdout
    status, _, rest = out.partition("\n")
    hdrs, _, body = rest.partition("---BODY---")
    assert status.strip() == "403", f"expected devm 403, got {status!r}:\n{out}"
    assert "x-devm-blocked: egress-policy" in hdrs.lower(), (
        f"reject must carry the devm marker header:\n{hdrs}"
    )
    assert "content-type: application/json" in hdrs.lower(), (
        f"reject body must be declared as JSON:\n{hdrs}"
    )
    assert body.strip(), f"empty reject body:\n{out}"
    payload = json.loads(body.strip())
    assert payload["blocked_by"] == "devm-egress-policy"
    assert payload["host"] == "example.org"
    assert payload["method"] == "GET"
    assert "/some/path" in payload["url"]
    assert payload["hint"]

    # Allowed host still round-trips.
    r = sandbox.exec_shell(
        "curl -s -o /dev/null --max-time 10 -w '%{http_code}' https://example.com/"
    )
    assert r.stdout.strip() == "200", (
        f"allowed host must proxy through the grpc transform path; got {r.stdout!r}"
    )
