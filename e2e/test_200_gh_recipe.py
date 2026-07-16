"""200: gh CLI recipe end-to-end (install proof + iron-proxy substitution).

Proves the recipes/tool/gh.md flow works on a real Tart VM, and proves
each link in the substitution chain separately so a passing test can't
be misinterpreted as "some 401 happened, ship it":

  A. Install works. `gh --version` returns 0 with `gh version X.Y.Z`
     — proves cli.github.com was reachable, keyring installed, gh
     binary present.

  B. Workload env holds the OPAQUE PLACEHOLDER, not the real value.
     `[ "$GH_TOKEN" = "__DEVM_SECRET_GH_TOKEN__" ]` inside the sandbox
     — proves env plumbing carried the placeholder from
     `env: GH_TOKEN: !secret GH_TOKEN`, and rules out the "empty env"
     failure mode.

  C. Iron-proxy SUBSTITUTES on the wire. Hit httpbin.org/headers with
     `Authorization: Bearer $GH_TOKEN`. httpbin echoes back the headers
     it received in the JSON body. If the response body contains the
     fake token value string (not the placeholder), iron-proxy MUST
     have swapped in-flight — nothing else in the chain could produce
     the fake value in httpbin's response.

  D. GitHub-side surface works. `gh api /user` reaches api.github.com
     through iron-proxy and returns 401 Bad credentials. Weaker than C
     (a 401 alone doesn't prove substitution — could be substitution
     failed and GitHub rejected the placeholder), but pins that the
     recipe's actual target host round-trips through iron-proxy.

Skipping C would leave a hole: with just A + B + D, a broken
iron-proxy secrets transform still passes (workload has the
placeholder → sends it → GitHub 401s just the same). C is the
belt-and-suspenders proof that substitution actually fires.

Live-network test: needs internet access to cli.github.com (apt repo +
keyring), api.github.com (recipe target), and httpbin.org (substitution
witness). All egress goes through iron-proxy.

LIVE RUN DEFERRED at branch-land time. Run via `just e2e-recipe`
(E2E_ISOLATE=1). Isolated harness auto-heals a stale base image via
`devm _build-base-if-needed` before starting. Uses the real macOS
login keychain for the secret (cleaned up in a finally block, keyed
under the fixture-generated project name so a killed run can be swept
via `security find-generic-password -s devm`).
"""
from __future__ import annotations

import subprocess
import textwrap

import pytest

pytestmark = pytest.mark.recipe


@pytest.mark.timeout(600)
def test_gh_recipe_installs_and_substitutes(devm, workspace, sandbox_name):
    secret_name = "GH_TOKEN"
    fake_token = f"devm-e2e-fake-{sandbox_name}"

    # Plant the fake token in the (real) macOS login keychain, scoped to
    # this project via `<project>/GH_TOKEN`. Cleaned up in the finally.
    proc = subprocess.run(
        [devm.path, "secret", "set", secret_name],
        input=fake_token.encode() + b"\n",
        capture_output=True, timeout=15,
        cwd=str(workspace.path),
    )
    assert proc.returncode == 0, proc.stderr.decode()

    try:
        workspace.devmyaml_path.write_text(textwrap.dedent(f"""\
            project:
              name: {workspace.vm_name}
            packages:
              - wget
            install:
              - "sudo mkdir -p -m 755 /etc/apt/keyrings"
              - "wget -qO- https://cli.github.com/packages/githubcli-archive-keyring.gpg | sudo tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null"
              - "sudo chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg"
              - "echo 'deb [arch=arm64 signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main' | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null"
              - "sudo apt-get update && sudo apt-get install -y gh"
            env:
              GH_TOKEN: !secret {secret_name}
            network:
              allow:
              - cli.github.com
              - host: api.github.com
                secrets:
                - {secret_name}
              - host: httpbin.org
                secrets:
                - {secret_name}
        """))

        # Cold-start. Budget covers apt update + wget keyring + gh install
        # (which pulls ~10MB of deb) with headroom.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=480,
        )
        assert r.returncode == 0, (
            f"cold-start failed:\n"
            f"stdout:\n{r.stdout.decode(errors='replace')}\n"
            f"stderr:\n{r.stderr.decode(errors='replace')}"
        )

        # A. Install worked: gh binary is on PATH and reports its version.
        r = subprocess.run(
            [devm.path, "shell", "--", "gh", "--version"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"gh --version failed:\n{r.stderr.decode()}"
        assert b"gh version" in r.stdout, f"unexpected output: {r.stdout!r}"

        # B. Env plumbing carried the OPAQUE PLACEHOLDER (not the real
        # value). We assert on the exact placeholder string so the test
        # fails loud if either (a) devm ever changes the placeholder
        # format or (b) some future bug plumbs the resolved value into
        # env instead of the placeholder.
        placeholder = f"__DEVM_SECRET_{secret_name}__"
        r = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c",
             f'[ "$GH_TOKEN" = "{placeholder}" ]'],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"GH_TOKEN in workload env must be the placeholder "
            f"{placeholder!r}, not the real value or empty. "
            f"Something in the env plumbing is wrong."
        )

        # C. Iron-proxy actually substitutes on the wire. httpbin.org's
        # /headers endpoint echoes the request headers it received back
        # in the JSON response body. The workload sends
        # `Authorization: Bearer __DEVM_SECRET_GH_TOKEN__` — if
        # iron-proxy substitutes correctly, httpbin sees
        # `Bearer <fake_token>` and echoes that in its response. If
        # substitution is broken, httpbin sees the placeholder and
        # echoes THAT back. The fake_token string in the response body
        # is the smoking-gun proof.
        r = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c",
             'curl -s -H "Authorization: Bearer $GH_TOKEN" https://httpbin.org/headers'],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"httpbin request failed:\n{r.stderr.decode()}"
        body = r.stdout.decode(errors="replace")
        assert fake_token in body, (
            f"httpbin echo did NOT contain the fake token value "
            f"{fake_token!r} — iron-proxy did not substitute on the "
            f"wire. Response body was:\n{body}"
        )
        assert placeholder not in body, (
            f"httpbin echo still contains the placeholder {placeholder!r} — "
            f"iron-proxy left the token unchanged. Response body was:\n{body}"
        )

        # D. Recipe's actual target host round-trips. gh api /user
        # reaches api.github.com through iron-proxy and gets 401 Bad
        # credentials. Weaker than C (see docstring), but pins the
        # recipe's real target.
        r = subprocess.run(
            [devm.path, "shell", "--", "gh", "api", "/user"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode != 0, "gh api /user with fake token must fail"
        combined = r.stdout + r.stderr
        assert b"Bad credentials" in combined or b"401" in combined, (
            f"expected 401/Bad credentials from api.github.com, got:\n"
            f"{combined.decode(errors='replace')}"
        )

    finally:
        # Best-effort keychain cleanup. If this fails (secret already gone,
        # keychain locked, etc.) we don't want to mask the real failure.
        subprocess.run(
            [devm.path, "secret", "delete", secret_name],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
