---
name: tool/lang/go
category: lang
display_name: Go
description: Current stable Go toolchain via direct download + gopls for LSP. Single GOBIN target so install-time and runtime `go install` share one directory. Honors Go defaults — no module/build cache redirection across the host bind-mount.
keywords: go golang gopls module go-install godev cross-compile
since: recipes-v1.0.0
---

# Go

Current stable Go toolchain fetched directly from go.dev (not apt —
Debian's package tends to lag by a release or two). The install line
resolves `go.dev/VERSION` at cold-start time, so recipes never go
stale; if you want to pin, swap in an explicit `goX.Y.Z` string. Plus
gopls for LSP-driven editor and agent intelligence. `GOBIN` is set
once in `env:` so both the install-time gopls install AND any ad-hoc
`go install X` at runtime land in the same directory. No overrides
for `GOMODCACHE`/`GOCACHE` — Go's defaults are honored so the VM
doesn't share linux/arm64 build artifacts with a darwin host.

## devm.yaml additions

```yaml
path:
  - /usr/local/go/bin       # Go toolchain (from the tarball extract)
  - /home/devm/go/bin       # $GOBIN — where `go install X` drops binaries

env:
  GOBIN: /home/devm/go/bin

scripts:
  # go.dev only publishes version-embedded tarball names — no `latest`
  # alias — so resolve the current stable release from `/VERSION?m=text`
  # first, then build the download URL from it. Broken into steps here
  # because the version has to survive between commands — `scripts:`
  # runs them under one shell so `$VER` stays live.
  install-go-toolchain:
    - VER=$(curl -sSL https://go.dev/VERSION?m=text | head -1)
    - curl -fsSL "https://go.dev/dl/${VER}.linux-arm64.tar.gz" | sudo tar -xz -C /usr/local

install:
  - ">install-go-toolchain"
  - "go install golang.org/x/tools/gopls@latest"

network:
  allow:
    - go.dev                # /VERSION lookup + release-notes
    - dl.google.com         # go.dev/dl/ redirects here for the tarball
    - proxy.golang.org      # `go mod download` + `go install` module proxy
    - sum.golang.org        # checksum database verification
    - github.com            # direct VCS fallback for un-proxied modules
```

## Notes

- **Version tracks upstream stable.** The `VER=$(curl … VERSION)` step
  fetches whatever go.dev currently advertises (e.g. `go1.26.5`) and
  builds the tarball URL from it. Recipe stays evergreen. If you need
  a specific release for reproducibility, hardcode
  `VER=go1.26.5` (or whatever version) inline instead. Tart is Apple
  Silicon only, so `linux-arm64` is the only variant needed.

- **Single `GOBIN` target.** Both the install: step (`go install
  gopls`) and any ad-hoc runtime `go install X@latest` land in
  `/home/devm/go/bin` — that's `env: GOBIN: ...` in action. Earlier
  iterations split system-wide vs user via `GOBIN=/usr/local/bin go
  install ...` in the install: line, but devm has one user (`devm`),
  so there's no multi-user story to solve.

- **No env overrides for `GOMODCACHE` / `GOCACHE`.** Earlier iterations
  redirected these to `$WORKSPACE/.devm/...` for teardown survival,
  but the workspace bind is shared with the host — putting
  linux/arm64 binaries and build artifacts on a darwin/arm64 Mac
  filesystem is wasteful and conceptually wrong (Mac would never run
  them). Module re-downloads on teardown are cheap; build cache
  rebuilds from cached source are fast.

- **Tools you want always-available go in the `install:` block** so
  they get reinstalled at every cold-start (`install:` is the
  teardown bucket). Pattern used here for gopls.

- **Ad-hoc tools land in `$HOME/go/bin`** via `GOBIN`. They don't
  survive `devm teardown` but rebuild fast from cached modules.
  Acceptable trade-off given teardowns are rare.

- **gopls is the only always-on tool worth baking into a generic Go
  recipe.** Other common picks are workflow preferences: install when
  you reach for them, not preemptively.
  - `dlv` (delve) — debugger. Only if you step-debug.
  - `golangci-lint` — meta-linter. Useful for local CI parity; heavy.
  - `air` / `reflex` — hot-reload for HTTP services.
  - `gotestsum` — prettier test output.
  - `goimports`, `staticcheck`, `gofumpt` — redundant with gopls in-editor.

- **The allowlist hits the module ecosystem, not language tooling.**
  `proxy.golang.org` proxies essentially every public Go module
  (including `golang.org/x/...` for tool installs); `sum.golang.org`
  is the checksum database; `github.com` is direct VCS fallback. Add
  module-host domains (`gitlab.com`, `bitbucket.org`, private
  registries) only when projects need them.

- **Cross-compilation is free for pure-Go projects.** `GOOS=darwin
  go build`, `GOOS=windows go build` etc. work out of the box. CGo
  cross-compile would need a cross-toolchain (musl-cross, mingw-w64);
  not included here.

## Verifying

```
devm shell
$ go version                             # go version go1.26.5 linux/arm64
$ gopls version                          # golang.org/x/tools/gopls vX.Y.Z
$ echo $GOBIN                            # /home/devm/go/bin
$ go install golang.org/x/tools/cmd/goimports@latest && goimports -h  # module proxy + PATH + GOBIN all work
$ echo 'package main; func main(){}' > /tmp/x.go && GOOS=darwin go build -o /dev/null /tmp/x.go  # cross-compile
```
