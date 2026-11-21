"""Pin: cold-start guest git clone with a repo `secret:` binding goes
through iron-proxy under passthrough mode, and the Basic-aware
`Authorization: Basic base64("x-access-token:__DEVM_SECRET_<name>__")`
header gets substituted on the wire (test_iron_contract_08 pins the
transform; this test pins that hydration reaches it).

Uses github.com/octocat/devm-e2e-no-such-repo.git — GitHub returns 401
for any request there, so a substitution annotation in the audit log is
the load-bearing evidence. Return code is not asserted (the stub
secret's value won't unlock the nonexistent repo either).

Bug 1's regression fence.
"""
from __future__ import annotations

import json
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_cold_clone_substitutes_secret(devm, workspace):
    workspace.write_devmyaml(
        repos={
            "main": {
                "url": "https://github.com/octocat/devm-e2e-no-such-repo.git",
                "secret": "gh_stub",
                "primary": True,
            },
        },
        packages=["git"],
        network={"allow": ["github.com", "deb.debian.org", "security.debian.org"]},
    )

    subprocess.run(
        [devm.path, "secret", "set", "gh_stub"],
        input=b"stub-value-not-a-real-github-credential\n",
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
        check=True,
    )

    try:
        # Deliberately NOT pre-seeding the volume — this test exists
        # specifically to prove hydration's own extraheader now reaches
        # iron-proxy's substitution.
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=240,
        )
        # devm start may fail (the nonexistent repo will 401 even with
        # the substituted stub) — but iron-proxy must have SEEN the
        # substitution attempt. Assert on the log, not the exit code.

        proxy_log_lines = workspace.read_proxy_log()
        swapped_gh_stub = False
        for line in proxy_log_lines:
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue
            audit = entry.get("audit") or {}
            if audit.get("host") != "github.com":
                continue
            for tf in entry.get("request_transforms", []):
                if tf.get("name") != "secrets":
                    continue
                for swap in (tf.get("annotations") or {}).get("swapped", []):
                    if swap.get("secret") == "DEVM_SECRET_GH_STUB":
                        swapped_gh_stub = True
                        break

        assert swapped_gh_stub, (
            "hydration clone did NOT route through iron-proxy's Basic-aware "
            "substitution — the always-through invariant is not covering "
            "the cold-clone extraheader path.\n"
            "start rc={}, stderr={}\n"
            "Log tail:\n{}"
        ).format(r.returncode, r.stderr.decode(), "".join(proxy_log_lines[-30:]))
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
