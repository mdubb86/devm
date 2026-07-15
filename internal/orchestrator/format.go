package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// StatusResult is what `devm status` produces. HasProject is false
// when there's no devm.yaml in cwd — in that case only Daemon fields
// are populated, and the sandbox/routing/DNS sections are skipped
// entirely at format time. This lets `devm status` be a useful
// "is the daemon happy?" probe outside a project directory.
type StatusResult struct {
	HasProject      bool
	Daemon          DaemonStatus
	Sandbox         string
	State           string // "running" | "stopped" | "absent"
	Sessions        []Session
	PendingLive     int
	PendingRecreate int
	Drift           []DriftItem
	Routing         serviceapi.RoutingStatus

	// DNSHealthy is true when the system resolver can reach the daemon's
	// DNS server for *.test names. DNSError describes the failure when
	// DNSHealthy is false. Both populated by RunStatus.
	DNSHealthy bool
	DNSError   string

	// CATrusted is true when devm's local CA root is installed in
	// the System Keychain. False means HTTPS will produce browser
	// warnings (devm install fixes this).
	CATrusted bool

	// ProxyHealthy is true when something is listening on :443.
	// Populated by a 500ms TCP dial. False means the daemon isn't
	// running or launchd's socket activation didn't hand off the
	// listeners properly.
	ProxyHealthy bool
	ProxyError   string

	// ProxyHealth is the daemon's per-project iron-proxy verdict (from
	// /handshake): missing, stale, or ok. Nil when the daemon was
	// unreachable — the format layer omits the line entirely rather
	// than claiming a status it doesn't have.
	ProxyHealth *serviceapi.ProxyHealth
}

// DriftItem is one piece of mismatch between snapshot and live VM state.
type DriftItem struct {
	Kind   string
	Detail string
}

// ReconcileResult is what `devm reconcile` produces.
type ReconcileResult struct {
	Rendered         bool
	SandboxState     string
	Sandbox          string // project's vm_name, for reporting (e.g. the revive line)
	Applied          []reconcile.Change
	AppliedIronProxy []reconcile.Change // BucketIronProxyRestart changes applied via /vm/apply-iron-proxy
	IronProxyRevived bool               // true when iron-proxy was dead and this reconcile respawned it
	RecreateRequired []reconcile.Change
	Flavor           reconcile.FlavorKind
	Sessions         []Session
	NextAction       string // "applied" | "needs_approval" | "user_refused" | "nothing_to_do"
}

// UseColor gates ANSI escapes emitted by the formatters (currently
// only the "MISMATCH" fingerprint marker). The CLI sets this from
// stdout-is-tty + $NO_COLOR before calling FormatStatusText. Package-
// level rather than a parameter because "should output be colored"
// is a global property of the writing environment, not a per-call
// formatting decision — and it keeps existing test call sites
// untouched.
var UseColor bool

// FormatStatusText renders StatusResult for human terminals. The
// Daemon section renders unconditionally; project-dependent sections
// (sandbox, routing, DNS, CA, proxy) render only when HasProject.
func FormatStatusText(r StatusResult) string {
	var b strings.Builder
	b.WriteString(formatDaemonStatus(r.Daemon))
	if !r.HasProject {
		fmt.Fprintln(&b, "\n(no devm.yaml in cwd — project sections skipped)")
		return b.String()
	}
	fmt.Fprintf(&b, "\nSandbox: %s\n", r.Sandbox)
	fmt.Fprintf(&b, "State:   %s\n", r.State)
	if r.State == "running" {
		fmt.Fprintf(&b, "\nActive sessions (%d):\n", len(r.Sessions))
		for _, s := range r.Sessions {
			fmt.Fprintf(&b, "  %s: %s (PID %d, owner %s)\n", s.TTY, s.Comm, s.PID, s.User)
		}
	}
	fmt.Fprintln(&b)
	switch {
	case r.State == "stopped" || r.State == "absent":
		fmt.Fprintln(&b, "Sandbox stopped; config changes will apply on next `devm shell`.")
	case r.PendingLive == 0 && r.PendingRecreate == 0:
		fmt.Fprintln(&b, "In sync.")
	default:
		fmt.Fprintf(&b, "Pending changes: %d live, %d require recreate\n", r.PendingLive, r.PendingRecreate)
		fmt.Fprintln(&b, "Run `devm reconcile` to apply.")
	}
	for _, d := range r.Drift {
		fmt.Fprintf(&b, "Drift: %s — %s\n", d.Kind, d.Detail)
	}
	b.WriteString(formatRouting(r.Routing))
	b.WriteString(formatDNSHealth(r))
	b.WriteString(formatCAHealth(r))
	b.WriteString(formatProxyHealth(r))
	b.WriteString(formatIronProxyHealth(r))
	return b.String()
}

// formatDaemonStatus renders the daemon section. Always fires — the
// "is the devm daemon happy?" question is meaningful outside a
// project directory too. Honors UseColor for the MISMATCH marker.
func formatDaemonStatus(d DaemonStatus) string {
	var b strings.Builder
	b.WriteString("Daemon:\n")
	switch {
	case d.Running:
		fmt.Fprintln(&b, "  state: running")
	case d.Installed:
		fmt.Fprintln(&b, "  state: installed but not running")
	default:
		fmt.Fprintln(&b, "  state: not installed")
	}
	if d.BinaryPath != "" {
		fmt.Fprintf(&b, "  binary: %s\n", d.BinaryPath)
	}
	if d.Fingerprint != "" {
		if d.FingerprintMatchesCLI {
			fmt.Fprintf(&b, "  fingerprint: %s (matches CLI)\n", d.Fingerprint)
		} else {
			marker := "(MISMATCH — CLI is different; run `devm install`)"
			if UseColor {
				marker = "\x1b[31m" + marker + "\x1b[0m"
			}
			fmt.Fprintf(&b, "  fingerprint: %s %s\n", d.Fingerprint, marker)
		}
	}
	if d.Error != "" {
		fmt.Fprintf(&b, "  error: %s\n", d.Error)
	}
	return b.String()
}

func formatRouting(r serviceapi.RoutingStatus) string {
	var b strings.Builder
	b.WriteString("\nRouting:\n")
	if r.Proxy == "none" {
		b.WriteString("  proxy: none (devm route disabled)\n")
		return b.String()
	}
	if !r.ProxyReachable {
		fmt.Fprintf(&b, "  proxy: %s (unreachable)\n", r.Proxy)
		return b.String()
	}
	fmt.Fprintf(&b, "  proxy:   %s\n", r.Proxy)
	if r.Mode == "" {
		b.WriteString("  mode: (no routes)\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  mode:    %s\n", r.Mode)
	b.WriteString("  routes:\n")
	for _, route := range r.Routes {
		modeTag := ""
		if r.Mode == "mixed (drift)" {
			modeTag = fmt.Sprintf("  (%s)", route.Mode)
		}
		fmt.Fprintf(&b, "    %-25s → %s%s\n",
			route.Hostname, route.Dial, modeTag)
	}
	return b.String()
}

func formatDNSHealth(r StatusResult) string {
	if r.DNSHealthy {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\ndns: NOT WORKING — %s\n", r.DNSError)
	b.WriteString("     Run `devm install` to set up the resolver file, or `devm restart`\n")
	b.WriteString("     if the daemon isn't responding.\n")
	return b.String()
}

func formatCAHealth(r StatusResult) string {
	if r.CATrusted {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nca: NOT TRUSTED\n")
	b.WriteString("     Run `devm install` to install the devm CA into your System Keychain.\n")
	return b.String()
}

func formatProxyHealth(r StatusResult) string {
	if r.ProxyHealthy {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nproxy: NOT LISTENING (port 443) — %s\n", r.ProxyError)
	b.WriteString("       Run `devm install` to register launchd's port binding,\n")
	b.WriteString("       or `devm restart` if the daemon isn't responding.\n")
	return b.String()
}

// formatIronProxyHealth renders the per-project iron-proxy verdict from
// /handshake. Silent when ProxyHealth is nil (daemon unreachable, or
// project mode wasn't queried) — same "don't claim a status we don't
// have" rule as the other health sections.
func formatIronProxyHealth(r StatusResult) string {
	if r.ProxyHealth == nil {
		return ""
	}
	var b strings.Builder
	switch r.ProxyHealth.Status {
	case serviceapi.ProxyOK:
		b.WriteString("\niron-proxy: ok\n")
	case serviceapi.ProxyMissing:
		b.WriteString("\niron-proxy: MISSING (run 'devm reconcile')\n")
	case serviceapi.ProxyStale:
		b.WriteString("\niron-proxy: STALE (run 'devm reconcile')\n")
	default:
		fmt.Fprintf(&b, "\niron-proxy: unknown (%s)\n", r.ProxyHealth.Status)
	}
	return b.String()
}

// green wraps s in an ANSI green escape when UseColor is set.
func green(s string) string {
	if !UseColor {
		return s
	}
	return "\x1b[32m" + s + "\x1b[0m"
}

// red wraps s in an ANSI red escape when UseColor is set. Mirrors the
// inline pattern formatDaemonStatus uses for its MISMATCH marker.
func red(s string) string {
	if !UseColor {
		return s
	}
	return "\x1b[31m" + s + "\x1b[0m"
}

// FormatStatusAllText renders a cross-project status table for `devm
// status --all`: one row per project the daemon has a persisted
// snapshot for, showing VM state, iron-proxy health, and whether
// reconcile is required. The iron-proxy/reconcile columns show "—"
// for stopped VMs — proxy health isn't actionable until the VM is up
// (same reasoning as ExitReconcileRequired only firing for running
// VMs). Honors UseColor: green "ok", red "MISSING"/"STALE".
func FormatStatusAllText(rows []serviceapi.ProjectStatus) string {
	if len(rows) == 0 {
		return "No projects found.\n"
	}

	type line struct {
		project, vm, proxy, reconcile string
		colored                       string // "" | "ok" | "bad"
	}

	lines := make([]line, len(rows))
	widths := [4]int{
		utf8.RuneCountInString("PROJECT"),
		utf8.RuneCountInString("VM"),
		utf8.RuneCountInString("IRON-PROXY"),
		utf8.RuneCountInString("RECONCILE"),
	}
	for i, r := range rows {
		vmState := "stopped"
		if r.VMRunning {
			vmState = "running"
		}
		proxyCol, reconcileCol, colored := "—", "—", ""
		if r.VMRunning {
			switch r.Proxy.Status {
			case serviceapi.ProxyOK:
				proxyCol, colored = "ok", "ok"
			case serviceapi.ProxyMissing:
				proxyCol, reconcileCol, colored = "MISSING", "required", "bad"
			case serviceapi.ProxyStale:
				proxyCol, reconcileCol, colored = "STALE", "required", "bad"
			default:
				proxyCol = string(r.Proxy.Status)
			}
		}
		lines[i] = line{project: r.ProjectID, vm: vmState, proxy: proxyCol, reconcile: reconcileCol, colored: colored}
		widths[0] = max(widths[0], utf8.RuneCountInString(lines[i].project))
		widths[1] = max(widths[1], utf8.RuneCountInString(lines[i].vm))
		widths[2] = max(widths[2], utf8.RuneCountInString(lines[i].proxy))
		widths[3] = max(widths[3], utf8.RuneCountInString(lines[i].reconcile))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %-*s\n",
		widths[0], "PROJECT", widths[1], "VM", widths[2], "IRON-PROXY", widths[3], "RECONCILE")
	for _, l := range lines {
		proxy := l.proxy
		switch l.colored {
		case "ok":
			proxy = green(l.proxy)
		case "bad":
			proxy = red(l.proxy)
		}
		pad := widths[2] - utf8.RuneCountInString(l.proxy)
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintf(&b, "%-*s  %-*s  %s%s  %-*s\n",
			widths[0], l.project, widths[1], l.vm, proxy, strings.Repeat(" ", pad), widths[3], l.reconcile)
	}
	return b.String()
}

// FormatStatusAllJSON renders the []ProjectStatus from GET /status/all
// as JSON — the CLI's `devm status --all --json` output.
func FormatStatusAllJSON(rows []serviceapi.ProjectStatus) string {
	if rows == nil {
		rows = []serviceapi.ProjectStatus{}
	}
	out, _ := json.MarshalIndent(rows, "", "  ")
	return string(out)
}

// FormatReconcileText renders ReconcileResult for human terminals.
func FormatReconcileText(r ReconcileResult) string {
	var b strings.Builder
	if len(r.Applied) > 0 {
		fmt.Fprintf(&b, "Applied %d live change(s):\n", len(r.Applied))
		for _, c := range r.Applied {
			fmt.Fprintln(&b, "  "+formatChange(c))
		}
		fmt.Fprintln(&b)
	}
	if len(r.AppliedIronProxy) > 0 {
		verb := "Applied"
		if r.SandboxState == "stopped" || r.SandboxState == "absent" {
			verb = "Recorded"
		}
		fmt.Fprintf(&b, "%s %d network egress change(s):\n", verb, len(r.AppliedIronProxy))
		for _, c := range r.AppliedIronProxy {
			fmt.Fprintln(&b, "  "+formatIronProxyChange(c))
		}
		if r.IronProxyRevived && r.Sandbox != "" {
			fmt.Fprintf(&b, "\niron-proxy for %s was not running — respawned with new config\n", r.Sandbox)
		}
		fmt.Fprintln(&b)
	}
	if len(r.RecreateRequired) > 0 {
		fmt.Fprintf(&b, "%d change(s) require recreate (%s):\n", len(r.RecreateRequired), r.Flavor)
		for _, c := range r.RecreateRequired {
			fmt.Fprintln(&b, "  "+formatChange(c))
		}
		fmt.Fprintln(&b)
		switch r.Flavor {
		case reconcile.FlavorTeardownShell:
			fmt.Fprintln(&b, "Teardown + recreate sandbox? This WIPES installed packages and volume data,")
			fmt.Fprintln(&b, "then re-runs install.")
		case reconcile.FlavorStopShell:
			fmt.Fprintln(&b, "Restart sandbox to apply env/startup/network changes?")
		}
		if len(r.Sessions) > 0 {
			fmt.Fprintf(&b, "Will hang up %d active session(s).\n", len(r.Sessions))
		}
	}
	return b.String()
}

// FormatStatusJSON renders StatusResult as JSON.
func FormatStatusJSON(r StatusResult) string {
	type pending struct {
		Live     int `json:"live"`
		Recreate int `json:"recreate"`
	}
	type sess struct {
		PID  int    `json:"pid"`
		TTY  string `json:"tty"`
		Comm string `json:"comm"`
		User string `json:"user"`
	}
	type drift struct {
		Kind   string `json:"kind"`
		Detail string `json:"detail"`
	}
	type daemon struct {
		Running               bool   `json:"running"`
		Installed             bool   `json:"installed"`
		BinaryPath            string `json:"binary_path,omitempty"`
		Fingerprint           string `json:"fingerprint,omitempty"`
		FingerprintMatchesCLI bool   `json:"fingerprint_matches_cli"`
		Error                 string `json:"error,omitempty"`
	}
	// health carries the global invariants `devm install` sets up:
	// the resolver file at /etc/resolver/test, the CA in the System
	// Keychain, launchd's :80/:443 socket handoff to the reverse
	// proxy. All are single-installation state — none are per-project.
	type health struct {
		DNSHealthy   bool   `json:"dns_healthy"`
		DNSError     string `json:"dns_error,omitempty"`
		CATrusted    bool   `json:"ca_trusted"`
		ProxyHealthy bool   `json:"proxy_healthy"`
		ProxyError   string `json:"proxy_error,omitempty"`
	}
	type ironProxy struct {
		Status       string `json:"status"`
		NeedsSecrets bool   `json:"needs_secrets"`
	}
	type project struct {
		Sandbox        string                   `json:"sandbox"`
		State          string                   `json:"state"`
		Sessions       []sess                   `json:"sessions"`
		PendingChanges pending                  `json:"pending_changes"`
		Drift          []drift                  `json:"drift"`
		Routing        serviceapi.RoutingStatus `json:"routing"`
		IronProxy      *ironProxy               `json:"iron_proxy,omitempty"`
	}
	type body struct {
		Daemon  daemon   `json:"daemon"`
		Health  health   `json:"health"`
		Project *project `json:"project,omitempty"`
	}
	sessions := make([]sess, len(r.Sessions))
	for i, s := range r.Sessions {
		sessions[i] = sess{PID: s.PID, TTY: s.TTY, Comm: s.Comm, User: s.User}
	}
	drifts := make([]drift, len(r.Drift))
	for i, d := range r.Drift {
		drifts[i] = drift{Kind: d.Kind, Detail: d.Detail}
	}
	b := body{
		Daemon: daemon{
			Running:               r.Daemon.Running,
			Installed:             r.Daemon.Installed,
			BinaryPath:            r.Daemon.BinaryPath,
			Fingerprint:           r.Daemon.Fingerprint,
			FingerprintMatchesCLI: r.Daemon.FingerprintMatchesCLI,
			Error:                 r.Daemon.Error,
		},
		Health: health{
			DNSHealthy:   r.DNSHealthy,
			DNSError:     r.DNSError,
			CATrusted:    r.CATrusted,
			ProxyHealthy: r.ProxyHealthy,
			ProxyError:   r.ProxyError,
		},
	}
	if r.HasProject {
		if sessions == nil {
			sessions = []sess{}
		}
		if drifts == nil {
			drifts = []drift{}
		}
		b.Project = &project{
			Sandbox:        r.Sandbox,
			State:          r.State,
			Sessions:       sessions,
			PendingChanges: pending{Live: r.PendingLive, Recreate: r.PendingRecreate},
			Drift:          drifts,
			Routing:        r.Routing,
		}
		if r.ProxyHealth != nil {
			b.Project.IronProxy = &ironProxy{
				Status:       string(r.ProxyHealth.Status),
				NeedsSecrets: r.ProxyHealth.NeedsSecrets,
			}
		}
	}
	out, _ := json.MarshalIndent(b, "", "  ")
	return string(out)
}

// FormatReconcileJSON renders ReconcileResult as JSON.
func FormatReconcileJSON(r ReconcileResult) string {
	type changeJSON struct {
		Kind    string `json:"kind"`
		Service string `json:"service,omitempty"`
		Key     string `json:"key,omitempty"`
		Old     string `json:"old,omitempty"`
		New     string `json:"new,omitempty"`
	}
	type sess struct {
		PID  int    `json:"pid"`
		TTY  string `json:"tty"`
		Comm string `json:"comm"`
		User string `json:"user"`
	}
	type recreate struct {
		Flavor   string       `json:"flavor"`
		Changes  []changeJSON `json:"changes"`
		Sessions []sess       `json:"sessions"`
	}
	type body struct {
		Rendered         bool         `json:"rendered"`
		SandboxState     string       `json:"sandbox_state"`
		Applied          []changeJSON `json:"applied"`
		AppliedIronProxy []changeJSON `json:"applied_iron_proxy,omitempty"`
		IronProxyRevived bool         `json:"iron_proxy_revived,omitempty"`
		RecreateRequired *recreate    `json:"recreate_required,omitempty"`
		NextAction       string       `json:"next_action"`
	}

	toJSON := func(c reconcile.Change) changeJSON {
		return changeJSON{
			Kind: changeKindJSON(c.Kind), Service: c.Service, Key: c.Key,
			Old: c.Old, New: c.New,
		}
	}

	applied := make([]changeJSON, len(r.Applied))
	for i, c := range r.Applied {
		applied[i] = toJSON(c)
	}

	ipRestart := make([]changeJSON, len(r.AppliedIronProxy))
	for i, c := range r.AppliedIronProxy {
		ipRestart[i] = toJSON(c)
	}

	out := body{
		Rendered: r.Rendered, SandboxState: r.SandboxState,
		Applied: applied, AppliedIronProxy: ipRestart,
		IronProxyRevived: r.IronProxyRevived,
		NextAction:       r.NextAction,
	}

	if len(r.RecreateRequired) > 0 {
		changes := make([]changeJSON, len(r.RecreateRequired))
		for i, c := range r.RecreateRequired {
			changes[i] = toJSON(c)
		}
		sessions := make([]sess, len(r.Sessions))
		for i, s := range r.Sessions {
			sessions[i] = sess{PID: s.PID, TTY: s.TTY, Comm: s.Comm, User: s.User}
		}
		out.RecreateRequired = &recreate{
			Flavor:   flavorJSON(r.Flavor),
			Changes:  changes,
			Sessions: sessions,
		}
	}

	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b)
}

func changeKindJSON(k reconcile.ChangeKind) string {
	switch k {
	case reconcile.KindPortAdd:
		return "port_add"
	case reconcile.KindPortRemove:
		return "port_remove"
	case reconcile.KindPortChange:
		return "port_change"
	case reconcile.KindNetworkAdd:
		return "network_add"
	case reconcile.KindNetworkRemove:
		return "network_remove"
	case reconcile.KindEnvAdd:
		return "env_add"
	case reconcile.KindEnvRemove:
		return "env_remove"
	case reconcile.KindEnvChange:
		return "env_change"
	case reconcile.KindInstallChange:
		return "install_change"
	case reconcile.KindPackagesChange:
		return "packages_change"
	case reconcile.KindMaskAddRemove:
		return "mask_add_remove"
	case reconcile.KindImageChange:
		return "image_change"
	case reconcile.KindIdentityChange:
		return "identity_change"
	case reconcile.KindDockerToggle:
		return "docker_toggle"
	case reconcile.KindDiskChange:
		return "disk_change"
	case reconcile.KindTemplateChange:
		return "template_change"
	case reconcile.KindMountAddRemove:
		return "mount_add_remove"
	case reconcile.KindServiceExecChange:
		return "service_exec_change"
	case reconcile.KindServiceRestartChange:
		return "service_restart_change"
	case reconcile.KindServiceAfterChange:
		return "service_after_change"
	case reconcile.KindServiceWorkdirChange:
		return "service_workdir_change"
	case reconcile.KindServiceUserChange:
		return "service_user_change"
	case reconcile.KindServiceSystemdOverrideChange:
		return "service_systemd_override_change"
	case reconcile.KindServiceHostnameChange:
		return "service_hostname_change"
	case reconcile.KindSecretAdd:
		return "secret_add"
	case reconcile.KindSecretRemove:
		return "secret_remove"
	case reconcile.KindSecretChange:
		return "secret_change"
	case reconcile.KindIronProxyDown:
		return "iron_proxy_down"
	}
	return "unknown"
}

func flavorJSON(f reconcile.FlavorKind) string {
	switch f {
	case reconcile.FlavorLiveOnly:
		return "live"
	case reconcile.FlavorStopShell:
		return "stop_shell"
	case reconcile.FlavorTeardownShell:
		return "teardown_shell"
	}
	return "unknown"
}

// formatChange returns a one-line, human-readable description of a Change.
func formatChange(c reconcile.Change) string {
	switch c.Kind {
	case reconcile.KindPortAdd:
		return fmt.Sprintf("+ port %s (%s)", c.New, c.Service)
	case reconcile.KindPortRemove:
		return fmt.Sprintf("- port %s (%s)", c.Old, c.Service)
	case reconcile.KindPortChange:
		return fmt.Sprintf("~ port %s: %s → %s", c.Service, c.Old, c.New)
	case reconcile.KindNetworkAdd:
		return fmt.Sprintf("+ allow network %s", c.New)
	case reconcile.KindNetworkRemove:
		return fmt.Sprintf("- allow network %s", c.Old)
	case reconcile.KindEnvAdd:
		return fmt.Sprintf("+ env: %s.%s = %q", c.Service, c.Key, c.New)
	case reconcile.KindEnvRemove:
		return fmt.Sprintf("- env: %s.%s", c.Service, c.Key)
	case reconcile.KindEnvChange:
		return fmt.Sprintf("~ env: %s.%s: %q → %q", c.Service, c.Key, c.Old, c.New)
	case reconcile.KindInstallChange:
		return "~ install commands"
	case reconcile.KindPackagesChange:
		return "~ packages"
	case reconcile.KindMountAddRemove:
		return "~ mounts"
	case reconcile.KindMaskAddRemove:
		return fmt.Sprintf("~ volumes: %s", c.Service)
	case reconcile.KindServiceExecChange:
		return fmt.Sprintf("~ service exec: %s", c.Service)
	case reconcile.KindServiceRestartChange:
		return fmt.Sprintf("~ service restart: %s", c.Service)
	case reconcile.KindServiceAfterChange:
		return fmt.Sprintf("~ service after: %s", c.Service)
	case reconcile.KindServiceWorkdirChange:
		return fmt.Sprintf("~ service workdir: %s", c.Service)
	case reconcile.KindServiceUserChange:
		return fmt.Sprintf("~ service user: %s", c.Service)
	case reconcile.KindServiceSystemdOverrideChange:
		return fmt.Sprintf("~ service systemd override: %s", c.Service)
	case reconcile.KindServiceHostnameChange:
		return fmt.Sprintf("~ service hostname: %s: %q → %q", c.Service, c.Old, c.New)
	case reconcile.KindImageChange:
		return "~ base image"
	case reconcile.KindIdentityChange:
		return "~ project identity"
	case reconcile.KindDockerToggle:
		return "~ docker"
	case reconcile.KindDiskChange:
		return fmt.Sprintf("~ disk: %s → %s", c.Old, c.New)
	case reconcile.KindTemplateChange:
		switch {
		case c.Old == "" && c.New != "":
			return fmt.Sprintf("+ template: %s → %s", c.Service, c.Detail)
		case c.Old != "" && c.New == "":
			return fmt.Sprintf("- template: %s (sandbox file persists; recreate to wipe)", c.Detail)
		default:
			return fmt.Sprintf("~ template: %s → %s", c.Service, c.Detail)
		}
	case reconcile.KindIronProxyDown:
		return "~ iron-proxy: restoring (missing/stale)"
	}
	return "(unknown change)"
}

// formatIronProxyChange renders a KindNetwork* or KindSecret* change
// under the "network egress" section header. Simpler than
// formatChange's long switch: only five kinds live in this bucket.
func formatIronProxyChange(c reconcile.Change) string {
	switch c.Kind {
	case reconcile.KindNetworkAdd:
		return fmt.Sprintf("+ network.allow: %s", c.Key)
	case reconcile.KindNetworkRemove:
		return fmt.Sprintf("- network.allow: %s", c.Key)
	case reconcile.KindSecretAdd:
		return fmt.Sprintf("+ secret: %s", c.Key)
	case reconcile.KindSecretRemove:
		return fmt.Sprintf("- secret: %s", c.Key)
	case reconcile.KindSecretChange:
		return fmt.Sprintf("~ secret rotated: %s", c.Key)
	case reconcile.KindIronProxyDown:
		return "~ iron-proxy: restoring (missing/stale)"
	}
	return "(unknown iron-proxy change)"
}
