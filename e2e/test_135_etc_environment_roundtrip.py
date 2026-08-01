"""135: /etc/environment round-trip through the real devm guest.

Successor to `TestEncoder_RoundTripThroughPamAndShell` (Go, ran a
Debian container via docker on the host — inconsistent with the rest
of the suite and broke locally whenever OrbStack wasn't running).

Contract pinned: values set under `env:` in devm.yaml, rendered by
production's encoder into /etc/environment in the guest, round-trip
byte-for-byte through BOTH delivery paths:

  1. pam_env  — sshd's session stack loads /etc/environment via
     pam_env, so anything `devm shell` execs inherits it.
  2. shell    — the `with-devm-env` wrapper / /etc/profile.d/devm.sh
     do `set -a; . /etc/environment` at exec time, which must decode
     to the same bytes.

The probe encodes each captured value as base64 guest-side and
decodes host-side, so binary-safe values (spaces, quotes, backticks,
unicode) survive the shell/ssh transport without escaping games.

`env -i` isolates the shell-source path from the pam_env-inherited
env — otherwise the sourced file would agree with itself by
inheritance even if the parser disagreed.

If this fails, /etc/environment consolidation is broken: one of the
two delivery paths disagrees with the input, and users will see
stale/corrupted env in either SSH sessions or the with-devm-env
wrapper.
"""
from __future__ import annotations

import base64
import subprocess

import pytest

pytestmark = pytest.mark.devm


# Cases as (name, yaml_input, expected_shell_value). Most shapes are
# identity round-trips, so yaml_input == expected_shell_value; the
# dollar cases differ because devm.yaml's env resolver treats `$` as
# a variable-reference sigil and requires `$$` for a literal.
#
# These are a SUPERSET of the Go unit test's acceptCases: this test
# exercises devm.yaml → resolve → encode → deliver, whereas the Go
# encoder test in internal/render/ covers encoder shape alone. Keep
# the identity cases in sync with that file; drift is caught by
# review.
ACCEPT_CASES = [
    ("bare_alnum", "hello_world", "hello_world"),
    ("bare_path", "/opt/devm/bin", "/opt/devm/bin"),
    ("bare_url_hostport", "user@host:8080/path.v2-alpha_x", "user@host:8080/path.v2-alpha_x"),
    ("bare_empty", "", ""),
    ("space", "hello world", "hello world"),
    ("leading_space", " leading", " leading"),
    ("trailing_space", "trailing ", "trailing "),
    ("tab", "col1\tcol2", "col1\tcol2"),
    ("apostrophe_only", "don't stop", "don't stop"),
    ("apostrophes_multi", "it's what's happening", "it's what's happening"),
    # devm.yaml `$` sigil: `$$` → literal `$`. Proves both the escape
    # AND that the resolved literal survives /etc/environment.
    ("dollar_escaped", "cost $$50 or $${VAR}", "cost $50 or ${VAR}"),
    ("backslash_only", "a\\b\\c", "a\\b\\c"),
    ("dquote_only", 'he said "hi"', 'he said "hi"'),
    ("backtick_only", "output `cmd` here", "output `cmd` here"),
    ("star_bang_semi", "has * ! ; chars", "has * ! ; chars"),
    ("json_like", '{"key":"value","n":1}', '{"key":"value","n":1}'),
    ("url_with_query", "https://example.com/a?b=1&c=2", "https://example.com/a?b=1&c=2"),
    ("unicode", "héllo wörld", "héllo wörld"),
    ("long_value", "x" * 500, "x" * 500),
]


# Guest-side probe. For each K_* key present in /etc/environment,
# emit a single line: `<key> <pam_b64> <shell_b64>`.
#
# pam path:   $KEY is already in env (pam_env loaded /etc/environment
#             during the ssh session's PAM stack), so `printenv $key`
#             reads the pam-delivered value.
# shell path: `env -i bash -c '. /etc/environment; printenv $key'`
#             strips the inherited env first, forcing the sourced
#             file to be the ONLY source of the value.
#
# base64 with -w0 keeps each captured value on one line even when it
# contains newlines or unicode. Result lines start with `K_` — anything
# else (sshd banners, motd, etc.) is filtered host-side.
PROBE_SCRIPT = r"""
set -u
for key in $(grep -oE '^K_[A-Za-z0-9_]+' /etc/environment); do
    pam=$(printenv "$key" 2>/dev/null || printf '')
    shell=$(env -i bash -c "set -a; . /etc/environment; set +a; printenv $key" 2>/dev/null || printf '')
    pb64=$(printf '%s' "$pam" | base64 -w0)
    sb64=$(printf '%s' "$shell" | base64 -w0)
    printf 'K_RESULT %s %s %s\n' "$key" "$pb64" "$sb64"
done
"""


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_etc_environment_roundtrip(devm, workspace, sandbox_name):
    # Build env: {K_<name>: <yaml_input>} for every accept case. YAML
    # serialization handles quoting for us; PyYAML round-trips these
    # shapes correctly (verified by write_devmyaml + reconcile).
    env_map = {f"K_{name}": yaml_input for name, yaml_input, _ in ACCEPT_CASES}
    workspace.write_devmyaml(
        install=["true"],
        env=env_map,
    )

    try:
        # Cold-start via `devm shell -- true`. Same pattern as test_133.
        # Timeout large because first-time base-image pull can be slow.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Run the probe in one ssh session. Non-login non-interactive
        # bash, so /etc/profile isn't auto-sourced — the pam path
        # depends solely on pam_env's env delivery, not on bash re-
        # sourcing /etc/environment behind our back.
        r = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c", PROBE_SCRIPT],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
        assert r.returncode == 0, (
            f"probe failed (rc={r.returncode}):\n"
            f"stdout: {r.stdout.decode()!r}\n"
            f"stderr: {r.stderr.decode()!r}"
        )

        pam_vals: dict[str, str] = {}
        shell_vals: dict[str, str] = {}
        for line in r.stdout.decode().splitlines():
            if not line.startswith("K_RESULT "):
                continue
            # split(" ") preserves empty base64 fields (bare_empty case);
            # default str.split() would collapse them and drop the line.
            fields = line.split(" ")
            if len(fields) != 4:
                continue
            _, key, pb64, sb64 = fields
            pam_vals[key] = base64.b64decode(pb64).decode()
            shell_vals[key] = base64.b64decode(sb64).decode()

        missing = [
            f"K_{name}" for name, _, _ in ACCEPT_CASES
            if f"K_{name}" not in pam_vals
        ]
        assert not missing, (
            f"probe emitted no result for: {missing}. Full probe stdout:\n"
            f"{r.stdout.decode()}"
        )

        mismatches: list[str] = []
        for name, _, expected in ACCEPT_CASES:
            key = f"K_{name}"
            if pam_vals[key] != expected:
                mismatches.append(
                    f"{key} pam: got {pam_vals[key]!r}, want {expected!r}"
                )
            if shell_vals[key] != expected:
                mismatches.append(
                    f"{key} shell: got {shell_vals[key]!r}, want {expected!r}"
                )
        assert not mismatches, "\n".join(mismatches)
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
