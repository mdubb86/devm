"""37: devm route local registers routes with the devm daemon.

Routes now flow through the daemon's httputil.ReverseProxy, NOT Caddy's
admin API. This test verifies route registration and removal via the
daemon's /routes HTTP API over its Unix domain socket.

devm route local is used here (host canonical ports, no VM required)
because the route registration path through the daemon is identical for
both local and vm modes — the only difference is BackendHost. The
daemon's /routes endpoint is the authoritative source of truth for all
routes regardless of mode.

What this pins:
  - `devm route local` exits 0.
  - The daemon's GET /routes shows the project's hostname after the
    command runs.
  - POST /routes/remove removes the project's routes; GET /routes no
    longer returns an entry for the project.

What it doesn't cover (tested elsewhere):
  - The actual HTTP proxy round-trip (would require a running sandbox to
    back the dial port).
  - devm route vm specifically (same daemon path; differs only in
    BackendHost resolution via tart, which requires a running VM).
"""
from __future__ import annotations

import http.client
import json
import os
import socket as _socket_module
import subprocess

import pytest

pytestmark = pytest.mark.devm

# The bootstrapped devm-e2e daemon's socket — distinct from any real
# prod devm install's, so this test only ever sees this suite's routes.
_SOCKET_PATH = os.path.join(
    os.path.expanduser("~/Library/Application Support/devm-e2e"),
    "devm.sock",
)


class _UnixSocketHTTP(http.client.HTTPConnection):
    """HTTPConnection over a Unix domain socket."""

    def __init__(self, socket_path: str):
        super().__init__("localhost")
        self._socket_path = socket_path

    def connect(self) -> None:
        self.sock = _socket_module.socket(
            _socket_module.AF_UNIX, _socket_module.SOCK_STREAM
        )
        self.sock.connect(self._socket_path)


def _get_routes() -> dict[str, list]:
    """GET /routes from the daemon; returns project_id → [Route, ...] map."""
    conn = _UnixSocketHTTP(_SOCKET_PATH)
    conn.request("GET", "/routes")
    resp = conn.getresponse()
    assert resp.status == 200, f"GET /routes returned {resp.status}"
    return json.loads(resp.read())


def _remove_routes(project_id: str) -> None:
    """POST /routes/remove for the given project."""
    body = json.dumps({"name": project_id}).encode()
    conn = _UnixSocketHTTP(_SOCKET_PATH)
    conn.request(
        "POST", "/routes/remove", body=body,
        headers={"Content-Type": "application/json"},
    )
    resp = conn.getresponse()
    assert resp.status == 204, f"POST /routes/remove returned {resp.status}"


@pytest.mark.timeout(60)
def test_route_registers_and_removes(workspace, devm, sandbox_name):
    hostname = f"{sandbox_name}-route.e2e.test"
    workspace.write_devmyaml(
        services={
            "web": {"port": 8080, "hostname": hostname},
        },
    )

    project_id = workspace.slug

    # Defensive cleanup: clear any leftover routes from a prior aborted run.
    _remove_routes(project_id)

    try:
        r = subprocess.run(
            [devm.path, "route", "local"],
            cwd=str(workspace.path),
            capture_output=True, timeout=30, check=False,
        )
        assert r.returncode == 0, (
            f"`devm route local` exit {r.returncode}\n"
            f"stdout: {r.stdout.decode()!r}\nstderr: {r.stderr.decode()!r}"
        )

        # The daemon must show the route for this project.
        routes = _get_routes()
        assert project_id in routes, (
            f"daemon /routes has no entry for project {project_id!r}; got: {routes}"
        )
        found = any(entry["hostname"] == hostname for entry in routes[project_id])
        assert found, (
            f"route for hostname {hostname!r} not in daemon routes for "
            f"{project_id!r}; got: {routes[project_id]}"
        )

        # Remove via the daemon API directly.
        _remove_routes(project_id)

        # Routes should be gone.
        routes = _get_routes()
        still_present = routes.get(project_id, [])
        assert not still_present, (
            f"routes still present for {project_id!r} after removal: {still_present}"
        )
    finally:
        # Always clean up.
        _remove_routes(project_id)
