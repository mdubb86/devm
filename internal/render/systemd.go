package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mdubb86/devm/internal/schema"
)

// RenderService generates a systemd unit file for the given service.
// If svc.Systemd is non-empty, returns it verbatim (full-override
// path; the user is responsible for After=devm-ready.target etc.).
// Otherwise generates from the declarative fields with sensible
// defaults that hook into devm-ready.target.
//
// The declarative path always adds After=devm-enforce.service, so a
// declared service starts only once devm-startup.service and network
// enforcement have run (see internal/provision's setupBootEnforcement
// — the mechanism is always registered, for every project). It has no
// effect on the verbatim Systemd override path.
//
// The returned bytes are the unit file contents — write at
// /etc/systemd/system/<name>.service inside the VM.
func RenderService(name string, svc schema.Service) []byte {
	if svc.Systemd != "" {
		// Trim trailing whitespace, ensure exactly one final newline.
		return []byte(strings.TrimRight(svc.Systemd, " \t\n") + "\n")
	}

	var b strings.Builder

	// [Unit]
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=devm service: %s\n", name)
	// devm-ready.target is the base infrastructure target written by
	// the base image (Task 4). Includes dnsmasq + Caddy + network.
	after := append([]string{"devm-ready.target"}, svc.After...)
	after = append(after, "devm-enforce.service")
	fmt.Fprintf(&b, "After=%s\n", strings.Join(after, " "))
	b.WriteString("Requires=devm-ready.target\n")

	// [Service]
	b.WriteString("\n[Service]\n")
	if len(svc.Exec) > 0 {
		fmt.Fprintf(&b, "ExecStart=%s\n", systemdQuoteArgv(svc.Exec))
	}
	if svc.WorkDir != "" {
		fmt.Fprintf(&b, "WorkingDirectory=%s\n", svc.WorkDir)
	}
	user := svc.User
	if user == "" {
		user = "devm"
	}
	fmt.Fprintf(&b, "User=%s\n", user)
	// Sorted env keys so the rendered output is deterministic — the
	// Caddyfile renderer made the same choice and tests rely on it.
	if len(svc.Env) > 0 {
		keys := make([]string, 0, len(svc.Env))
		for k := range svc.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "Environment=%s=%s\n", k, svc.Env[k].Render())
		}
	}
	restart := svc.Restart
	if restart == "" {
		restart = "on-failure"
	}
	fmt.Fprintf(&b, "Restart=%s\n", restart)

	// [Install]
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")

	return []byte(b.String())
}

// RenderStartupScript generates the bash script devm-startup.service
// executes: each cfg.Startup command verbatim, one per line — a
// script body needs no single-quote escaping, unlike an ExecStart=
// argument. `set -eo pipefail` means a failing command aborts the
// run, matching install:'s "a failing command fails the run"
// semantics. An empty cmds produces just the shebang + set line: a
// valid no-op that exits 0.
//
// The returned bytes are the script contents — write at
// /opt/devm/startup.sh inside the VM, mode 0755.
func RenderStartupScript(cmds []string) []byte {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("set -eo pipefail\n")
	for _, cmd := range cmds {
		b.WriteString(cmd)
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// RenderStartupUnit generates the devm-startup.service unit. Its
// content is STABLE — it never changes with cfg.Startup; the commands
// live in /opt/devm/startup.sh (RenderStartupScript), which the
// provisioner's bundle re-pipe rewrites on every cold-start. Always
// registered for every project (see internal/provision's
// setupBootEnforcement) — not opt-in.
//
// A failing startup: command does NOT block devm-enforce.service:
// devm-enforce.service has only After=devm-startup.service (no
// Requires=/BindsTo=), so systemd starts it regardless of whether
// devm-startup.service succeeded. This is fail-safe — egress
// enforcement is always applied even when a startup command fails.
//
// The returned bytes are the unit file contents — write at
// /etc/systemd/system/devm-startup.service inside the VM.
func RenderStartupUnit() []byte {
	var b strings.Builder

	b.WriteString("[Unit]\n")
	b.WriteString("Description=devm startup commands\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("Before=devm-enforce.service\n")

	b.WriteString("\n[Service]\n")
	b.WriteString("Type=oneshot\n")
	b.WriteString("RemainAfterExit=yes\n")
	b.WriteString("ExecStart=/opt/devm/startup.sh\n")

	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")

	return []byte(b.String())
}

// RenderEnforceUnit generates the devm-enforce.service unit, which applies
// the devm nftables egress policy after devm-startup.service has run.
// Ordered before it in the per-service units (RenderService always
// appends devm-enforce.service to After=) so declared services start
// only once enforcement is in place.
//
// The returned bytes are the unit file contents — write at
// /etc/systemd/system/devm-enforce.service inside the VM.
func RenderEnforceUnit() []byte {
	var b strings.Builder

	b.WriteString("[Unit]\n")
	b.WriteString("Description=devm network enforcement\n")
	b.WriteString("After=devm-startup.service\n")

	b.WriteString("\n[Service]\n")
	b.WriteString("Type=oneshot\n")
	b.WriteString("RemainAfterExit=yes\n")
	b.WriteString("ExecStart=/usr/sbin/nft -f /etc/nftables.conf\n")

	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")

	return []byte(b.String())
}

// systemdQuoteArgv renders an argv slice for systemd ExecStart=. Systemd's
// parser splits ExecStart on whitespace unless args are double-quoted with
// C-style escapes (see systemd.service(5) COMMAND LINES). Elements that
// contain whitespace, double quotes, or backslashes are wrapped in double
// quotes with `"` and `\` backslash-escaped. Plain elements pass through
// unquoted so simple ExecStart lines stay readable.
func systemdQuoteArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = quoteSystemdArg(a)
	}
	return strings.Join(parts, " ")
}

func quoteSystemdArg(a string) string {
	// Escape systemd specifiers first (%s = user shell, %h = user home, …
	// systemd.unit(5) SPECIFIERS). A literal `%` in an argv element must
	// be doubled or systemd swaps it for its own value silently.
	a = strings.ReplaceAll(a, "%", "%%")
	if a == "" {
		return `""`
	}
	if !strings.ContainsAny(a, " \t\n\"\\") {
		return a
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range a {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
