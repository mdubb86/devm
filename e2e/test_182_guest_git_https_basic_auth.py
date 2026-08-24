"""182: guest-side git-over-HTTPS through iron-proxy substitutes the
secret placeholder using the Basic-auth wire shape devm's credential
helper emits.

Provisioning writes `/home/devm/.git-credentials` and
`/home/devm/.gitconfig` with credential.helper=store + useHttpPath=true
(internal/render/gitcredentials.go). Guest git assembles
`Authorization: Basic base64("x-access-token:__DEVM_SECRET_<name>__")`
and iron-proxy's Basic-aware replaceInHeader substitutes the
placeholder before forwarding to upstream.

Uses github.com/octocat/Hello-World.git — public, no auth required.
The stub secret's value is irrelevant to the upstream response (public
repo returns 200 regardless of what the Authorization header carries);
we assert on iron-proxy's audit log to confirm the substitution path
fired end-to-end, and on the guest's on-disk file layout to confirm
the credential helper was provisioned as designed.
"""
from __future__ import annotations
import json
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_guest_git_https_basic_auth_substitution(devm, workspace):
    # devm.yaml with a repo: block. Public repo, so hydration succeeds
    # regardless of secret value; the substitution path is what we're
    # verifying. `network.allow` is belt-and-suspenders — repo hosts are
    # auto-added to the effective allowlist (serviceapi.RepoHosts), but
    # spelling it out here documents intent.
    workspace.write_devmyaml(
        repo={"url": "https://github.com/octocat/Hello-World.git", "secret": "gh_stub"},
        network={"allow": ["github.com"]},
    )

    # Seed the stub secret — any non-empty value works.
    subprocess.run(
        [devm.path, "secret", "set", "gh_stub"],
        input=b"stub-value-not-used-by-public-repo\n",
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
        check=True,
    )

    try:
        # Cold-start: hydration runs through iron-proxy with the Basic-auth
        # extraheader. Success proves both the tunnel path and the Basic
        # substitution path work end-to-end.
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=180,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

        # Guest-side git operation: git ls-remote against the same host,
        # relying on the provisioned credential helper — no user-side setup
        # inside the guest.
        result = subprocess.run(
            [devm.path, "exec", "--",
             "git", "ls-remote", "https://github.com/octocat/Hello-World.git"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=60,
        )
        assert result.returncode == 0, (
            f"guest-side git ls-remote failed (rc={result.returncode}): "
            f"stderr={result.stderr.decode(errors='replace')!r}"
        )

        # Iron-proxy audit log records every request through it. Find the
        # entry for the git ls-remote hit and assert the Basic-aware
        # substitution ran on the Authorization header.
        proxy_log_lines = workspace.read_proxy_log()
        substitution_annotations = []
        for line in proxy_log_lines:
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue
            audit = entry.get("audit") or {}
            if audit.get("host") != "github.com":
                continue
            for tf in entry.get("request_transforms", []):
                if tf.get("name") == "secrets":
                    annotations = tf.get("annotations") or {}
                    for swap in annotations.get("swapped", []):
                        substitution_annotations.append(swap)
        assert substitution_annotations, (
            "iron-proxy audit log has no swapped-secret annotation for a "
            "github.com request — guest git did NOT route through iron-proxy's "
            "Basic-aware substitution path. Log tail:\n"
            + "".join(proxy_log_lines[-20:])
        )
        # At least one annotation must name our stub secret and locate it
        # in the Authorization header.
        match = next(
            (s for s in substitution_annotations
             if s.get("secret") == "DEVM_SECRET_GH_STUB"
             and "header:Authorization" in s.get("locations", [])),
            None,
        )
        assert match is not None, (
            f"no swapped annotation for DEVM_SECRET_GH_STUB in header:Authorization; "
            f"got: {substitution_annotations!r}"
        )

        # Verify guest-side file layout matches the design.
        creds = subprocess.run(
            [devm.path, "exec", "--", "cat", "/home/devm/.git-credentials"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        ).stdout.decode()
        assert (
            "https://x-access-token:__DEVM_SECRET_gh_stub__@github.com/octocat/Hello-World.git"
            in creds
        ), f"guest .git-credentials missing the expected line; got:\n{creds}"

        gitcfg = subprocess.run(
            [devm.path, "exec", "--", "cat", "/home/devm/.gitconfig"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        ).stdout.decode()
        assert "helper = store" in gitcfg
        assert "useHttpPath = true" in gitcfg

        # Mode + owner assertions on the two files — atomic install(1) is a
        # design invariant.
        stat_creds = subprocess.run(
            [devm.path, "exec", "--", "stat", "-c", "%a %U %G", "/home/devm/.git-credentials"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        ).stdout.decode().strip()
        assert stat_creds == "600 devm devm", (
            f".git-credentials mode/owner wrong: got {stat_creds!r}, want '600 devm devm'"
        )

        stat_cfg = subprocess.run(
            [devm.path, "exec", "--", "stat", "-c", "%a %U %G", "/home/devm/.gitconfig"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        ).stdout.decode().strip()
        assert stat_cfg == "644 devm devm", (
            f".gitconfig mode/owner wrong: got {stat_cfg!r}, want '644 devm devm'"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=60,
        )
