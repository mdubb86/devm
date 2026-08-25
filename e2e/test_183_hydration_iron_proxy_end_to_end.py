"""183: cold-start hydration through iron-proxy — regression pin for the
sewtrue hydration failure.

HydrateRepoVolume (internal/serviceapi/hydrate.go) runs the primary
volume's `git clone` on the HOST, with HTTP(S)_PROXY pointed at the
freshly spawned iron-proxy's listener (internal/serviceapi/vm.go calls
SpawnIronProxy, then loops HydrateRepoVolume immediately after).
SpawnIronProxy's readiness wait only confirms the iron-proxy PROCESS
exists (a `ps`-based DiscoverIronProxies poll) — not that its HTTP(S)
listeners are actually bound and accepting yet. If hydration's `git
clone` dials before the listener is up, git's HTTP backend (libcurl)
fails with the unmistakable signature:

    Failed to connect to 127.0.0.1:<proxy-port>

No existing e2e test exercises this path: test_170..test_181 all use
`file://` bare repos, which hydrate.go's `if ironProxyURL != "" &&
secret != ""` guard skips entirely (a local filesystem clone needs no
HTTP(S)_PROXY or Authorization header). test_182 pre-seeds the primary
volume specifically to SKIP hydration and pin the guest-side
credential-helper path instead — see its module docstring for why
hydration and the guest path can't share one test.

This test builds a real HTTPS git server (dumb protocol, Basic-auth
gated) on localhost, signed by the e2e CA (the same CA the macOS
System Keychain already trusts as "devm-e2e Local CA"), and drives a
real `devm start` cold-start clone through iron-proxy against it:

  - test_hydration_through_iron_proxy_succeeds: correct secret -> a
    clean clone, with the substitution proven both via the mini
    server's received Authorization header and iron-proxy's audit
    log.
  - test_hydration_fails_loud_with_wrong_secret: wrong secret -> the
    mini server's 401 rejects hydration's first request, which
    surfaces in `devm start`'s error as an auth-shaped failure. Note:
    this is NOT a literal "401" string in practice -- HydrateRepoVolume
    passes the secret via `-c http.extraheader=Authorization: ...`,
    which libcurl doesn't treat as "credentials already supplied" for
    its own auth-negotiation machinery. On a 401 challenge git always
    falls through to its credential subsystem (global
    `credential.helper`, then an interactive prompt) before ever
    reporting the raw HTTP status back to the caller; headless (no
    TTY, as under the daemon's LaunchDaemon context) that subsystem
    itself fails first, e.g. "could not read Username for '...':
    Device not configured" or "unable to get password from user".
    Confirmed empirically (see the mini server's dumb-HTTP handshake
    trace) before landing on this assertion shape. CRITICALLY, whatever
    the exact wording, the "Failed to connect to 127.0.0.1" signature
    must NOT appear — that signature means iron-proxy wasn't listening
    yet, a different (and more serious) failure than an auth rejection.
"""
from __future__ import annotations

import base64
import json
import ssl
import subprocess
import threading
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm

_CA_DIR = Path.home() / "Library" / "Application Support" / "devm-e2e" / "ca"
_CA_CERT = _CA_DIR / "root.crt"
_CA_KEY = _CA_DIR / "root.key"

# git's HTTP backend (libcurl) emits this exact shape when it can't
# reach the proxy port at all -- the sewtrue signature. A 401 (or any
# other HTTP-level rejection) never produces this string; only a
# connection-level failure to 127.0.0.1 does.
_SEWTRUE_SIGNATURE = "Failed to connect to 127.0.0.1:"


def _make_bare_repo(base_dir: Path) -> Path:
    """Build a bare repo with one committed file, servable over the git
    dumb-HTTP protocol: loose objects (no `git gc` ever runs) plus
    `update-server-info`'s generated `info/refs`."""
    work = base_dir / "work"
    work.mkdir()
    subprocess.run(["git", "init", "-q", str(work)], check=True)
    (work / "HYDRATED.md").write_text("hydrated-through-iron-proxy\n")
    subprocess.run(["git", "-C", str(work), "add", "."], check=True)
    subprocess.run(
        ["git", "-C", str(work), "-c", "user.email=e2e@e2e", "-c", "user.name=e2e",
         "commit", "-q", "-m", "init"],
        check=True,
    )
    bare = base_dir / "repo.git"
    subprocess.run(["git", "clone", "--bare", "-q", str(work), str(bare)], check=True)
    subprocess.run(["git", "-C", str(bare), "update-server-info"], check=True)
    return bare


def _generate_leaf_cert(out_dir: Path) -> tuple[Path, Path]:
    """Mint a `localhost` leaf cert signed by the e2e CA.

    Iron-proxy MITMs the CONNECT tunnel: it terminates TLS with the
    client (git, here) using a leaf it generates itself for the CONNECT
    target's hostname, signed by its OWN configured CA -- that leg
    doesn't involve this cert at all. This cert is for the OTHER leg:
    iron-proxy, acting as a client itself, dials the real upstream (our
    mini server) and verifies ITS certificate against the Mac's system
    trust store, which already trusts "devm-e2e Local CA" (confirmed
    via `security find-certificate`). Same shape as
    test_iron_contract_05_ca_signs_leaf_certs.py's `_generate_ca`
    helper, but signing a leaf (CA:FALSE, serverAuth EKU) rather than
    another CA.
    """
    leaf_key = out_dir / "leaf.key"
    leaf_csr = out_dir / "leaf.csr"
    leaf_crt = out_dir / "leaf.crt"
    ext_file = out_dir / "leaf.ext"
    serial_file = out_dir / "ca.srl"

    subprocess.run(
        ["openssl", "ecparam", "-genkey", "-name", "prime256v1", "-out", str(leaf_key)],
        check=True, capture_output=True,
    )
    subprocess.run(
        ["openssl", "req", "-new", "-key", str(leaf_key), "-out", str(leaf_csr),
         "-subj", "/CN=localhost"],
        check=True, capture_output=True,
    )
    ext_file.write_text(
        "subjectAltName=DNS:localhost\n"
        "keyUsage=critical,digitalSignature,keyEncipherment\n"
        "extendedKeyUsage=serverAuth\n"
    )
    # -CAserial writes a fresh serial file under out_dir rather than
    # touching the e2e CA's own directory (~/Library/.../ca/), which is
    # 0700 and not this test's to write into.
    subprocess.run(
        ["openssl", "x509", "-req", "-in", str(leaf_csr),
         "-CA", str(_CA_CERT), "-CAkey", str(_CA_KEY),
         "-CAcreateserial", "-CAserial", str(serial_file),
         "-out", str(leaf_crt), "-days", "1", "-extfile", str(ext_file)],
        check=True, capture_output=True,
    )
    return leaf_crt, leaf_key


@dataclass
class _Recorded:
    path: str
    authorization: str | None


class _GitDumbHTTPServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


def _make_handler(repo_dir: Path, expected_token: str, requests: list, lock: threading.Lock):
    """Build a request-handler class closing over this server instance's
    repo dir, expected credential, and shared request log (http.server
    instantiates a fresh handler object per request, so per-server state
    has to live outside `self`)."""

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt, *args):  # noqa: A002 - stdlib signature
            pass  # silence -- keep pytest's captured output readable

        def do_GET(self):
            auth = self.headers.get("Authorization")
            with lock:
                requests.append(_Recorded(path=self.path, authorization=auth))

            if not self._authorized(auth):
                self.send_response(401)
                self.send_header("WWW-Authenticate", 'Basic realm="git"')
                self.send_header("Content-Length", "0")
                self.end_headers()
                return

            rel = self.path.split("?", 1)[0]
            prefix = "/repo.git/"
            fpath = repo_dir / rel[len(prefix):] if rel.startswith(prefix) else None
            if fpath is None or not fpath.is_file():
                body = b"not found"
                self.send_response(404)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                return

            data = fpath.read_bytes()
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def _authorized(self, auth: str | None) -> bool:
            if not auth or not auth.startswith("Basic "):
                return False
            try:
                decoded = base64.b64decode(auth[len("Basic "):]).decode()
            except Exception:
                return False
            return decoded == f"x-access-token:{expected_token}"

    return Handler


@dataclass
class MiniGitServer:
    port: int
    requests: list
    _httpd: ThreadingHTTPServer
    _thread: threading.Thread

    def url(self) -> str:
        return f"https://localhost:{self.port}/repo.git"

    def shutdown(self) -> None:
        self._httpd.shutdown()
        self._httpd.server_close()
        self._thread.join(timeout=5)

    def basic_auth_values(self) -> list[str | None]:
        """Decoded `user:pass` payloads (or None) for every request this
        server received, in receipt order."""
        out: list[str | None] = []
        for r in self.requests:
            decoded = None
            if r.authorization and r.authorization.startswith("Basic "):
                try:
                    decoded = base64.b64decode(r.authorization[len("Basic "):]).decode()
                except Exception:
                    decoded = None
            out.append(decoded)
        return out


@pytest.fixture
def mini_git_server(tmp_path):
    """Factory fixture: `mini_git_server(expected_token)` starts a fresh
    HTTPS git server (dumb protocol, Basic-auth gated, leaf cert signed
    by the e2e CA) on an ephemeral localhost port, and returns a
    `MiniGitServer` handle. Every server a test starts is shut down at
    fixture teardown, guaranteed."""
    if not _CA_CERT.exists() or not _CA_KEY.exists():
        pytest.skip(f"e2e CA not found at {_CA_DIR} -- run `just e2e-bootstrap`")

    servers: list[MiniGitServer] = []
    counter = [0]

    def _start(expected_token: str) -> MiniGitServer:
        idx = counter[0]
        counter[0] += 1
        base = tmp_path / f"gitsrv-{idx}"
        base.mkdir()
        bare = _make_bare_repo(base)
        leaf_crt, leaf_key = _generate_leaf_cert(base)

        requests: list[_Recorded] = []
        lock = threading.Lock()
        handler = _make_handler(bare, expected_token, requests, lock)

        httpd = _GitDumbHTTPServer(("127.0.0.1", 0), handler)
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.load_cert_chain(certfile=str(leaf_crt), keyfile=str(leaf_key))
        httpd.socket = ctx.wrap_socket(httpd.socket, server_side=True)
        port = httpd.server_address[1]

        thread = threading.Thread(target=httpd.serve_forever, daemon=True)
        thread.start()

        srv = MiniGitServer(port=port, requests=requests, _httpd=httpd, _thread=thread)
        servers.append(srv)
        return srv

    try:
        yield _start
    finally:
        for s in servers:
            s.shutdown()


@pytest.mark.timeout(300)
def test_hydration_through_iron_proxy_succeeds(devm, workspace, mini_git_server):
    srv = mini_git_server("the-real-value")

    workspace.write_devmyaml(
        repo={"url": srv.url(), "secret": "hydrate_test"},
        network={"allow": ["localhost"]},
    )
    subprocess.run(
        [devm.path, "secret", "set", "hydrate_test"],
        input=b"the-real-value\n",
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
        check=True,
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        stdout = r.stdout.decode(errors="replace")
        stderr = r.stderr.decode(errors="replace")

        # Regression pin, checked BEFORE the returncode assertion so a
        # failure names the exact invariant broken: iron-proxy's
        # listener must be bound and accepting before HydrateRepoVolume
        # dials it. This is the sewtrue race; a bare returncode == 0
        # check alone wouldn't tell a future debugger which of many
        # possible cold-start failures this is.
        assert _SEWTRUE_SIGNATURE not in (stdout + stderr), (
            "iron-proxy-not-listening-yet regression (sewtrue): hydration's "
            "git clone dialed iron-proxy before its HTTP(S) listener was "
            f"bound. stdout:\n{stdout}\nstderr:\n{stderr}"
        )
        assert r.returncode == 0, f"devm start failed:\nstdout:\n{stdout}\nstderr:\n{stderr}"

        # Mini server actually received the substituted credential --
        # proves iron-proxy's Basic-aware secrets transform fired.
        auth_values = srv.basic_auth_values()
        assert "x-access-token:the-real-value" in auth_values, (
            f"mini git server never saw the substituted secret; "
            f"decoded Authorization payloads: {auth_values!r}"
        )

        # Primary volume's Mac-side storage has the cloned content.
        cloned_file = workspace.volume_path() / "HYDRATED.md"
        assert cloned_file.exists(), (
            f"primary volume storage {workspace.volume_path()} missing "
            f"cloned HYDRATED.md -- hydration did not run (or failed silently)"
        )

        # Iron-proxy's audit log shows both the CONNECT tunnel setup and
        # the GET traffic for localhost.
        proxy_log_lines = workspace.read_proxy_log()
        methods_seen = set()
        for line in proxy_log_lines:
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue
            audit = entry.get("audit") or {}
            host = audit.get("host", "")
            if host.startswith("localhost"):
                methods_seen.add(audit.get("method"))
        assert "CONNECT" in methods_seen, (
            "no CONNECT audit entry for localhost -- iron-proxy never saw "
            "the tunnel setup. Log tail:\n" + "".join(proxy_log_lines[-20:])
        )
        assert "GET" in methods_seen, (
            "no GET audit entry for localhost -- iron-proxy never saw the "
            "clone's HTTP traffic. Log tail:\n" + "".join(proxy_log_lines[-20:])
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )


@pytest.mark.timeout(180)
def test_hydration_fails_loud_with_wrong_secret(devm, workspace, sandbox_name, mini_git_server):
    srv = mini_git_server("the-real-value")

    workspace.write_devmyaml(
        repo={"url": srv.url(), "secret": "hydrate_test_wrong"},
        network={"allow": ["localhost"]},
    )
    subprocess.run(
        [devm.path, "secret", "set", "hydrate_test_wrong"],
        input=b"wrong-value\n",
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
        check=True,
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=120,
        )
        stdout = r.stdout.decode(errors="replace")
        stderr = r.stderr.decode(errors="replace")
        combined = stdout + stderr

        assert r.returncode != 0, (
            f"devm start unexpectedly succeeded with a wrong secret:\n{combined}"
        )
        # A literal "401" rarely survives to this error text -- see the
        # module docstring: git's credential subsystem intercepts the
        # 401 challenge before remote-curl ever reports the raw status,
        # so the visible failure is auth-shaped (a credential-helper or
        # username/password failure) rather than the HTTP code itself.
        # Accept any of the observed shapes.
        auth_failure_markers = (
            "401",
            "Unauthorized",
            "could not read Username",
            "unable to get password",
            "credential",
        )
        assert any(m in combined for m in auth_failure_markers), (
            f"expected an auth-rejection-shaped error in devm start's "
            f"output (one of {auth_failure_markers!r}); got:\n"
            f"stdout:\n{stdout}\nstderr:\n{stderr}"
        )
        # This is the critical discriminator: a wrong secret must fail
        # as an auth rejection, never as the iron-proxy-not-listening-yet
        # race. If this fires, the mini server was never even dialed and
        # the marker matched above would be a false positive from
        # something else.
        assert _SEWTRUE_SIGNATURE not in combined, (
            "wrong-secret failure must read as an auth rejection (401), "
            "not the iron-proxy-not-listening-yet signature -- if this "
            f"fires, the mini server never even got dialed. Output:\n{combined}"
        )

        # Mini server DID get dialed, with the wrong secret substituted
        # in -- proves iron-proxy was up and ran the substitution; this
        # failure is a legitimate auth rejection, not the race.
        auth_values = srv.basic_auth_values()
        assert "x-access-token:wrong-value" in auth_values, (
            f"mini git server never received the wrong-secret request; "
            f"decoded Authorization payloads: {auth_values!r}"
        )

        # A failed cold-start must not leave the sandbox running.
        vm = TartSandbox(name=sandbox_name)
        final_state = vm.wait_state("absent", timeout=30)
        assert final_state == "absent", (
            f"sandbox must not survive a failed hydration; state={final_state!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
