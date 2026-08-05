"""157: `https://*.test` works from INSIDE the VM — the in-vm-https
feature's end-to-end proof.

Modeled on test_110_direct_cold_start.py (closest analogue: a `direct:
true` service through a cold start) and test_112's host-process `exec:`
pattern (no docker needed for either service here). `netcat-openbsd` and
`openssl` aren't in the base image, so both are declared via `packages:`
(test_112's shape), with `network.allow` for the Debian mirrors the apt
install needs.

Two services on one project:
  - `api` — a plain (non-direct) HTTP service on an `.test` hostname.
    Its backend is a host-process `nc` loop that hands back a fixed
    HTTP/1.1 response on every connection (fresh `nc` per connection via
    a `while true` wrapper — `printf | nc -l` exits after one client, so
    the loop keeps a listener up across repeated curls).
  - `db` — a `direct: true` service, same shape, but replies with a bare
    banner (no HTTP framing) since direct traffic is raw TCP, never
    HTTP-dispatched.

What this pins (design: docs/superpowers/specs/2026-08-05-in-vm-https-design.md):
  - in-guest DNS: `getent hosts <api-hostname>` answers softnet's hairpin
    `192.0.2.2` (dnsmasq forwards `.test` to the gateway; softnet's own
    resolver intercepts it); `getent hosts <db-hostname>` (direct)
    answers `127.0.0.1`.
  - `curl -fsS https://<api-hostname>/` succeeds with NO `-k` — the
    guest already trusts the devm CA, so the guest-origin listener's
    leaf verifies. This is the headline assertion the whole feature
    exists for.
  - `curl -fsS http://<api-hostname>/` also succeeds — scheme parity
    with the Mac, which serves both from the same ProxyServer.
  - cert-identity parity: the issuer of the leaf served in-guest (via
    the daemon's guest-origin listener) equals the issuer of the leaf
    the Mac serves for the *same* hostname (via the browser-facing
    listener) — both draw from the same `*CA` instance, standing in for
    design spec contract test #4.
  - the `direct: true` service is reachable in-guest on its own port via
    raw TCP (loopback, no HTTP framing) — traffic never left the guest.
  - `pgrep -x caddy` exits non-zero — no in-guest Caddy process; the
    guest's HTTP router was deleted, not just unconfigured.
  - an unregistered `.test` hostname still hairpins to the guest-origin
    listener (TLS handshake succeeds — the CA signs a leaf for any SNI)
    but gets a 502 with a "no route configured" body from `Routes.Lookup`
    failing — the unknown-host path, same body the Mac-facing listener
    would give (`write502NoRoute`, internal/serviceapi/proxy.go).

What it doesn't cover (pinned elsewhere per the design doc):
  - The pure `egress.target()` dstIP-branch table — `internal/softnet/egress_test.go`.
  - The guest-origin backend-pin (route-local must not leak a Mac
    localhost backend) — a pure test alongside `guestorigin.go`.
  - The softnet `.test` answer table itself (hairpin/loopback/pass-through
    per name shape, under every policy) — `internal/softnet/dns_test.go`.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers import pool_ip
from helpers.direct import BANNER
from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

API_PORT = 54722
DB_PORT = 54723

API_BODY = "in-vm-https-e2e-ok"
# NOTE: these are literal two-character `\r`/`\n` escapes (doubled
# backslashes), not actual CR/LF bytes — the string below becomes a
# single-line shell command that `printf`'s format-string interprets
# at runtime. Embedding real newlines here would span the ExecStart=
# line the provisioner renders into the service's systemd unit file,
# which systemd rejects ("bad unit file setting").
_API_RESPONSE = (
    "HTTP/1.1 200 OK\\r\\n"
    f"Content-Length: {len(API_BODY)}\\r\\n"
    "Connection: close\\r\\n"
    "\\r\\n"
    f"{API_BODY}"
)


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_in_vm_https(workspace, devm, sandbox_name):
    api_hostname = f"api.{sandbox_name}.e2e.test"
    db_hostname = f"db.{sandbox_name}.e2e.test"
    nope_hostname = f"nope.{sandbox_name}.e2e.test"

    # Fresh `nc` per connection: `printf | nc -l` exits once the client
    # disconnects, so a `while true` wrapper keeps a listener up across
    # the repeated curls below (matches test_110's container-side loop,
    # just as a host process — see test_112's host-process precedent).
    api_script = f"while true; do printf '{_API_RESPONSE}' | nc -l -p {API_PORT}; done"
    db_script = f"while true; do printf '%s' '{BANNER.decode()}' | nc -l -p {DB_PORT}; done"

    workspace.write_devmyaml(
        packages=["netcat-openbsd", "openssl"],
        network={"allow": ["deb.debian.org", "security.debian.org"]},
        services={
            "api": {
                "port": API_PORT,
                "hostname": api_hostname,
                "exec": ["sh", "-c", api_script],
                "restart": "always",
            },
            "db": {
                "port": DB_PORT,
                "hostname": db_hostname,
                "direct": True,
                "exec": ["sh", "-c", db_script],
                "restart": "always",
            },
        },
    )

    shell = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert shell.returncode == 0, (
        f"devm shell cold-start failed:\nstderr={shell.stderr.decode()!r}"
    )

    project_id = workspace.slug
    pool = pool_ip(project_id)

    # ---- Assertion 1: in-guest DNS — non-direct `.test` hairpins to
    # ---- softnet's hairpin address; direct `.test` stays loopback. ----
    getent_api = devm_exec_with_retry(
        devm.path, ["getent", "hosts", api_hostname],
        cwd=str(workspace.path), timeout=30,
    )
    assert getent_api.returncode == 0, (
        f"getent hosts {api_hostname} failed: {getent_api.stderr.decode()!r}"
    )
    api_answer = getent_api.stdout.decode().split()[0] if getent_api.stdout else ""
    assert api_answer == "192.0.2.2", (
        f"getent hosts {api_hostname!r} should answer softnet's hairpin "
        f"192.0.2.2; got {api_answer!r} (full: {getent_api.stdout.decode()!r})"
    )

    getent_db = devm_exec_with_retry(
        devm.path, ["getent", "hosts", db_hostname],
        cwd=str(workspace.path), timeout=30,
    )
    assert getent_db.returncode == 0, (
        f"getent hosts {db_hostname} failed: {getent_db.stderr.decode()!r}"
    )
    db_answer = getent_db.stdout.decode().split()[0] if getent_db.stdout else ""
    assert db_answer == "127.0.0.1", (
        f"getent hosts {db_hostname!r} (direct) should answer 127.0.0.1; "
        f"got {db_answer!r} (full: {getent_db.stdout.decode()!r})"
    )

    # ---- Assertion 2 (headline): curl -fsS https://<api>/ with NO -k —
    # ---- the guest's trust bundle verifies the guest-origin listener's
    # ---- leaf. Retried: the `api` service's systemd unit may not have
    # ---- its `nc` listener bound the instant cold-start finishes. ----
    def _curl_body(scheme: str) -> subprocess.CompletedProcess:
        return devm_exec_with_retry(
            devm.path,
            ["curl", "-fsS", f"{scheme}://{api_hostname}/"],
            cwd=str(workspace.path), timeout=15,
        )

    deadline = time.time() + 30
    https_result = _curl_body("https")
    while https_result.returncode != 0 and time.time() < deadline:
        time.sleep(1)
        https_result = _curl_body("https")
    assert https_result.returncode == 0, (
        f"curl -fsS https://{api_hostname}/ (no -k) failed: "
        f"rc={https_result.returncode} stderr={https_result.stderr.decode()!r}"
    )
    assert https_result.stdout.decode() == API_BODY, (
        f"unexpected body over https: {https_result.stdout.decode()!r}"
    )

    # ---- Assertion 3: scheme parity — http:// also works. ----
    http_result = _curl_body("http")
    assert http_result.returncode == 0, (
        f"curl -fsS http://{api_hostname}/ failed: "
        f"rc={http_result.returncode} stderr={http_result.stderr.decode()!r}"
    )
    assert http_result.stdout.decode() == API_BODY, (
        f"unexpected body over http: {http_result.stdout.decode()!r}"
    )

    # ---- Assertion 4: cert-identity parity — same issuer in-guest
    # ---- (via the guest-origin listener) and on the Mac (via the
    # ---- browser-facing ProxyServer listener) for the same hostname.
    # ---- Both draw from the same *CA instance. ----
    issuer_cmd = (
        "openssl s_client -connect {host}:443 -servername {sni} "
        "</dev/null 2>/dev/null | openssl x509 -noout -issuer"
    )
    guest_issuer = devm_exec_with_retry(
        devm.path,
        ["sh", "-c", issuer_cmd.format(host=api_hostname, sni=api_hostname)],
        cwd=str(workspace.path), timeout=30,
    )
    assert guest_issuer.returncode == 0 and guest_issuer.stdout.strip(), (
        f"in-guest openssl s_client/x509 issuer lookup failed: "
        f"rc={guest_issuer.returncode} stdout={guest_issuer.stdout!r} "
        f"stderr={guest_issuer.stderr.decode()!r}"
    )
    guest_issuer_str = guest_issuer.stdout.decode().strip()

    mac_issuer = subprocess.run(
        ["sh", "-c", issuer_cmd.format(host=pool, sni=api_hostname)],
        capture_output=True, timeout=30,
    )
    assert mac_issuer.returncode == 0 and mac_issuer.stdout.strip(), (
        f"Mac-side openssl s_client/x509 issuer lookup failed: "
        f"rc={mac_issuer.returncode} stdout={mac_issuer.stdout!r} "
        f"stderr={mac_issuer.stderr.decode()!r}"
    )
    mac_issuer_str = mac_issuer.stdout.decode().strip()

    assert guest_issuer_str == mac_issuer_str, (
        f"cert issuer mismatch for {api_hostname!r}: "
        f"guest={guest_issuer_str!r} mac={mac_issuer_str!r}"
    )

    # ---- Assertion 5: the direct service answers on its own port
    # ---- in-guest via raw TCP loopback — no HTTP framing, no proxy
    # ---- hop. Retried for the same readiness reason as assertion 2. ----
    def _db_read() -> subprocess.CompletedProcess:
        return devm_exec_with_retry(
            devm.path,
            ["bash", "-c",
             f"timeout 5 bash -c "
             f"'exec 3<>/dev/tcp/127.0.0.1/{DB_PORT}; "
             f"head -c {len(BANNER)} <&3'"],
            cwd=str(workspace.path), timeout=30,
        )

    deadline = time.time() + 30
    db_result = _db_read()
    while db_result.stdout != BANNER and time.time() < deadline:
        time.sleep(1)
        db_result = _db_read()
    assert db_result.returncode == 0 and db_result.stdout == BANNER, (
        f"in-VM loopback 127.0.0.1:{DB_PORT} did not return the expected "
        f"banner: rc={db_result.returncode} stdout={db_result.stdout!r} "
        f"stderr={db_result.stderr.decode()!r}"
    )

    # ---- Assertion 6: no in-guest Caddy process — the guest's HTTP
    # ---- router was deleted, not left idle. ----
    pgrep_caddy = devm_exec_with_retry(
        devm.path, ["pgrep", "-x", "caddy"],
        cwd=str(workspace.path), timeout=15,
    )
    assert pgrep_caddy.returncode != 0, (
        f"pgrep -x caddy found a process in the guest (rc="
        f"{pgrep_caddy.returncode}): stdout={pgrep_caddy.stdout!r}"
    )

    # ---- Assertion 7: unknown hostname — TLS still terminates (the CA
    # ---- signs a leaf for any SNI) but the daemon's guest-origin
    # ---- listener has no route for it: 502, body mentions "no route".
    # ---- No -f here (it would suppress the body we need to inspect). ----
    nope = devm_exec_with_retry(
        devm.path,
        ["curl", "-s", "-w", "\nHTTP_STATUS:%{http_code}",
         f"https://{nope_hostname}/"],
        cwd=str(workspace.path), timeout=15,
    )
    nope_out = nope.stdout.decode()
    assert "HTTP_STATUS:502" in nope_out, (
        f"unknown hostname {nope_hostname!r} should 502; full output: {nope_out!r}"
    )
    assert "no route" in nope_out, (
        f"502 body for unknown hostname {nope_hostname!r} should mention "
        f"'no route'; full output: {nope_out!r}"
    )
