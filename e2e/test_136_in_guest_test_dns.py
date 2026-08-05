"""136: `.test` hostnames resolve to softnet's hairpin from INSIDE the guest.

Regression pin for the buzztrack-observed break: cirruslabs' Debian
ships systemd-resolved enabled, which binds 127.0.0.53:53 and
rewrites /etc/resolv.conf. dnsmasq (which owns `.test` for us)
becomes unreachable, and any in-guest `curl foo.test` / `getent
hosts foo.test` returns NXDOMAIN.

The fix is baked into provision-base.sh: mask systemd-resolved,
write /etc/resolv.conf → 127.0.0.1, and dnsmasq's drop-in has
`no-resolv` + explicit upstream (softnet's gateway, 192.168.127.1) so
it doesn't loop and so `.test` queries actually reach softnet's own
resolver instead of NXDOMAIN-ing locally.

Prior tests exercised `.test` from the Mac side (via macOS's
/etc/resolver/ mechanism, which doesn't touch the guest) — no
in-guest coverage until this test.

The assertion is `getent hosts <name>.test` from inside the guest
returns 192.0.2.2 — softnet's hairpin answer for a non-direct `.test`
name, forwarded to the daemon's guest-origin listeners (see
internal/serviceapi/guestorigin.go). `getent hosts` walks nsswitch
(files → dns) so it exercises the same path a real process would.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_dot_test_resolves_to_loopback_in_guest(devm, workspace, sandbox_name):
    workspace.write_devmyaml(install=["true"])

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Probe: resolve a name we know nothing configures explicitly.
        # If dnsmasq is on the resolution path and its `address=/test/…`
        # drop-in is active, ANY `*.test` name resolves to 127.0.0.1.
        r = subprocess.run(
            [devm.path, "shell", "--", "getent", "hosts", "arbitrary.test"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, (
            f"getent hosts arbitrary.test failed (rc={r.returncode}):\n"
            f"stdout: {r.stdout.decode()!r}\n"
            f"stderr: {r.stderr.decode()!r}\n"
            f"If NXDOMAIN, systemd-resolved is likely still owning :53 "
            f"or /etc/resolv.conf isn't pointing at dnsmasq. Check the "
            f"base-image build applied provision-base.sh's mask step."
        )
        out = r.stdout.decode().strip()
        # getent output: "<ip>\t<canonical>  [<aliases…>]"
        first_field = out.split()[0] if out else ""
        assert first_field == "192.0.2.2", (
            f"getent hosts arbitrary.test resolved to {first_field!r}, "
            f"want 192.0.2.2 (softnet's hairpin). A wrong answer here "
            f"means dnsmasq isn't forwarding `.test` queries to the "
            f"softnet gateway (192.168.127.1) — the systemd-resolved-hijack "
            f"regression this test exists to catch. Full output: {out!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
