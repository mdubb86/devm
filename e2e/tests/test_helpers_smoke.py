"""Offline unit tests for helpers (no sbx, no devm)."""
import os
import subprocess
import tempfile
from pathlib import Path
from unittest.mock import patch

import yaml

from helpers import registry, sbx
from helpers.devm import Devm, DevmError
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
    ws = Workspace(tmp_path, slug="example", sandbox_name="e2e-example-1234")
    ws.write_devmyaml()
    cfg = yaml.safe_load((tmp_path / "devm.yaml").read_text())
    assert cfg["project"]["id"] == "example"
    assert cfg["project"]["sandbox_name"] == "e2e-example-1234"
    assert cfg["project"]["port_offset"] == 51000


def test_workspace_write_with_services_install(tmp_path):
    ws = Workspace(tmp_path, slug="x", sandbox_name="e2e-x-aaaa")
    ws.write_devmyaml(
        install=["touch /tmp/m"],
        services={"api": {"canonical": 8080}},
    )
    cfg = yaml.safe_load((tmp_path / "devm.yaml").read_text())
    assert cfg["install"] == ["touch /tmp/m"]
    assert cfg["services"]["api"]["canonical"] == 8080


def test_workspace_patch_devmyaml(tmp_path):
    ws = Workspace(tmp_path, slug="x", sandbox_name="e2e-x-aaaa")
    ws.write_devmyaml(install=["touch /tmp/a"])
    ws.patch_devmyaml(install=["touch /tmp/b"])
    cfg = yaml.safe_load((tmp_path / "devm.yaml").read_text())
    assert cfg["install"] == ["touch /tmp/b"]


# --- sbx ---

def test_sbx_sandbox_exists_true():
    fake = subprocess.CompletedProcess(
        args=[], returncode=0,
        stdout=b"SANDBOX  IMAGE  STATUS\nfoo      img    running\n",
        stderr=b"",
    )
    with patch("subprocess.run", return_value=fake):
        assert sbx.sandbox_exists("foo") is True


def test_sbx_sandbox_exists_false():
    fake = subprocess.CompletedProcess(
        args=[], returncode=0,
        stdout=b"SANDBOX  IMAGE  STATUS\nbar      img    stopped\n",
        stderr=b"",
    )
    with patch("subprocess.run", return_value=fake):
        assert sbx.sandbox_exists("foo") is False


def test_sbx_ports_parses_json():
    fake = subprocess.CompletedProcess(
        args=[], returncode=0,
        stdout=b'[{"host_ip":"127.0.0.1","host_port":59080,"sandbox_port":8080,"protocol":"tcp"}]',
        stderr=b"",
    )
    with patch("subprocess.run", return_value=fake):
        ports = sbx.ports("foo")
    assert ports == [{"host_ip": "127.0.0.1", "host_port": 59080, "sandbox_port": 8080, "protocol": "tcp"}]


# --- devm ---

def test_devm_path_uses_env(monkeypatch, tmp_path):
    binary = tmp_path / "devm"
    binary.write_text("")
    monkeypatch.setenv("DEVM_BIN", str(binary))
    assert Devm.from_env().path == str(binary)


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
