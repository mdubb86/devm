"""Workspace helper: tempdir + devm.yaml builder/patcher.

A test workspace is a directory containing a freshly-rendered
devm.yaml. The Workspace knows how to write a minimal config and
how to patch named sections without breaking YAML.
"""
from __future__ import annotations
import http.server
import socket
import subprocess
import threading
from pathlib import Path
from typing import Any

import yaml


class Workspace:
    def __init__(self, path: Path, slug: str, vm_name: str, port_offset: int = 51000):
        self.path = Path(path)
        self.slug = slug
        self.vm_name = vm_name
        self.port_offset = port_offset
        self._bare_repo_cache: str | None = None
        self._bare_repo_server: http.server.ThreadingHTTPServer | None = None
        self._bare_repo_thread: threading.Thread | None = None
        self._bare_repo_name: str | None = None

    @property
    def devmyaml_path(self) -> Path:
        return self.path / "devm.yaml"

    def bare_repo_url(self) -> str:
        """Create (once) a local bare git repo for this test and serve it
        via a mac-side python http.server so the guest can clone it
        under mutagen. Returns `http://127.0.0.1:<port>/<repo>.git`.

        Iron-proxy forwards guest → mac's 127.0.0.1:port. Callers must
        include `127.0.0.1` in `network.allow` (the default fixture does).
        """
        if self._bare_repo_cache is not None:
            return self._bare_repo_cache
        work = self.path.parent / f"{self.path.name}-repo-work"
        work.mkdir()
        subprocess.run(["git", "-C", str(work), "init", "-q"], check=True)
        (work / "README.md").write_text("bare\n")
        subprocess.run(["git", "-C", str(work), "add", "."], check=True)
        subprocess.run(
            ["git", "-C", str(work), "-c", "user.email=e2e@e2e", "-c", "user.name=e2e",
             "commit", "-q", "-m", "init"],
            check=True,
        )
        bare = self.path.parent / f"{self.path.name}-repo.git"
        subprocess.run(["git", "clone", "--bare", "-q", str(work), str(bare)], check=True)
        # Enable dumb-http protocol on the bare repo. `git update-server-info`
        # writes info/refs and objects/info/packs so plain HTTP GET can serve
        # everything git needs (no git-http-backend CGI required).
        subprocess.run(["git", "-C", str(bare), "update-server-info"], check=True)

        # Start a mac-side python http.server serving the bare repo's parent
        # dir. Guest reaches it via iron-proxy → mac's 127.0.0.1:port.
        served_root = bare.parent  # so URL is /<bare-name>/HEAD, /<bare-name>/info/refs, ...
        handler = _quiet_http_handler(served_root)
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
        port = server.server_address[1]
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self._bare_repo_server = server
        self._bare_repo_thread = thread
        self._bare_repo_name = bare.name
        self._bare_repo_cache = f"http://127.0.0.1:{port}/{bare.name}"
        return self._bare_repo_cache

    def teardown(self) -> None:
        """Called by the workspace fixture at end. Shuts down the mac-side
        git-serving http.server if one was started."""
        if self._bare_repo_server is not None:
            self._bare_repo_server.shutdown()
            self._bare_repo_server.server_close()
            self._bare_repo_server = None
        if self._bare_repo_thread is not None:
            self._bare_repo_thread.join(timeout=5)
            self._bare_repo_thread = None

    def volume_path(self, name: str | None = None) -> Path:
        """Return the Mac-side volume storage path for a project volume.

        name=None -> primary (the daemon derives the primary volume's
        name from the basename of the Mac cwd, i.e. this workspace dir).
        Hardcoded to the devm-e2e identity's RuntimeDir to match the
        daemon under test (see internal/identity.E2E.RuntimeDir()).
        """
        if name is None:
            name = self.path.name
        return Path.home() / "Library/Application Support/devm-e2e/volumes" / self.vm_name / name

    def write_devmyaml(self, *, no_repo: bool = False, **sections: Any) -> None:
        """Write a fresh devm.yaml. Extra sections (install, services, env,
        network) are merged into the project skeleton. A `repos:` map with
        a single "main" entry is auto-injected (pointing at a hermetic
        local bare repo, which shells out to git) unless the caller opts
        out: pass an explicit `repos={...}` section to use verbatim, pass
        `repo=False` or `no_repo=True` to omit the `repos:` block entirely
        without ever calling `bare_repo_url()`.

        The singular `repo=...` kwarg (the pre-Phase-B shape) is no
        longer accepted -- Config.Repos is a map now, and the daemon's
        KnownFields(true) decode rejects a stray `repo:` key outright.
        Callers must pass `repos={"main": {...}}` (or another id)
        instead."""
        if "repo" in sections and sections["repo"] is not False:
            raise ValueError(
                "write_devmyaml(repo=...) is no longer supported: devm.yaml "
                "now uses a `repos:` map. Pass repos={'main': {...}} instead "
                "(or repo=False / no_repo=True to omit repos entirely)."
            )
        if sections.get("repo") is False:
            no_repo = True
            del sections["repo"]
        if not no_repo and "repos" not in sections:
            sections["repos"] = {
                "main": {
                    "url": self.bare_repo_url(),
                    "secret": "e2e_default",
                    "primary": True,
                },
            }
        cfg: dict[str, Any] = {
            "project": {
                "name": self.vm_name,
            },
        }
        # git is a hard requirement in-guest for mutagen cold-start clone.
        # Base image doesn't ship it yet; declare here so RunOpen apt-installs
        # it before SetupPhase runs. Remove once the base image bakes git in.
        if not no_repo and "packages" not in sections:
            cfg["packages"] = ["git"]
        # bare_repo_url() serves the bare repo over http on mac's 127.0.0.1;
        # iron-proxy forwards guest → mac's loopback. Guest needs 127.0.0.1
        # in its egress allowlist to reach it. Callers passing their own
        # `network` block are responsible for including it.
        if not no_repo and "network" not in sections:
            cfg["network"] = {"allow": ["127.0.0.1"]}
        for k, v in sections.items():
            cfg[k] = v
        self.devmyaml_path.write_text(yaml.safe_dump(cfg, sort_keys=False))

    def patch_devmyaml(self, **sections: Any) -> None:
        """Update named top-level sections in the existing devm.yaml."""
        cfg = yaml.safe_load(self.devmyaml_path.read_text()) or {}
        for k, v in sections.items():
            cfg[k] = v
        self.devmyaml_path.write_text(yaml.safe_dump(cfg, sort_keys=False))

    def proxy_log_path(self) -> Path:
        """Mac-side path to this project's iron-proxy audit log under the
        devm-e2e identity's LogDir (~/Library/Logs/devm-e2e/). Filename is
        `<project-name>-proxy.log` — supervisor.Key{ProjectID, RoleProxy}
        keys the log file on project.name, which is this workspace's
        vm_name (see internal/identity.Config.LogDir,
        internal/supervisor.New's log naming)."""
        return Path.home() / "Library" / "Logs" / "devm-e2e" / f"{self.vm_name}-proxy.log"

    def read_proxy_log(self) -> list[str]:
        """Return the iron-proxy audit log's lines for this project, or
        an empty list if iron-proxy hasn't written one yet."""
        path = self.proxy_log_path()
        if not path.exists():
            return []
        return path.read_text(errors="replace").splitlines(keepends=True)

    def add_systemd_service(self, name: str, exec: list[str], restart: str = "always", **extra) -> None:
        """Add (or replace) a systemd service block under services.<name>.

        Use this from tests that need a "service that stays alive" pattern —
        cleaner than threading the full services dict through write_devmyaml
        on every call.
        """
        import yaml
        cfg = yaml.safe_load(self.devmyaml_path.read_text()) or {}
        services = cfg.setdefault("services", {})
        services[name] = {"exec": exec, "restart": restart, **extra}
        self.devmyaml_path.write_text(yaml.safe_dump(cfg, sort_keys=False))


def _quiet_http_handler(directory: Path):
    """Return a SimpleHTTPRequestHandler subclass rooted at `directory`
    with logging suppressed (test suite noise). Serves static bytes;
    git dumb-http protocol only needs HEAD/GET, which
    SimpleHTTPRequestHandler provides out of the box.
    """
    root = str(directory)

    class Handler(http.server.SimpleHTTPRequestHandler):
        def __init__(self, *args, **kwargs):
            super().__init__(*args, directory=root, **kwargs)

        def log_message(self, format, *args):
            pass  # silence

    return Handler
