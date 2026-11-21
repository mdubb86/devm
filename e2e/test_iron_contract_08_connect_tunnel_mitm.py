"""Pin: CONNECT tunnel + MITM proxies HTTPS through the tunnel_listen port.

Iron-proxy exposes THREE listeners, not two:

  - `http_listen`   plain HTTP proxy — serves `GET http://...` requests.
                    Returns 400 for `CONNECT host:port` (not this handler's
                    job — that's the tunnel listener's).
  - `https_listen`  direct TLS-with-SNI arrival. Softnet routes guest
                    traffic here by NATing the target IP to 127.0.0.1 at
                    this port. Pinned by test_iron_contract_05.
  - `tunnel_listen` CONNECT / SOCKS5 tunnel handler with its own accept
                    loop (internal/proxy/tunnel.go). HTTP_PROXY-consuming
                    clients — including `git` with `HTTP_PROXY` set —
                    send `CONNECT host:443` here. Iron-proxy opens the
                    upstream tunnel and MITMs it through the transform
                    pipeline. If this port isn't configured, iron-proxy
                    never starts the tunnel handler at all.

Devm's Mac-side hydration (internal/serviceapi/hydrate.go) sets
HTTP_PROXY / HTTPS_PROXY for `git clone`, so git sends CONNECT to
whatever port those envs name. If devm points HTTP_PROXY at http_listen,
CONNECT returns 400 (see the two negative-control tests below); if it
points at tunnel_listen, CONNECT works and secrets are substituted.

test_iron_contract_04 pins substitution over the plain-HTTP path.
test_iron_contract_05 pins MITM over the https_listen path.
This test pins CONNECT+MITM+substitution over the tunnel_listen path —
the actual code path hydration uses.

Skipped when httpbin.org is not reachable (offline CI).
"""
import subprocess

import pytest

from helpers.iron_proxy import IronProxyConfig, free_ports, spawn


_HTTPBIN_REACHABLE = subprocess.run(
    ["curl", "-o", "/dev/null", "-s", "--max-time", "5", "https://httpbin.org/get"],
    capture_output=True,
).returncode == 0


def _generate_ca(tmp_path):
    ca_cert = tmp_path / "ca.crt"
    ca_key = tmp_path / "ca.key"
    subprocess.run(
        [
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
            "-keyout", str(ca_key), "-out", str(ca_cert),
            "-days", "1", "-subj", "/CN=devm-test-ca",
            "-addext", "basicConstraints=critical,CA:TRUE",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign,digitalSignature",
        ],
        check=True,
        capture_output=True,
    )
    return ca_cert, ca_key


@pytest.mark.contract
@pytest.mark.skipif(not _HTTPBIN_REACHABLE, reason="needs internet to httpbin.org")
def test_connect_via_tunnel_listen_reaches_allowlisted_upstream(tmp_path):
    """CONNECT sent to tunnel_listen → 200 through the MITM pipeline."""
    ca_cert, ca_key = _generate_ca(tmp_path)

    http_port, https_port, tunnel_port = free_ports(3)
    cfg = IronProxyConfig(
        http_listen=f"127.0.0.1:{http_port}",
        https_listen=f"127.0.0.1:{https_port}",
        tunnel_listen=f"127.0.0.1:{tunnel_port}",
        ca_cert_path=str(ca_cert),
        ca_key_path=str(ca_key),
        allow_domains=["httpbin.org"],
    )

    with spawn(cfg):
        result = subprocess.run(
            [
                "curl",
                "--silent",
                "--show-error",
                "--max-time", "15",
                "--proxy", f"http://127.0.0.1:{tunnel_port}",
                "--cacert", str(ca_cert),
                "--write-out", "STATUS=%{http_code}\n",
                "--output", "/dev/null",
                "https://httpbin.org/get",
            ],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0, (
            f"curl through tunnel_listen CONNECT failed (rc={result.returncode}): "
            f"stderr={result.stderr!r}"
        )
        assert "STATUS=200" in result.stdout, (
            f"expected HTTP 200 through tunnel_listen CONNECT, got: "
            f"{result.stdout!r} (stderr={result.stderr!r})"
        )


@pytest.mark.contract
@pytest.mark.skipif(not _HTTPBIN_REACHABLE, reason="needs internet to httpbin.org")
def test_connect_via_tunnel_listen_substitutes_secret_in_header(tmp_path):
    """CONNECT+MITM+secrets end-to-end in the Basic-auth wire shape hydrate
    emits: __DEVM_SECRET_*__ embedded in `Basic base64("x-access-token:...")`
    is auto-decoded by iron-proxy's replaceInHeader, substituted, and
    re-encoded before reaching upstream. Pins the exact contract git
    hydration relies on for private-repo clones.
    """
    import base64 as _b64
    ca_cert, ca_key = _generate_ca(tmp_path)

    http_port, https_port, tunnel_port = free_ports(3)
    cfg = IronProxyConfig(
        http_listen=f"127.0.0.1:{http_port}",
        https_listen=f"127.0.0.1:{https_port}",
        tunnel_listen=f"127.0.0.1:{tunnel_port}",
        ca_cert_path=str(ca_cert),
        ca_key_path=str(ca_key),
        allow_domains=["httpbin.org"],
        secret_tokens={"__DEVM_SECRET_HYDRATE__": "DEVM_SECRET_HYDRATE"},
    )

    # Client-side blob: base64("x-access-token:__DEVM_SECRET_HYDRATE__")
    client_blob = _b64.b64encode(
        b"x-access-token:__DEVM_SECRET_HYDRATE__"
    ).decode("ascii")
    # Expected upstream blob: base64("x-access-token:real-hydrate-secret")
    expected_blob = _b64.b64encode(
        b"x-access-token:real-hydrate-secret"
    ).decode("ascii")

    with spawn(cfg, env={"DEVM_SECRET_HYDRATE": "real-hydrate-secret"}):
        result = subprocess.run(
            [
                "curl",
                "--silent",
                "--show-error",
                "--max-time", "15",
                "--proxy", f"http://127.0.0.1:{tunnel_port}",
                "--cacert", str(ca_cert),
                "-H", f"Authorization: Basic {client_blob}",
                "https://httpbin.org/headers",
            ],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0, (
            f"curl through tunnel_listen CONNECT failed (rc={result.returncode}): "
            f"stderr={result.stderr!r}"
        )
        # httpbin /headers echoes back what it received. The Authorization
        # value must be the re-encoded Basic blob with the substituted
        # secret — proves iron-proxy's Basic-aware decode+substitute+
        # re-encode path fired.
        assert f"Basic {expected_blob}" in result.stdout, (
            "iron-proxy did not produce the expected Basic-auth blob at upstream; "
            f"stdout={result.stdout!r}"
        )
        assert "__DEVM_SECRET_HYDRATE__" not in result.stdout, (
            "raw placeholder leaked to upstream — Basic decode+replace path did not fire; "
            f"stdout={result.stdout!r}"
        )


@pytest.mark.contract
def test_connect_to_http_listen_returns_400_not_supported(tmp_path):
    """Negative control: `CONNECT host:443` sent to http_listen returns 400.
    Documents the failure mode devm's hydration hits when HTTP_PROXY points
    at http_listen instead of tunnel_listen — no code change on either side
    fixes this; the client must reach the tunnel port.

    No upstream network needed — the plain-HTTP handler rejects the method
    before any upstream dial.
    """
    ca_cert, ca_key = _generate_ca(tmp_path)

    http_port, https_port = free_ports(2)
    cfg = IronProxyConfig(
        http_listen=f"127.0.0.1:{http_port}",
        https_listen=f"127.0.0.1:{https_port}",
        # tunnel_listen intentionally omitted — mirrors devm <= v0.16.0.
        ca_cert_path=str(ca_cert),
        ca_key_path=str(ca_key),
        allow_domains=["example.com"],
    )

    with spawn(cfg):
        result = subprocess.run(
            [
                "curl",
                "--silent",
                "--show-error",
                "--max-time", "5",
                "--proxy", f"http://127.0.0.1:{http_port}",
                "--cacert", str(ca_cert),
                "https://example.com/",
            ],
            capture_output=True,
            text=True,
        )
        assert "response 400" in result.stderr, (
            "expected iron-proxy's http_listen to reject CONNECT with 400. "
            f"rc={result.returncode} stderr={result.stderr!r}"
        )
