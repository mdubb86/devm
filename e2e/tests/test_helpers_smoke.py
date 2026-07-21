"""Offline unit tests for helpers (no tart, no devm)."""
import json
import os
import subprocess
import tempfile
from pathlib import Path
from unittest.mock import patch

import yaml

from helpers import registry
from helpers.devm import Devm, DevmError
from helpers.pool import pool_ip
from helpers.workspace import Workspace


# --- registry ---

def test_registry_append_and_remove(tmp_path, monkeypatch):
    reg = tmp_path / "reg"
    monkeypatch.setenv("E2E_REGISTRY", str(reg))

    registry.append("sandbox", "e2e-foo-1234")
    registry.append("workspace", "/tmp/devm-e2e-foo-1234")
    registry.append("policy", "x.example.invalid")

    lines = reg.read_text().splitlines()
    assert "sandbox\te2e-foo-1234" in lines
    assert "workspace\t/tmp/devm-e2e-foo-1234" in lines
    assert "policy\tx.example.invalid" in lines

    registry.remove("sandbox", "e2e-foo-1234")
    remaining = reg.read_text().splitlines()
    assert "sandbox\te2e-foo-1234" not in remaining
    assert len(remaining) == 2


def test_registry_noop_when_env_unset(tmp_path, monkeypatch):
    monkeypatch.delenv("E2E_REGISTRY", raising=False)
    # Must not raise even though there's no registry file.
    registry.append("sandbox", "whatever")
    registry.remove("sandbox", "whatever")


# --- workspace ---

def test_workspace_write_minimal_devmyaml(tmp_path):
    ws = Workspace(tmp_path, slug="example", vm_name="e2e-example-1234")
    ws.write_devmyaml()
    cfg = yaml.safe_load((tmp_path / "devm.yaml").read_text())
    assert cfg["project"]["name"] == "e2e-example-1234"


def test_workspace_write_with_services_install(tmp_path):
    ws = Workspace(tmp_path, slug="x", vm_name="e2e-x-aaaa")
    ws.write_devmyaml(
        install=["touch /tmp/m"],
        services={"api": {"port": 8080}},
    )
    cfg = yaml.safe_load((tmp_path / "devm.yaml").read_text())
    assert cfg["install"] == ["touch /tmp/m"]
    assert cfg["services"]["api"]["port"] == 8080


def test_workspace_patch_devmyaml(tmp_path):
    ws = Workspace(tmp_path, slug="x", vm_name="e2e-x-aaaa")
    ws.write_devmyaml(install=["touch /tmp/a"])
    ws.patch_devmyaml(install=["touch /tmp/b"])
    cfg = yaml.safe_load((tmp_path / "devm.yaml").read_text())
    assert cfg["install"] == ["touch /tmp/b"]


# --- devm ---

def test_devm_from_env_returns_bootstrapped_e2e_binary():
    assert Devm.from_env().path == "/usr/local/bin/devm-e2e"


def test_devm_reconcile_invokes_subprocess(monkeypatch, tmp_path):
    binary = tmp_path / "devm"
    binary.write_text("")
    binary.chmod(0o755)
    captured: dict[str, list[str]] = {}
    fake = subprocess.CompletedProcess(args=[], returncode=0, stdout=b"OK", stderr=b"")

    def fake_run(args, **kw):
        captured["args"] = args
        return fake

    monkeypatch.setattr(subprocess, "run", fake_run)
    Devm(str(binary), cwd=str(tmp_path)).reconcile(yes=True)
    assert captured["args"] == [str(binary), "reconcile", "--yes"]


def test_devm_reconcile_raises_on_nonzero(monkeypatch, tmp_path):
    binary = tmp_path / "devm"
    binary.write_text("")
    binary.chmod(0o755)
    fake = subprocess.CompletedProcess(args=[], returncode=2, stdout=b"", stderr=b"boom")
    monkeypatch.setattr(subprocess, "run", lambda *a, **k: fake)
    try:
        Devm(str(binary), cwd=str(tmp_path)).reconcile()
    except DevmError as e:
        assert e.returncode == 2
        assert "boom" in str(e)
    else:
        raise AssertionError("expected DevmError")


def test_add_systemd_service_writes_block(tmp_path):
    from helpers.workspace import Workspace
    ws = Workspace(tmp_path, slug="hsmoke", vm_name="hsmoke-vm")
    ws.write_devmyaml()
    ws.add_systemd_service("greeter", exec=["/usr/bin/echo", "hi"])
    import yaml
    cfg = yaml.safe_load(ws.devmyaml_path.read_text())
    assert cfg["services"]["greeter"]["exec"] == ["/usr/bin/echo", "hi"]
    assert cfg["services"]["greeter"]["restart"] == "always"


def test_add_systemd_service_idempotent_last_wins(tmp_path):
    from helpers.workspace import Workspace
    ws = Workspace(tmp_path, slug="hsmoke", vm_name="hsmoke-vm")
    ws.write_devmyaml()
    ws.add_systemd_service("svc", exec=["/bin/a"])
    ws.add_systemd_service("svc", exec=["/bin/b"], restart="no")
    import yaml
    cfg = yaml.safe_load(ws.devmyaml_path.read_text())
    assert cfg["services"]["svc"]["exec"] == ["/bin/b"]
    assert cfg["services"]["svc"]["restart"] == "no"


# --- pool ---

def test_pool_ip_reads_project_ip_from_state_snapshot(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    state_dir = tmp_path / "Library" / "Application Support" / "devm-e2e" / "state"
    state_dir.mkdir(parents=True)
    (state_dir / "my-project.json").write_text(json.dumps({"project_ip": "127.42.0.7"}))

    assert pool_ip("my-project") == "127.42.0.7"


def test_pool_ip_raises_when_no_snapshot(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    try:
        pool_ip("missing-project")
    except RuntimeError as e:
        assert "missing-project" in str(e)
    else:
        raise AssertionError("expected RuntimeError")
