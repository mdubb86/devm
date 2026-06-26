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
		user = "dev"
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
			fmt.Fprintf(&b, "Environment=%s=%s\n", k, svc.Env[k])
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

// systemdQuoteArgv joins argv with spaces for systemd's ExecStart=.
// Systemd's ExecStart parser handles quoting itself when needed; we
// just space-join. If a user has whitespace in their argv elements,
// they should drop down to the systemd: full-override field.
func systemdQuoteArgv(argv []string) string {
	return strings.Join(argv, " ")
}
