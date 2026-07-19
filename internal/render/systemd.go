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
// The declarative path declares WantedBy=devm.target ([Install]), but
// that's enable-bookkeeping, not the start trigger: the composed
// provisioning script (RenderProvisionScript) starts each declared
// service explicitly and health-polls it BEFORE running `systemctl
// start devm.target`, so a broken service aborts before access is
// granted. Ordering relative to enforcement is therefore not a
// systemd concern here.
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
	b.WriteString("WantedBy=devm.target\n")

	return []byte(b.String())
}

// RenderStartupScript generates the bash script the composed
// provisioning script (RenderProvisionScript) invokes via
// `/opt/devm/with-devm-env bash /opt/devm/startup.sh`. Each cfg.Startup
// entry is emitted verbatim, one per line. Entries that reference a
// named script (`>NAME`) are replaced by the underlying commands of
// `scripts[NAME]`, each on its own line — startup.sh runs as one bash
// process, so no && joining is needed (vars carry across lines
// naturally). `set -eo pipefail` means any failing command aborts the
// run, matching install:'s failure semantics. An empty cmds produces
// just the shebang + set line: a valid no-op that exits 0.
//
// The returned bytes are the script contents — write at
// /opt/devm/startup.sh inside the VM, mode 0755.
func RenderStartupScript(cmds []string, scripts map[string][]string) []byte {
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("set -eo pipefail\n")
	for _, cmd := range cmds {
		if name, ok := schema.ParseScriptRef(cmd); ok {
			for _, sub := range scripts[name] {
				b.WriteString(sub)
				b.WriteString("\n")
			}
			continue
		}
		b.WriteString(cmd)
		b.WriteString("\n")
	}
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
