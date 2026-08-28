"""210: cold-start hydration clones IN THE GUEST, through iron-proxy.

The mutagen-volumes model hydrates repos entirely inside the guest:
`CloneRepoInGuest` (internal/serviceapi/mutagen_cold_start.go) runs
`git clone` as the guest's devm user, with HTTP(S)_PROXY pointed at
iron-proxy's tunnel-capable listener.

A plain HTTP (not HTTPS) dumb-protocol git server is enough here: no
CONNECT tunnel or MITM cert plumbing needed, since the guest's
HTTP_PROXY (not HTTPS_PROXY) leg is a bare forward-proxy GET.

Pins:
  - `.git` exists in the guest at `/home/devm/<label>/` (label derives
    from the URL via schema.BareCloneName -- "repo" for this server's
    `.../repo.git` path).
  - The cloned file's content matches what the mini server serves.
  - iron-proxy's audit log shows GET traffic for the server's host --
    proof the clone request actually transited iron-proxy's forward
    proxy, which only a GUEST-side process is wired to use (HTTP_PROXY
    is set inside CloneRepoInGuest's guest script, never on the host
    git invocation this replaces). Iron-proxy's audit log doesn't
    literally carry the guest's DHCP-leased IP as a field (traffic
    arrives via softnet's NAT alias), so this is the closest available
    proxy for "originating from the guest": absence of any GET/CONNECT
    audit entry would mean the request never reached iron-proxy at
    all, which is what a regression to host-side cloning would look
    like structurally on a `file://`-only test but not on an http://
    one like this.
"""
from __future__ import annotations

import json
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


def _make_bare_repo(base_dir: Path) -> Path:
    work = base_dir / "work"
    work.mkdir()
    subprocess.run(["git", "init", "-q", str(work)], check=True)
    (work / "GUESTCLONE.md").write_text("guest-side-clone\n")
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


class _GitDumbHTTPServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


def _make_handler(repo_dir: Path):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt, *args):  # noqa: A002
            pass

        def do_GET(self):
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

    return Handler


@pytest.fixture
def guest_git_server(tmp_path):
    bare = _make_bare_repo(tmp_path)
    handler = _make_handler(bare)
    httpd = _GitDumbHTTPServer(("127.0.0.1", 0), handler)
    port = httpd.server_address[1]
    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://localhost:{port}/repo.git"
    finally:
        httpd.shutdown()
        httpd.server_close()
        thread.join(timeout=5)


@pytest.mark.timeout(300)
def test_cold_start_guest_clone(devm, workspace, guest_git_server):
    workspace.write_devmyaml(
        repos={"main": {"url": guest_git_server, "secret": "e2e_default"}},
        network={"allow": ["localhost"]},
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

        # BareCloneName("http://.../repo.git") -> "repo".
        guest_dir = "/home/devm/repo"

        r = subprocess.run(
            [devm.path, "shell", "--", "test", "-d", f"{guest_dir}/.git"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, (
            f"{guest_dir}/.git missing in guest -- clone did not land at the "
            f"expected guest path:\n{r.stderr.decode()}"
        )

        r = subprocess.run(
            [devm.path, "shell", "--", "cat", f"{guest_dir}/GUESTCLONE.md"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "guest-side-clone"

        proxy_lines = workspace.read_proxy_log()
        saw_get_localhost = False
        for line in proxy_lines:
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue
            audit = entry.get("audit") or {}
            if audit.get("host", "").startswith("localhost") and audit.get("method") == "GET":
                saw_get_localhost = True
                break
        assert saw_get_localhost, (
            "no GET audit entry for localhost in iron-proxy's log -- the guest "
            "clone should have transited iron-proxy's forward proxy. Log tail:\n"
            + "".join(proxy_lines[-20:])
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
