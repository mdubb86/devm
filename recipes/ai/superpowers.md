---
name: tool/ai/superpowers
category: ai
display_name: Superpowers (Claude Code plugin)
description: Install Anthropic's Superpowers plugin at user scope; configure the brainstorming visual companion so a Mac browser can talk to the in-VM server.
keywords: superpowers claude-code plugin brainstorming visual companion skills
since: recipes-v1.0.0
---

# Superpowers

Anthropic-official Claude Code plugin. Most of it is skills that run stock
inside a devm VM. The one devm-specific wrinkle is the **brainstorming
visual companion** — an HTTP+WebSocket server designed for browser and
server on one machine. In devm the browser lives on the Mac, the server
in the VM, so it needs a pinned port, a reachable bind, an https-aware
socket, and a Mac-openable URL.

## devm.yaml additions

```yaml
packages:
  - git                                           # claude plugin marketplace add shells out to git

scripts:
  install-superpowers:
    - claude plugin marketplace add https://github.com/anthropics/claude-plugins-official
    - claude plugin install superpowers@claude-plugins-official

install:
  - ">install-superpowers"

env:
  BRAINSTORM_PORT: "5180"                          # pin the port
  BRAINSTORM_HOST: "0.0.0.0"                        # bind all ifaces (softnet doesn't route to loopback)
  BRAINSTORM_URL_HOST: brainstorm.myproject.test    # replace myproject with your devm project name
  BRAINSTORM_URL_PORT: ""                           # empty → omit port from printed URL (Caddy fronts :443)
  BRAINSTORM_URL_SCHEME: https                      # https via Caddy → enables wss

services:
  brainstorm:
    port: 5180
    hostname: brainstorm.myproject.test             # same as BRAINSTORM_URL_HOST
    # NOT direct: — HTTP-fronted via Caddy at :443, so the URL stays port-free

network:
  allow:
    - github.com/obra/*              # marketplace + superpowers source (git clone)
    - codeload.github.com/obra/*     # github archive/tarball downloads
    - objects.githubusercontent.com  # github LFS (not owner-scoped in the URL)
    - raw.githubusercontent.com/obra/*  # plugin marketplace index
```

The github entries are scoped to the `obra` owner — installing plugins
from other marketplaces/owners needs their own `github.com/<owner>/*`
(+ codeload/raw) entries.

`env` and `services` are the live bucket — after adding, `devm reconcile`
applies them without restart, then `devm route vm`.

**Prereq**: the Claude Code CLI must be installed. Use `tool/ai/claude`
(the `claude` recipe) first; the plugin install steps here call `claude
plugin` at provision time and will fail loud if the binary isn't on PATH.

## Brainstorming companion — on-demand plugin edits

Superpowers' `start-server.sh` and `server.cjs` don't honor most of the
env vars out of the box. Five small sed edits fix that. **They are NOT
wired into `install:`** — a Superpowers upgrade would silently revert
them and the companion would die with no signal. Run these when setting
up the companion (ask Claude to re-apply if an upgrade breaks a line —
each edit is described by *intent*, so it can be relocated):

```bash
D=$(ls -d ~/.claude/plugins/cache/claude-plugins-official/superpowers/*/skills/brainstorming/scripts | tail -1)

# 1 — env fallback for bind + url host (flags still win)
sed -i 's@^BIND_HOST="127.0.0.1"@BIND_HOST="${BRAINSTORM_HOST:-127.0.0.1}"@' "$D/start-server.sh"
sed -i 's@^URL_HOST=""@URL_HOST="${BRAINSTORM_URL_HOST:-}"@'                  "$D/start-server.sh"
chmod 755 "$D/start-server.sh"      # sed -i drops the exec bit

# 2 — omit port from URL when BRAINSTORM_URL_PORT is empty
sed -i "s@return 'http://' + urlHostForHttp(URL_HOST) + ':' + PORT + '/?key=' + TOKEN;@const urlPort = process.env.BRAINSTORM_URL_PORT === undefined ? String(PORT) : process.env.BRAINSTORM_URL_PORT;\n  return 'http://' + urlHostForHttp(URL_HOST) + (urlPort ? ':' + urlPort : '') + '/?key=' + TOKEN;@" "$D/server.cjs"

# 3 — client: wss on https pages (avoids mixed content)
sed -i "s@'ws://' + window.location.host@(window.location.protocol === 'https:' ? 'wss://' : 'ws://') + window.location.host@" "$D/helper.js"

# 4 — server: accept https origin on the WS upgrade
sed -i "s@return origin === 'http://' + host;@return origin === 'http://' + host || origin === 'https://' + host;@" "$D/server.cjs"

# 5 — printed URL scheme from BRAINSTORM_URL_SCHEME (default http)
sed -i "s@'http://' + urlHostForHttp(URL_HOST)@(process.env.BRAINSTORM_URL_SCHEME || 'http') + '://' + urlHostForHttp(URL_HOST)@" "$D/server.cjs"

# verify — each MUST print 1; node --check MUST pass. A 0 means an upgrade
# moved a line; read the current source and re-apply that edit from its intent.
echo -n "1 bind fallback:  "; grep -cF 'BIND_HOST="${BRAINSTORM_HOST' "$D/start-server.sh"
echo -n "2 url port opt:   "; grep -cF 'BRAINSTORM_URL_PORT === undefined' "$D/server.cjs"
echo -n "3 client wss:     "; grep -cF "window.location.protocol === 'https:' ? 'wss://'" "$D/helper.js"
echo -n "4 origin https:   "; grep -cF "origin === 'https://' + host" "$D/server.cjs"
echo -n "5 url scheme:     "; grep -cF "BRAINSTORM_URL_SCHEME || 'http'" "$D/server.cjs"
node --check "$D/server.cjs" && echo "server.cjs OK"
```

Launch (no flags needed — the launcher reads env):

```bash
"$D/start-server.sh" --project-dir "$PWD"   # persists port+token for reconnect
```

Expected startup JSON — the tell the edits held:

```json
{"type":"server-started","port":5180,"host":"0.0.0.0",
 "url":"https://brainstorm.myproject.test/?key=<token>", …}
```

Open the printed `url` on the Mac. `host:"0.0.0.0"` + a port-free
`https://…` URL = good. `host:"127.0.0.1"` or a `:5180` in the url =
an edit didn't hold; re-run.

Want plain http instead? Skip edits 3–5 and set `BRAINSTORM_URL_SCHEME`
unset. Fewer patches; only tradeoff is the "Not secure" badge (see
Security note).

## Notes

- **User-scope install**: lands in `~/.claude/plugins/cache/…` — available
  across every project on the machine. `install:` runs once per VM
  lifetime; `claude plugin marketplace add` is idempotent.
- **Why `0.0.0.0`**: `devm route vm` dials the service port over softnet
  → the guest's primary interface, not loopback. A `127.0.0.1` bind is
  invisible from the Mac.
- **Security**: the bind is reachable only over the private Mac↔VM
  softnet link (no `expose_host` → not on LAN). Caddy terminates TLS
  on the Mac and forwards plain http to the backend, so the token is
  plaintext on that one private hop — https buys consistency, not
  confidentiality.

## Failure modes

| symptom | fix |
|---|---|
| startup JSON `"host":"127.0.0.1"` or `":5180"` in url | edit reverted by a plugin upgrade → re-run the sed block (verify prints a 0) |
| `Permission denied` launching `start-server.sh` | `sed -i` stripped exec bit → `chmod 755 "$D/start-server.sh"` |
| hostname resolves but page is blank HTTP 200 | Caddy has no route — `services:` entry missing or `devm reconcile` / `devm route vm` not run |
| page loads but "Companion paused", clicks never arrive | WS rejected. https path → edits 3+4 not applied; http path → origin/host mismatch. Re-run edits; confirm page scheme matches how you opened it |
| `no service listening at 127.42.0.N:5180` | server bound loopback (see 0.0.0.0 note) or not running |

**Upstream**: landing the five edits (env-settable host/url-host,
port-omit, url-scheme, scheme-adaptive client, https-aware origin check)
collapses this recipe to the yaml alone. Track: obra/superpowers #2041
(remote-bind); #1991/#1999 (env-configurability precedent).
