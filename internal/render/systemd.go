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
// afterEnforce adds After=devm-enforce.service to the declarative
// path's ordering, so the service starts only once startup: commands
// and network enforcement have run. Callers pass true when the config
// has startup: commands configured; it has no effect on the verbatim
// Systemd override path.
//
// The returned bytes are the unit file contents — write at
// /etc/systemd/system/<name>.service inside the VM.
func RenderService(name string, svc schema.Service, afterEnforce bool) []byte {
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
	if afterEnforce {
		after = append(after, "devm-enforce.service")
	}
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

// RenderStartupUnit generates the devm-startup.service unit, which runs
// every startup: command in order on every boot, before network
// enforcement (devm-enforce.service) locks the guest down. Each command
// runs as its own ExecStart= under `bash -o pipefail -c`, so a failing
// command stops the unit (and blocks devm-enforce.service) rather than
// silently continuing.
//
// The returned bytes are the unit file contents — write at
// /etc/systemd/system/devm-startup.service inside the VM.
func RenderStartupUnit(cmds []string) []byte {
	var b strings.Builder

	b.WriteString("[Unit]\n")
	b.WriteString("Description=devm startup commands\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("Before=devm-enforce.service\n")

	b.WriteString("\n[Service]\n")
	b.WriteString("Type=oneshot\n")
	b.WriteString("RemainAfterExit=yes\n")
	for _, cmd := range cmds {
		fmt.Fprintf(&b, "ExecStart=/bin/bash -o pipefail -c '%s'\n", shellSingleQuoteEscape(cmd))
	}

	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")

	return []byte(b.String())
}

// RenderEnforceUnit generates the devm-enforce.service unit, which applies
// the devm nftables egress policy after devm-startup.service has run.
// Ordered before it in the per-service units (via afterEnforce=true) so
// declared services start only once enforcement is in place.
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

// shellSingleQuoteEscape escapes a string for embedding inside single
// quotes in a POSIX shell command: each literal `'` becomes `'\''`
// (close the quote, escaped literal quote, reopen the quote).
func shellSingleQuoteEscape(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
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
