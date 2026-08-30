"""Pin: an `install:` script that hits an external HTTPS host with a
`!secret <name>` env var wired to it sees the substituted value on the
wire. Under v0.21.0 this was structurally impossible in install: (OPEN
egress skipped iron-proxy entirely, so the secrets transform never saw
the request).

Uses a `curl -H "Authorization: Bearer __DEVM_SECRET_gh_stub__"` shape
(placeholder-in-header, not a credential helper) to exercise the same
substitution the guest can drive from install:. The placeholder is
embedded directly in the install: script literal rather than expanded
via `$GH_TOKEN` — the substitution is triggered by iron-proxy seeing
the `__DEVM_SECRET_gh_stub__` token in the header, regardless of how it
got there; `env.GH_TOKEN: !secret gh_stub` still has to be present so
ResolveSecretBindings picks up `gh_stub` as a name to resolve and wires
it into iron-proxy's `secrets` transform for api.github.com (see
internal/serviceapi/resolve_secrets.go).

Similar shape to test_iron_contract_04, but driven from install: in a
real cold-start with devm's iron-proxy config (grpc transform + secrets
transform), not iron-proxy spawned standalone. Asserts on iron-proxy's
audit log's `swapped` annotation — the load-bearing evidence, since a
literal Bearer garbage-token will 401 against GitHub either way (return
code is not asserted, matching test_182 / test_private_repo_cold_clone).

`network.allow` uses the {host, secrets} scoped-allow-entry form
(schema.AllowEntry) so the secret is only ever injectable for
api.github.com.
"""
from __future__ import annotations

import json
import subprocess
import textwrap

import pytest

pytestmark = pytest.mark.devm

SECRET_NAME = "gh_stub"


@pytest.mark.timeout(300)
def test_install_script_env_secret_substitution(devm, workspace):
    subprocess.run(
        [devm.path, "secret", "set", SECRET_NAME],
        input=b"stub-value-not-a-real-github-credential\n",
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
        check=True,
    )

    # yaml.safe_dump can't emit the `!secret` custom tag devm.yaml needs
    # for env: — write the config directly (same pattern as
    # test_101_reconcile_heals_missing_proxy / test_132 / test_74).
    workspace.devmyaml_path.write_text(textwrap.dedent(f"""\
        project:
          name: {workspace.vm_name}
        env:
          GH_TOKEN: !secret {SECRET_NAME}
        install:
          - "curl -fsSL -H 'Authorization: Bearer __DEVM_SECRET_{SECRET_NAME}__' https://api.github.com/rate_limit -o /tmp/rl.out"
        network:
          allow:
          - host: api.github.com
            secrets:
            - {SECRET_NAME}
    """))

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=240,
        )
        # devm start may fail: `curl -f` non-zero-exits on GitHub's 401
        # for the (substituted-but-fake) bearer token, and install:
        # scripts run under `bash -e`, aborting provisioning. That
        # failure is expected and orthogonal to what this test pins —
        # assert on the audit log, not the exit code (same rationale as
        # test_182 / test_private_repo_cold_clone_substitutes_secret).

        proxy_log_lines = workspace.read_proxy_log()
        swapped = []
        for line in proxy_log_lines:
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue
            audit = entry.get("audit") or {}
            if audit.get("host") != "api.github.com":
                continue
            for tf in entry.get("request_transforms", []):
                if tf.get("name") != "secrets":
                    continue
                for swap in (tf.get("annotations") or {}).get("swapped", []):
                    swapped.append(swap)

        match = next(
            (s for s in swapped if s.get("secret") == "DEVM_SECRET_GH_STUB"),
            None,
        )
        assert match is not None, (
            "install: script's Authorization header placeholder was not "
            "substituted by iron-proxy's secrets transform — the "
            "always-through invariant does not cover install:.\n"
            f"start rc={r.returncode}, stderr={r.stderr.decode()!r}\n"
            f"swapped annotations seen: {swapped!r}\n"
            "Log tail:\n" + "".join(proxy_log_lines[-30:])
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
