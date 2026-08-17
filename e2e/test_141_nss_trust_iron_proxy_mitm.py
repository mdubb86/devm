"""141: system NSS trust is seeded with the devm CA.

Browsers (Chromium, Firefox) validate TLS against NSS, not the OpenSSL
bundle or any of devm's ~12 env-var CA hooks. devm seeds
/etc/pki/nssdb with the devm CA at guest install-time (see
internal/scripts/install.sh), so headless Chromium can hit any host
iron-proxy MITMs — same as curl does via the OpenSSL bundle.

Without this seed, every `.test` hostname and every allow-listed
iron-proxy-MITM'd HTTPS site in a browser would fail with
ERR_CERT_AUTHORITY_INVALID, and no env var can fix it — NSS refuses
to consume file paths, only its own sqlite trust store. This test
pins the structural gap has been closed.

Assertions:
  1. certutil -L confirms the devm cert is present in /etc/pki/nssdb
  2. Headless Chromium GETs an allow-listed HTTPS URL (through iron-
     proxy's MITM) with exit 0 and no cert-error signature in stderr.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_nss_trust_iron_proxy_mitm(devm, workspace, sandbox_name):
    # chromium package pulls the browser + its runtime libs; no
    # Playwright, no CDN download — the assertion is about devm's
    # NSS seeding, not about any specific browser distribution.
    workspace.write_devmyaml(
        install=["true"],
        packages=["chromium"],
        network={"allow": ["api.github.com"]},
    )
    try:
        # Cold-start.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # 1. Verify the devm cert is present in the system NSS db.
        r = subprocess.run(
            [devm.path, "shell", "--",
             "certutil", "-L", "-d", "sql:/etc/pki/nssdb"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=15,
        )
        assert r.returncode == 0, (
            f"certutil -L failed (libnss3-tools may not be installed in the "
            f"base image, or /etc/pki/nssdb may not exist):\n{r.stderr.decode()}"
        )
        assert b"devm" in r.stdout, (
            f"expected 'devm' nickname in NSS db listing — "
            f"install.sh did not seed it. certutil -L output:\n{r.stdout.decode()}"
        )

        # 2. Headless Chromium reaches an allow-listed HTTPS URL through
        # iron-proxy's MITM without cert errors. Iron-proxy re-signs the
        # upstream cert with the devm CA; Chromium validates that chain
        # against NSS. If NSS trust isn't seeded, Chromium exits with
        # ERR_CERT_AUTHORITY_INVALID.
        #
        # --no-sandbox: chromium's sandbox needs setuid-root helpers we
        # don't ship; the point of this test is TLS chain, not sandbox.
        r = subprocess.run(
            [devm.path, "shell", "--",
             "chromium",
             "--headless=new",
             "--disable-gpu",
             "--no-sandbox",
             "--dump-dom",
             "https://api.github.com/octocat"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=60,
        )
        stderr = r.stderr.decode()
        assert "ERR_CERT_AUTHORITY_INVALID" not in stderr, (
            f"chromium rejected iron-proxy's re-signed cert — NSS trust not "
            f"seeded (or the CA in /etc/pki/nssdb doesn't match "
            f"/opt/devm/ca/devm.crt):\n{stderr}"
        )
        assert "ERR_CERT" not in stderr, (
            f"chromium hit a cert error other than AUTHORITY_INVALID:\n{stderr}"
        )
        assert r.returncode == 0, (
            f"headless chromium failed with exit {r.returncode} against an "
            f"allow-listed HTTPS URL. stderr:\n{stderr}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=60,
        )
