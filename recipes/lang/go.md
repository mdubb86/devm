---
name: tool/lang/go
category: lang
display_name: Go
description: Go toolchain (apt) + gopls for LSP. Honors Go defaults — no module/build/bin redirection across the host bind-mount.
keywords: go golang gopls module go-install godev
since: recipes-v1.0.0
---

# Go

Cross-platform Go toolchain via apt, plus gopls for LSP-driven editor and
agent intelligence. No environment overrides for module / build / bin
locations — Go's defaults are honored so the VM doesn't share
linux/arm64 build artifacts with a darwin host.

## devm.yaml additions

```yaml
path:
  - /home/devm/go/bin    # Go's default GOBIN -> on PATH so `go install x && x` works

install:
  - apt-get install -y golang-go
  # gopls — Go language server. Used by IDEs and by Claude Code's gopls-lsp
  # plugin (which runs gopls on the same machine as the agent; if Claude is
  # in the VM, gopls must be too). Installed system-wide so it's on PATH for
  # any user inside the VM and survives shell rotation; it gets
  # reinstalled at every cold-start via this block.
  - GOBIN=/usr/local/bin go install golang.org/x/tools/gopls@latest

network:
  allow:
    - proxy.golang.org      # `go mod download` + `go install` module proxy
    - sum.golang.org        # checksum database verification
    - golang.org            # bare `go install golang.org/x/...` paths
    - github.com            # direct VCS fallback for un-proxied modules
```

## Notes

- **Apt is current.** Ubuntu 26.04's `golang-go` package is Go 1.26. No
  version manager needed for modern Go projects.

- **No env overrides for `GOMODCACHE` / `GOCACHE` / `GOBIN`.** Earlier
  iterations redirected these to `$WORKSPACE/.devm/...` for teardown
  survival, but the workspace bind is shared with the host — putting
  linux/arm64 binaries and build artifacts on a darwin/arm64 Mac filesystem
  is wasteful and conceptually wrong (Mac would never run them). Module
  re-downloads on teardown are cheap; build cache rebuilds from cached
  source are fast. Defaults win.

- **`path:` puts `$HOME/go/bin` on PATH.** This uses the top-level `path:`
  field (prepends absolute dirs to PATH). Go's default `GOBIN` is
  `$HOME/go/bin` (= `/home/devm/go/bin` inside the VM); without this
  entry, ad-hoc `go install x@latest` succeeds but typing `x` fails. With
  the entry, the conventional layout works.

- **Tools you want always-available go in the `install:` block** with
  `GOBIN=/usr/local/bin go install …` — they land on a system PATH dir
  (visible to all users in the VM) and get reinstalled at every
  cold-start (`install:` is the teardown bucket). Pattern used here for
  gopls.

- **Ad-hoc tools land in `$HOME/go/bin`** via Go's default. They don't
  survive `devm teardown` but rebuild fast from cached modules. Acceptable
  trade-off given teardowns are rare.

- **gopls is the only always-on tool worth baking into a generic Go
  recipe.** Other common picks are workflow preferences: install when you
  reach for them, not preemptively.
  - `dlv` (delve) — debugger. Only if you step-debug.
  - `golangci-lint` — meta-linter. Useful for local CI parity; heavy.
  - `air` / `reflex` — hot-reload for HTTP services.
  - `gotestsum` — prettier test output.
  - `goimports`, `staticcheck`, `gofumpt` — redundant with gopls in-editor.
  - `gofmt`, `go vet`, `go test`, `pprof` — already built into `go`.

- **The allowlist hits the module ecosystem, not language tooling.**
  `proxy.golang.org` proxies essentially every public Go module (including
  `golang.org/x/...` for tool installs); `sum.golang.org` is the checksum
  database; `golang.org` covers bare-path tool installs that bypass the
  proxy; `github.com` is direct VCS fallback. Add module-host domains
  (`gitlab.com`, `bitbucket.org`, private registries) only when projects
  need them.

- **Cross-compilation is free for pure-Go projects.** `GOOS=darwin
  go build`, `GOOS=windows go build` etc. work out of the box. CGo
  cross-compile would need a cross-toolchain (musl-cross, mingw-w64); not
  included here.
