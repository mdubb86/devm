"""203: Camoufox recipe end-to-end (packages install + binary fetch +
Firefox CA trust via p11-kit bridge).

Proves recipes/ai/camoufox.md works on a real Tart VM, pinning the
recipe's promises in one go:

  A. Runtime libraries present. `dbus-launch` on PATH; libdbus-glib-1-2
     and libxt6t64 both `ii` (installed). Rules out an apt regression
     that would let camoufox's Firefox fail to launch mid-workload.

  B. Volume mounted at the extraction path. `/home/devm/.cache/camoufox`
     is a virtiofs mount, so the ~1.2 GB binary survives teardown.

  C. Firefox trust bridge in place. The startup step symlinks
     ~/.cache/camoufox/distribution/policies.json → the canonical
     /etc/firefox/policies/policies.json (dropped by devm's install.sh).

  D. Real Firefox trust works. Launch camoufox.sync_api.Camoufox
     headless, navigate to https://example.com/ (iron-proxy MITMs it
     with a devm-CA-signed cert), assert the page title is "Example
     Domain" — NOT a "Warning: Potential Security Risk" cert-error
     interstitial. This is the strongest single assertion; a broken
     p11-kit bridge, wrong package set, missing symlink, or Camoufox
     ignoring policies.json all surface here.

Live-network test: needs github.com/daijro/camoufox (release page),
release-assets.githubusercontent.com (the ~1.2 GB binary), and
example.com (the actual trust probe). All egress goes through
iron-proxy.

LIVE RUN DEFERRED at branch-land time. Run via `just e2e-recipe`.
"""
from __future__ import annotations

import subprocess
import textwrap

import pytest

pytestmark = pytest.mark.recipe


@pytest.mark.timeout(1800)
def test_camoufox_recipe(devm, workspace, sandbox_name):
    workspace.devmyaml_path.write_text(textwrap.dedent(f"""\
        project:
          name: {workspace.vm_name}
        packages:
          - xvfb
          - libdbus-glib-1-2
          - libxt6t64
          - dbus-x11
          - python3-pip
          - python3-venv
          # p11-kit-modules ships in devm >=0.18.1 base images; list it
          # here so the test tolerates a stale base image where the
          # bootstrapped e2e daemon predates the base rebuild.
          - p11-kit-modules
        network:
          allow:
            - github.com/daijro/camoufox/releases/download/*
            - release-assets.githubusercontent.com/github-production-release-asset/834082440/*
            - pypi.org
            - files.pythonhosted.org
            - example.com
        volumes:
          camoufox: /home/devm/.cache/camoufox
        install:
          # Camoufox from PyPI (thin Python shim over the bundled Firefox).
          - python3 -m venv /home/devm/.venvs/camoufox
          - /home/devm/.venvs/camoufox/bin/pip install --quiet camoufox[geoip]
          # First-time binary fetch — rides the OPEN provisioning window
          # so pypi.org / GitHub release hosts don't need to stay on the
          # runtime allowlist. ~1.2 GB download; slow tail of cold-start.
          - /home/devm/.venvs/camoufox/bin/python -m camoufox fetch
        startup:
          - mkdir -p /home/devm/.cache/camoufox/distribution
          - ln -sfn /etc/firefox/policies/policies.json /home/devm/.cache/camoufox/distribution/policies.json
    """))

    # Cold-start. Budget covers apt install + venv + pip camoufox +
    # ~1.2 GB pkgman fetch. The binary download is the long tail.
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=1500,
    )
    assert r.returncode == 0, (
        f"cold-start failed:\n"
        f"stdout:\n{r.stdout.decode(errors='replace')}\n"
        f"stderr:\n{r.stderr.decode(errors='replace')}"
    )

    # A. Runtime libraries. dbus-launch is the smoke-test; the other
    # two are library packages you can't `command -v`, so grep dpkg.
    r = subprocess.run(
        [devm.path, "shell", "--", "bash", "-c",
         "command -v Xvfb dbus-launch && "
         "dpkg -l libdbus-glib-1-2 libxt6t64 | awk '$1==\"ii\"{print $2}' | sort"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"runtime-libs check failed:\n{r.stderr.decode()}"
    installed = r.stdout.decode()
    assert "libdbus-glib-1-2" in installed and "libxt6t64" in installed, (
        f"expected both firefox runtime libs installed; got:\n{installed}"
    )

    # B. Volume mounted at the extraction path.
    r = subprocess.run(
        [devm.path, "shell", "--", "bash", "-c",
         "mount | grep -F ' on /home/devm/.cache/camoufox '"],
        cwd=str(workspace.path), capture_output=True, timeout=15,
    )
    assert r.returncode == 0 and b"virtiofs" in r.stdout, (
        f"expected virtiofs mount at /home/devm/.cache/camoufox; got:\n"
        f"{r.stdout.decode()!r}"
    )

    # C. Trust-bridge symlink resolves to the devm-managed policies.json.
    r = subprocess.run(
        [devm.path, "shell", "--", "readlink",
         "/home/devm/.cache/camoufox/distribution/policies.json"],
        cwd=str(workspace.path), capture_output=True, timeout=15,
    )
    assert r.returncode == 0, f"readlink failed:\n{r.stderr.decode()}"
    assert r.stdout.decode().strip() == "/etc/firefox/policies/policies.json", (
        f"trust-bridge symlink target unexpected: {r.stdout.decode()!r}"
    )

    # D. THE trust assertion. Launch Camoufox headless, load example.com
    # (iron-proxy MITMs it with a devm-CA-signed cert), assert the real
    # page title comes back. If the p11-kit bridge, package set, or
    # symlink is wrong, page.title() either raises (TLS failure) or
    # returns "Warning: Potential Security Risk" (cert error page).
    probe = (
        "from camoufox.sync_api import Camoufox\n"
        "with Camoufox(headless=True) as browser:\n"
        "    page = browser.new_page()\n"
        "    page.goto('https://example.com/', timeout=30_000)\n"
        "    title = page.title()\n"
        "    print('TITLE=' + title)\n"
    )
    r = subprocess.run(
        [devm.path, "shell", "--", "bash", "-c",
         f"/home/devm/.venvs/camoufox/bin/python -c \"{probe}\""],
        cwd=str(workspace.path), capture_output=True, timeout=180,
    )
    combined = r.stdout.decode(errors="replace") + r.stderr.decode(errors="replace")
    assert "TITLE=Example Domain" in combined, (
        f"Camoufox failed to load https://example.com through iron-proxy MITM. "
        f"Expected 'TITLE=Example Domain' in output; got exit={r.returncode}\n"
        f"stdout:\n{r.stdout.decode(errors='replace')}\n"
        f"stderr:\n{r.stderr.decode(errors='replace')}"
    )
