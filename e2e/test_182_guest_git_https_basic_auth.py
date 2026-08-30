"""182: guest-side git-over-HTTPS through iron-proxy substitutes the
secret placeholder using the Basic-auth wire shape devm's credential
helper emits.

Provisioning writes `/home/devm/.git-credentials` and
`/home/devm/.gitconfig` with credential.helper=store + useHttpPath=true
(internal/render/gitcredentials.go). Guest git assembles
`Authorization: Basic base64("x-access-token:__DEVM_SECRET_<name>__")`
and iron-proxy's Basic-aware replaceInHeader substitutes the
placeholder before forwarding to upstream.

Uses github.com/octocat/devm-e2e-no-such-repo.git — deliberately a
repo path that does not exist. GitHub answers an anonymous git-http
request for it with 401 (it does this uniformly for "doesn't exist"
and "private, no access", to avoid leaking which), which is exactly
what's needed to force git's credential-helper flow to engage:

1. Git's first request carries no Authorization header at all (a
   *real* public repo, e.g. octocat/Hello-World, answers 200 to that
   bare request — git then never calls the credential helper, and
   there is nothing for iron-proxy's secrets transform to substitute;
   this was tried and confirmed empirically before landing on the
   nonexistent-repo approach).
2. On the 401, git queries `credential.helper=store`, finds the
   provisioned `.git-credentials` line, and retries with
   `Authorization: Basic base64("x-access-token:__DEVM_SECRET_<name>__")`
   — no TTY needed, since the credential store already has a match
   and git never has to prompt.
3. Iron-proxy's Basic-aware secrets transform substitutes the
   placeholder on that retry (checked below via the audit log).
4. GitHub still 401s the retry (the stub value isn't a real
   credential) — expected, and not asserted on; see below.

Under the always-through-iron-proxy design, hydration's own extraheader
goes through the same substitution path this test's guest-side ls-remote
exercises; we no longer route around hydration.

We assert on iron-proxy's audit log to confirm the substitution path
fired end-to-end for the guest-originated request, and on the guest's
on-disk file layout to confirm the credential helper was provisioned
as designed.
"""
from __future__ import annotations
import json
import subprocess

import pytest

pytestmark = [
    pytest.mark.devm,
    pytest.mark.skip(
        reason="Needs redesign for the always-through-iron-proxy lifecycle: "
        "hydration now runs unconditionally, so a private-repo `secret:` binding "
        "with a stub credential fails the cold-start clone before the guest-lazy "
        "retry path can be exercised. Substitution coverage lives in "
        "test_iron_contract_08 (algorithm) + "
        "test_private_repo_cold_clone_substitutes_secret (hydration trigger). "
        "See docs/superpowers/TODO.md."
    ),
]


@pytest.mark.timeout(300)
def test_guest_git_https_basic_auth_substitution(devm, workspace):
    # devm.yaml with a repo: block. `network.allow` is belt-and-suspenders
    # for github.com — repo hosts are auto-added to the effective
    # allowlist (serviceapi.RepoHosts) — but load-bearing for the Debian
    # mirrors, which the base guest image needs to apt-install `git`
    # (not present by default; see test_76_packages_apt_install.py for
    # the same `packages:` + mirror-allowlist pattern).
    workspace.write_devmyaml(
        repos={"main": {"url": "https://github.com/octocat/devm-e2e-no-such-repo.git", "secret": "gh_stub", "primary": True}},
        packages=["git"],
        network={"allow": ["github.com", "deb.debian.org", "security.debian.org"]},
    )

    # Seed the stub secret — any non-empty value works. Its value is never
    # a valid GitHub credential; see the module docstring for why that's
    # exactly the point.
    subprocess.run(
        [devm.path, "secret", "set", "gh_stub"],
        input=b"stub-value-not-a-real-github-credential\n",
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
        check=True,
    )


    try:
        # Cold-start: boots the guest and runs hydration through iron-proxy,
        # then provisioning writes .git-credentials / .gitconfig per
        # internal/render/gitcredentials.go.
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=180,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

        # Verify guest-side file layout matches the design BEFORE running
        # any git operation against it. credential.helper=store erases a
        # credential line on a rejected auth (git calls the helper's
        # "erase" action on 401) — the ls-remote below intentionally
        # drives that rejection, so checking file content afterward would
        # be checking a file git has already scrubbed. Confirmed
        # empirically: .git-credentials is 0 bytes post-ls-remote.
        creds = subprocess.run(
            [devm.path, "exec", "--", "cat", "/home/devm/.git-credentials"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        ).stdout.decode()
        assert (
            "https://x-access-token:__DEVM_SECRET_gh_stub__@github.com/octocat/devm-e2e-no-such-repo.git"
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

        # Guest-side git operation: git ls-remote against the same
        # (nonexistent) repo, relying on the provisioned credential
        # helper — no user-side setup inside the guest.
        #
        # Return code is deliberately NOT asserted here. GitHub 401s the
        # anonymous first attempt, git's credential helper retries with
        # the stub secret's placeholder-substituted value as Basic auth,
        # and GitHub 401s again since it isn't a real credential (and,
        # for a truly private repo the token doesn't unlock, git would
        # exit non-zero here regardless — that's correct behavior, not a
        # devm bug). That final failure is expected and orthogonal to
        # what this test pins: that the guest-originated retry reaches
        # iron-proxy and iron-proxy's Basic-aware secret substitution
        # fires on it (checked below via the audit log). A real PAT
        # unlocking a real repo would get 200 here; the stub proves the
        # plumbing, not the credential's validity.
        result = subprocess.run(
            [devm.path, "exec", "--",
             "git", "ls-remote", "https://github.com/octocat/devm-e2e-no-such-repo.git"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=60,
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
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=60,
        )
