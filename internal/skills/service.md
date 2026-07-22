---
name: service
description: devm daemon — install, manage, and troubleshoot the LaunchDaemon that owns Tart VM lifecycle and iron-proxy. Use when the daemon won't start, won't respond, or when first wiring devm onto a new machine.
---

# devm daemon reference

The devm daemon is a macOS LaunchDaemon (`com.devm.service`) that runs as your user. It owns VM lifecycle (start, stop), drives the iron-proxy on the Mac, and serves DNS for `*.test` names. The daemon is managed via a set of root-level and `devm service` subcommands.

---

## One-time install

```
devm install
```

Registers the daemon with launchd. It:

1. Writes the launchd plist to `/Library/LaunchDaemons/com.devm.service.plist` (via `sudo`).
2. Configures `/etc/resolver/test` so the system resolver forwards `*.test` queries to the daemon's DNS port.
3. Generates a local CA (if none exists) and trusts it in the System Keychain.
4. Bootstraps the daemon and starts it immediately.

`devm install` requires `sudo` for the launchctl and resolver steps; it will prompt for your password (or Touch ID) once.

```
devm uninstall
```

Removes the plist, unregisters the daemon from launchd, removes `/etc/resolver/test`, and removes the CA certificate from the System Keychain. Requires `sudo`.

---

## Day-to-day management

```
devm service status
```

Prints `running`, `stopped`, or `not installed`. No sudo required.

```
devm service start
```

Starts the daemon via launchctl. Requires sudo (Touch ID prompt).

```
devm service stop
```

Stops the daemon via launchctl. Requires sudo (Touch ID prompt).

```
devm service restart
```

Stops then starts the daemon. Requires sudo (Touch ID prompt).

---

## `devm serve`

The entry point for the daemon process itself. Launchd invokes `devm serve` automatically when the service starts; you do not run this yourself under normal use.

Useful for ad-hoc foreground debugging: running `devm serve` directly in a terminal starts the daemon attached to your terminal's stdout/stderr so you can watch logs live.

---

## Daemon log location

Daemon stdout and stderr are written to your home directory's `Library/Logs`:

| Stream | Path |
|---|---|
| stdout | `~/Library/Logs/com.devm.service.out.log` |
| stderr | `~/Library/Logs/com.devm.service.err.log` |

To tail both:

```
tail -f ~/Library/Logs/com.devm.service.out.log ~/Library/Logs/com.devm.service.err.log
```

Install and uninstall subprocess output is captured separately to `~/Library/Logs/devm/install.log`. On install failure, devm tails the last 30 lines automatically; you can re-read the full file at any time.

---

## Common failures

### Daemon not running

`devm shell` and other commands that call the daemon API will fail with a connection error if the daemon is down. Check the state first:

```
devm service status
```

If `stopped`, start it:

```
devm service start
```

If the daemon fails to stay up, tail the log to find the error:

```
tail -n 50 ~/Library/Logs/com.devm.service.err.log
```

### Plist points at a stale binary path

After a manual rebuild (e.g., `go build`) that places the binary at a new path, the existing plist still points at the old location. Re-running `devm install` rewrites the plist with the current binary path and restarts the daemon.

### Keychain access

The daemon runs as your user but in a LaunchDaemon context. macOS does not grant LaunchDaemon processes access to the user's login keychain, even when the `UserName` key in the plist matches your account.

This is why `devm secret` resolves keychain lookups in the CLI process (which has login keychain access) and passes the resolved values to the daemon at start time as proxy-tokens. See `devm skills get secrets` for details on how secrets are declared and handed off.
