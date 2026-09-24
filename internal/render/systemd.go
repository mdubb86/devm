package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/scriptfile"
)

// ServiceWrapperGuestDir is the guest-side directory holding one
// wrapper script per function-ref (ExecFunc) service. A function-form
// service's ExecStart= points at <ServiceWrapperGuestDir>/<name>.sh.
const ServiceWrapperGuestDir = "/opt/devm/service-wrappers"

// RenderService generates a systemd unit file for the given service,
// plus — for a function-ref (ExecFunc) service — the wrapper script
// its ExecStart= points at. If svc.Systemd is non-empty, the unit is
// returned verbatim (full-override path; the user is responsible for
// After=devm-ready.target etc.) and wrapperPath/wrapperBody are empty.
// Otherwise the unit is generated from the declarative fields with
// sensible defaults that hook into devm-ready.target.
//
// env and pathPrepend seed the wrapper's exports and PATH prepend
// (scriptfile.WrapperInput); svc.Env is merged on top so per-service
// values win, matching the unit's own EnvironmentFile+Environment=
// precedence. An ExecArgv (argv-form) service emits ExecStart=<argv>
// directly and returns an empty wrapperPath/wrapperBody — no wrapper
// is rendered.
//
// The declarative path declares WantedBy=devm.target ([Install]), but
// that's enable-bookkeeping, not the start trigger: the composed
// provisioning script (RenderProvisionScript) starts each declared
// service explicitly and health-polls it BEFORE running `systemctl
// start devm.target`, so a broken service aborts before access is
// granted. Ordering relative to enforcement is therefore not a
// systemd concern here.
//
// unitBody is the unit file contents — write at
// /etc/systemd/system/<name>.service inside the VM. wrapperBody, when
// non-empty, is written at wrapperPath (== ServiceWrapperGuestDir +
// "/" + name + ".sh"), mode 0755.
func RenderService(svc schema.Service, name string, env map[string]string, pathPrepend []string) (unitBody []byte, wrapperPath string, wrapperBody []byte, err error) {
	if svc.Systemd != "" {
		// Trim trailing whitespace, ensure exactly one final newline.
		return []byte(strings.TrimRight(svc.Systemd, " \t\n") + "\n"), "", nil, nil
	}

	var b strings.Builder

	// [Unit]
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=devm service: %s\n", name)
	// devm-ready.target is the base infrastructure target written by
	// the base image, gating declared services on network readiness.
	after := append([]string{"devm-ready.target"}, svc.After...)
	fmt.Fprintf(&b, "After=%s\n", strings.Join(after, " "))
	b.WriteString("Requires=devm-ready.target\n")

	// [Service]
	b.WriteString("\n[Service]\n")
	switch {
	case svc.ExecFunc != "":
		wrapperPath = ServiceWrapperGuestDir + "/" + name + ".sh"
		wrapEnv := make(map[string]string, len(env)+len(svc.Env))
		for k, v := range env {
			wrapEnv[k] = v
		}
		for k, v := range svc.Env {
			wrapEnv[k] = v.Render()
		}
		body, werr := scriptfile.RenderWrapper(scriptfile.WrapperInput{
			FunctionName: svc.ExecFunc,
			Env:          wrapEnv,
			PathPrepend:  pathPrepend,
			Cwd:          svc.WorkDir,
		})
		if werr != nil {
			return nil, "", nil, fmt.Errorf("render service %q wrapper: %w", name, werr)
		}
		wrapperBody = []byte(body)
		fmt.Fprintf(&b, "ExecStart=%s\n", wrapperPath)
	case len(svc.ExecArgv) > 0:
		fmt.Fprintf(&b, "ExecStart=%s\n", systemdQuoteArgv(svc.ExecArgv))
	}
	if svc.WorkDir != "" {
		fmt.Fprintf(&b, "WorkingDirectory=%s\n", svc.WorkDir)
	}
	user := svc.User
	if user == "" {
		user = "devm"
	}
	fmt.Fprintf(&b, "User=%s\n", user)
	// EnvironmentFile=-/etc/environment sources machine-wide env
	// (PATH, cfg.Env, NODE_EXTRA_CA_CERTS, etc.) written by the bundle
	// install path. Leading `-` makes it non-fatal if the file hasn't
	// landed yet (first-boot edge case). Ordering: EnvironmentFile=
	// values are applied first, then any Environment= lines below
	// override for the same key — so per-service env still wins.
	b.WriteString("EnvironmentFile=-/etc/environment\n")
	// Sorted env keys so the rendered output is deterministic — tests
	// rely on it.
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

	return []byte(b.String()), wrapperPath, wrapperBody, nil
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
