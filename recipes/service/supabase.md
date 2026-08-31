---
name: tool/service/supabase
category: service
display_name: Supabase
description: "Local Supabase stack via `supabase start`. HTTP services (Kong / Studio / Mailpit) ride devm's proxy; Postgres uses `direct: true`. Same hostname works on Mac AND inside the VM."
keywords: supabase postgres kong studio mailpit inbucket gotrue realtime auth database docker direct
since: recipes-vNEXT
---

# Supabase

Run the standard `supabase start` stack inside a devm VM and expose it
at stable `*.test` hostnames that resolve identically on the Mac and
inside the VM.

Two routing patterns are combined:

- **HTTP services** — Kong (`api`), Studio, Mailpit — ride the daemon
  HTTP proxy on the Mac (`:80/:443`, TLS via devm's CA). One `hostname:`
  per service.
- **Postgres** (raw TCP) — uses `direct: true`, which does two things
  automatically: route-aware DNS (`db.<proj>.test → VM_IP` on the Mac,
  `→ 127.0.0.1` inside the VM), and a direct Mac→VM_IP path for the
  raw TCP traffic. No proxy hop, no per-port fiddling.

Net: `psql postgresql://postgres:postgres@db.<proj>.test:54322/postgres`
works from the Mac AND from inside the VM, unchanged.

## devm.yaml additions

```yaml
docker: true                          # supabase start spins up ~10 containers

env:
  # The CLI POSTs telemetry on every invocation. That host isn't in the
  # egress allowlist below, so iron-proxy 403s it and the CLI prints a
  # stacktrace over the output of every `supabase` command. Opting out
  # is cleaner than allowing the egress.
  DO_NOT_TRACK: "1"

packages:
  - postgresql-client   # `psql` — handy for local queries, migrations, troubleshooting

scripts:
  # supabase CLI: canonical `.deb` per supabase's docs (Releases page).
  # `.deb` releases only publish version-embedded names — no `latest`
  # alias — so resolve the tag by following github.com's own
  # /releases/latest → /releases/tag/vX.Y.Z redirect, then download the
  # correctly-named `.deb`. dpkg tracks the package and future re-runs
  # (new release) replace files cleanly. Broken into steps here because
  # the tag has to survive between commands — `scripts:` runs them under
  # one shell so `$TAG` stays live.
  install-supabase:
    - TAG=$(curl -sIL -o /dev/null -w '%{url_effective}' https://github.com/supabase/cli/releases/latest | xargs basename)
    - curl -fsSL -o /tmp/supabase.deb "https://github.com/supabase/cli/releases/download/${TAG}/supabase_${TAG#v}_linux_arm64.deb"
    - sudo dpkg -i /tmp/supabase.deb
    - rm /tmp/supabase.deb

install:
  - ">install-supabase"

services:
  supabase-api:
    port: 54321
    hostname: api.<proj>.test         # Kong / PostgREST / GoTrue / Realtime(WS)
  supabase-studio:
    port: 54323
    hostname: studio.<proj>.test
  supabase-mail:
    port: 54324
    hostname: mail.<proj>.test         # Mailpit — optional but recipe includes it for email flows
  supabase-db:
    port: 54322
    hostname: db.<proj>.test
    direct: true                      # raw TCP: DNS→VM_IP, +1 firewall rule (auto)
  # supabase-pooler:
  #   port: 54329
  #   hostname: pooler.<proj>.test
  #   direct: true                    # optional, for high-connection-count apps

network:
  allow:
    - github.com/supabase/cli/*           # supabase CLI release download + /releases/latest redirect
    # release-asset storage — supabase/cli assets only
    - release-assets.githubusercontent.com/github-production-release-asset/314160187/*
    - public.ecr.aws                      # supabase container image registry (manifests)
    - "*.cloudfront.net"                  # ECR Public blob storage (image layers)
    # ECR Public returns HTTP 307s to CloudFront for layer blobs, and
    # AWS can rotate which distribution (`dXXX.cloudfront.net`) serves
    # them. iron-proxy's allow syntax is a domain glob — `*.cloudfront.net`
    # matches every subdomain (and bare `cloudfront.net`) so the rule
    # survives AWS rebalancing.
```

Then `devm route vm` (auto-applied on `devm shell` when no routes exist)
points every hostname at the VM.

## Applying to an existing Node.js project

Two supabase npm packages sometimes appear in `package.json` — they do
different jobs and only one is redundant with the recipe's `.deb`
install:

- **`@supabase/supabase-js`** (`dependencies`) — the runtime JS client
  the app imports (`import { createClient } from '@supabase/supabase-js'`).
  Unrelated to the CLI. Keep it.
- **`supabase`** (`devDependencies`) — the same CLI as the `.deb`,
  wrapped for npm so `pnpm supabase start` works. Once the recipe
  installs the `.deb`, this npm entry is redundant and risks version
  drift between the two (native `.deb` at latest vs. npm pinned to
  the last `pnpm install`).

If `supabase` is in `devDependencies`, ask before removing it — the
project may deliberately pin a version. If it's absent, nothing to do.

## Steer agents at the CLI, not `docker exec`

Add to the project's `CLAUDE.md`:

```
- Use the `supabase` CLI for all DB work — `supabase db query`,
  `supabase db reset`, `supabase migration new`, `supabase gen types`.
  Reach for `docker exec supabase_db_* psql` only for CLI-unavailable
  ad-hoc probes.
```

Agents in the VM default to `docker exec … psql` because it always
works; the CLI is the officially supported interface, keeps generated
types in sync, and its subcommands handle migrations, seeds, and
resets cleanly.

## Supabase-specific config fixes

These are Supabase quirks devm can't know about.

### 1. Pin auth URLs + register custom email templates

In `supabase/config.toml` — pin `site_url` + `external_url` via
`env()` so devm controls them, and register custom templates so
GoTrue stops using its broken defaults:

```toml
[auth]
site_url = "env(PUBLIC_SITE_URL)"
external_url = "env(PUBLIC_SUPABASE_AUTH_EXTERNAL_URL)"
additional_redirect_urls = ["env(PUBLIC_SITE_URL)"]   # bare origin covers sub-paths

[auth.email]
enable_confirmations = true
double_confirm_changes = true

[auth.email.template.confirmation]
subject = "Confirm your email"
content_path = "./supabase/templates/confirmation.html"

[auth.email.template.magic_link]
subject = "Your magic link"
content_path = "./supabase/templates/magic_link.html"

[auth.email.template.recovery]
subject = "Reset your password"
content_path = "./supabase/templates/recovery.html"

[auth.email.template.email_change]
subject = "Confirm your email change"
content_path = "./supabase/templates/email_change.html"
```

Matching `devm.yaml` `env:` block:

```yaml
env:
  PUBLIC_SITE_URL: https://<proj>.test
  PUBLIC_SUPABASE_AUTH_EXTERNAL_URL: https://api.<proj>.test/auth/v1
```

Everything is `https` — an `http://` API base in an `https://` app
page would be blocked by the browser as mixed content.

Gotchas — all silent failures:

- **Add the devm var first, then reference it.** An unset `env(NAME)`
  is a fatal parse error for the whole stack (Postgres included).
- **`env()` is whole-value only** — `env(NAME)/**` reaches GoTrue as
  the literal string. Any suffixed URL needs its own variable.
- **`redirect_to` mismatches silently fall back to `site_url`** and
  still return 200 — assert the emitted link, not the status code.
- **`[auth]` changes need `supabase stop && supabase start`** — `db:reset`
  won't re-read them.
- **`[api] external_url` is a no-op** — CLI reconstructs it from
  `hostname + api.port`. Use `[auth] external_url` for the auth email
  host.
- **`SUPABASE_SERVICES_HOSTNAME` isn't usable with devm's routing** —
  it emits one global hostname with real ports appended, incompatible
  with per-service routes on 80/443.

### 2. Ship the four custom templates

Under `supabase/templates/*.html`. The default GoTrue templates build
their button from `{{ .ConfirmationURL }}`, which GoTrue assembles
from `API_EXTERNAL_URL` — and the Supabase CLI hardcodes that to
`http://127.0.0.1:54321`. Emails then contain a loopback link nobody
off-CLI-host can click.

Fix: build the link by hand from `{{ .SiteURL }}` + `{{ .TokenHash }}`,
pointing at the app's `/auth/confirm` route. Sample
`confirmation.html`:

```html
<!DOCTYPE html>
<html><body>
  <h1>Confirm your email</h1>
  <p>Click to confirm ({{ .Email }}):</p>
  <a href="{{ .SiteURL }}/auth/confirm?token_hash={{ .TokenHash }}&type=email">
    Confirm email
  </a>
</body></html>
```

The other three templates use the same shape; only the `&type=` value
changes:

| Template | `&type=` |
|---|---|
| `confirmation.html` (signup) | `email` |
| `magic_link.html` | `email` |
| `recovery.html` (password reset) | `recovery` |
| `email_change.html` | `email_change` |

The `type` value has to match what the app's verify route hands to
`supabase.auth.verifyOtp({ token_hash, type })`.

### 3. Implement the app's `/auth/confirm` route

The templates point at `/auth/confirm`. The app has to implement it:

```
GET /auth/confirm?token_hash=...&type=...
  → supabase.auth.verifyOtp({ token_hash, type })
  → redirect to your app on success
```

Without this route the emailed link 404s even though the URL is now
correct.

### 4. Generate app env from the running stack — with hostname URLs

```bash
eval "$(supabase status -o env)"    # gets keys + default-port URLs
cat > .env.local <<EOF
NEXT_PUBLIC_SUPABASE_URL=https://api.<proj>.test
NEXT_PUBLIC_SUPABASE_ANON_KEY=${ANON_KEY}
NEXT_PUBLIC_SITE_URL=https://<proj>.test
DATABASE_URL=postgresql://postgres:postgres@db.<proj>.test:54322/postgres
EOF
```

Take keys from `supabase status`, but **override the URLs** to hostnames.
`--override-name` on `supabase status -o env` renames the KEY, not the
value — don't try to pass a URL to it.

### 5. Framework dev-origin allowlist

If the app runs a dev server on the VM:

- Next.js: add `<proj>.test`, `api.<proj>.test` to `allowedDevOrigins`
- Vite: add to `server.allowedHosts`
- Image loaders: add to `remotePatterns` in `next.config.js`

Missing this means HMR / image loaders reject the hostnames.

## Verifying

```
devm shell
$ supabase --version                                              # CLI installed
$ supabase init && supabase start                                 # ~5-10 min first time
$ curl -sS https://api.<proj>.test/rest/v1/                       # PostgREST reachable
$ curl -sS https://studio.<proj>.test | head -20                  # Studio HTML
$ curl -sS https://mail.<proj>.test/api/v1/messages               # Mailpit API
$ psql postgresql://postgres:postgres@db.<proj>.test:54322/postgres -c 'SELECT 1'
```

## Notes

- **`docker: true` is load-bearing.** `supabase start` orchestrates ~10
  Docker containers. See `recipes/service/docker.md` for what devm's
  built-in docker feature actually provides.
- **App servers behind a route must bind `0.0.0.0`.** A loopback-bound
  listener returns 502 through the proxy — reads like a crashed
  service. Applies to `vite dev --host`, `wrangler dev --ip 0.0.0.0`,
  and anything else you route to.
- **Realtime rides `api.<proj>.test`.** WebSocket upgrades flow through
  the daemon HTTP proxy. No separate hostname.
- **Analytics (Logflare, port 54327) is deliberately not exposed.** It's
  usually only consumed by other Supabase services internally; add a
  hostname if a project actually needs external access.
- **First `supabase start` is slow** (~5-10 min pulling ~10 container
  images through iron-proxy). Subsequent starts reuse the local docker
  image cache.
- **After `devm teardown`, run the full rehydration in one shot** —
  `supabase start && supabase db reset` (+ `playwright install chromium`
  if the project uses Playwright, since `~/.cache/ms-playwright` is
  VM-local). Ordering bugs (a test that reads `.env` before the step
  generating it) hide behind warm state and only surface on a cold VM.
- **DNS TTL for `direct:` services is near-zero** — the VM's DHCP address
  changes on restart, and clients that cache beyond TTL may need a
  reconnect. Relevant if you leave `psql` sessions open across VM
  bounces.

Upstream: <https://supabase.com/docs/guides/cli/local-development>
