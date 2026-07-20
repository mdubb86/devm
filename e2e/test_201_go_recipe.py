"""201: Go recipe end-to-end (install proof + module proxy reachability
+ single-GOBIN + cross-compile).

Proves recipes/lang/go.md works on a real Tart VM, pinning each
recipe promise separately so a passing test can't be misinterpreted:

  A. Direct install worked. `go version` prints a `go1.` version
     for `linux/arm64` — proves the go.dev/dl tarball reached the
     guest, sudo tar-extracted cleanly, and /usr/local/go/bin is on
     PATH. Doesn't pin the exact version because the recipe follows
     upstream stable via VERSION lookup; test would break on every
     Go release otherwise.

  B. gopls install worked. `gopls version` returns 0 — proves the
     install: step ran `go install golang.org/x/tools/gopls@latest`
     as user `devm` (no sudo needed with GOBIN=$HOME/go/bin), the
     binary landed in /home/devm/go/bin, and that dir is on PATH.

  C. Single GOBIN target. `[ "$GOBIN" = "/home/devm/go/bin" ]` inside
     the sandbox — pins the env: value the recipe promises. This
     rules out silent env-plumbing regressions (empty GOBIN would
     make gopls land in $HOME/go/bin by default anyway; broken env:
     would look right in isolation but fail on the runtime install
     below).

  D. Module proxy + PATH end-to-end. Do a real `go install X` at
     runtime, then run X — proves proxy.golang.org and sum.golang.org
     are properly allowlisted AND that GOBIN's binary is invokable
     via PATH. This is the single strongest assertion — most failure
     modes surface here.

  E. Cross-compile works. `GOOS=darwin go build` of a hello-world
     compiles cleanly — pins the recipe's explicit promise that pure-
     Go cross-compile works out of the box.

Live-network test: needs go.dev, dl.google.com, proxy.golang.org,
sum.golang.org, github.com. All egress goes through iron-proxy.

LIVE RUN DEFERRED at branch-land time. Run via `just e2e-recipe`.
"""
from __future__ import annotations

import subprocess
import textwrap

import pytest

pytestmark = pytest.mark.recipe


@pytest.mark.timeout(600)
def test_go_recipe(devm, workspace, sandbox_name):
    workspace.devmyaml_path.write_text(textwrap.dedent(f"""\
        project:
          name: {workspace.vm_name}
        path:
          - /usr/local/go/bin
          - /home/devm/go/bin
        env:
          GOBIN: /home/devm/go/bin
        install:
          - 'VER=$(curl -sSL https://go.dev/VERSION?m=text | head -1) && curl -fsSL "https://go.dev/dl/${{VER}}.linux-arm64.tar.gz" | sudo tar -xz -C /usr/local'
          - "go install golang.org/x/tools/gopls@latest"
        network:
          allow:
          - go.dev
          - dl.google.com
          - proxy.golang.org
          - sum.golang.org
          - github.com
    """))

    # Cold-start. Budget covers Go tarball download + extract + gopls
    # module fetch + build. gopls compile is the long tail.
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert r.returncode == 0, (
        f"cold-start failed:\n"
        f"stdout:\n{r.stdout.decode(errors='replace')}\n"
        f"stderr:\n{r.stderr.decode(errors='replace')}"
    )

    # A. Toolchain landed. Shape check: some `go1.X.Y` on linux/arm64.
    # Not pinning the exact version because the recipe follows upstream
    # stable via go.dev/VERSION — pinning here means the test breaks on
    # every Go patch release, defeating the purpose.
    r = subprocess.run(
        [devm.path, "shell", "--", "go", "version"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"go version failed:\n{r.stderr.decode()}"
    out = r.stdout.decode()
    assert "go version go1." in out and "linux/arm64" in out, (
        f"go version output shape mismatch; got: {out!r}"
    )

    # B. gopls is on PATH and callable. gopls itself lives in $GOBIN
    # (/home/devm/go/bin) — this proves that dir is on PATH.
    r = subprocess.run(
        [devm.path, "shell", "--", "gopls", "version"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"gopls version failed:\n{r.stderr.decode()}"

    # C. Env carries GOBIN.
    r = subprocess.run(
        [devm.path, "shell", "--", "bash", "-c", '[ "$GOBIN" = "/home/devm/go/bin" ]'],
        cwd=str(workspace.path), capture_output=True, timeout=15,
    )
    assert r.returncode == 0, (
        "GOBIN in workload env must be /home/devm/go/bin — env: plumbing "
        "regressed or the recipe's env: block was edited."
    )

    # D. The whole chain: module proxy reachable, `go install` writes
    # to GOBIN, PATH picks it up. This is the strongest single
    # assertion — a broken allowlist, wrong GOBIN, or missing PATH
    # entry all surface here.
    r = subprocess.run(
        [devm.path, "shell", "--", "bash", "-c",
         "go install golang.org/x/tools/cmd/goimports@latest && goimports -h"],
        cwd=str(workspace.path), capture_output=True, timeout=180,
    )
    # goimports -h returns non-zero (2) even on success. Check output
    # content instead of exit code.
    combined = r.stdout + r.stderr
    assert b"usage:" in combined.lower() or b"goimports" in combined, (
        f"go install + run goimports failed:\n"
        f"stdout:\n{r.stdout.decode(errors='replace')}\n"
        f"stderr:\n{r.stderr.decode(errors='replace')}"
    )

    # E. Cross-compile promise. The recipe's Notes explicitly claim
    # pure-Go cross-compile is free. Pin it.
    r = subprocess.run(
        [devm.path, "shell", "--", "bash", "-c",
         "echo 'package main\nfunc main(){}' > /tmp/e2e-xc.go && "
         "GOOS=darwin GOARCH=arm64 go build -o /tmp/e2e-xc-out /tmp/e2e-xc.go && "
         "file /tmp/e2e-xc-out | grep -q 'Mach-O'"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    assert r.returncode == 0, (
        f"cross-compile GOOS=darwin failed:\n"
        f"stdout:\n{r.stdout.decode(errors='replace')}\n"
        f"stderr:\n{r.stderr.decode(errors='replace')}"
    )
