package orchestrator

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
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

	// Egress is the project's current egress policy state (restricted
	// vs. passthrough). Populated only when the VM is running; nil
	// otherwise. Rendered under the Routing section — a highlighted
	// row when passthrough is active, silent when restricted.
	Egress *serviceapi.EgressStatus

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

	// ApproveState is the daemon's approve-gate verdict for the project
	// (GET /vm/approve-state): whether devm.yaml/devm.me.yaml have
	// diverged from the last-approved snapshot. Nil when the daemon
	// predates the approve gate (404) — the format layer silently
	// omits the approve-gate line for backward compatibility.
	ApproveState *serviceapi.ApproveStateResponse

	// ApproveError carries a non-404 failure from the approve-state
	// probe (network error, malformed response, etc.). The approve
	// gate is informational only, so `devm status` never fails because
	// of it — this just lets the format layer report the failure.
	ApproveError string

	// PopSessions is the daemon's pop-session count + oldest age for
	// the project (GET /pop-session-summary). Nil means no data (old
	// daemon predating this endpoint, or the probe was unreachable) —
	// the format layer omits the line entirely rather than claiming a
	// count it doesn't have. A non-nil zero-count value also renders
	// nothing.
	PopSessions *PopSessionSummary
}

// PopSessionSummary is the pop-session count + oldest session age for
// a project, as reported by GET /pop-session-summary.
type PopSessionSummary struct {
	Count     int
	OldestAge time.Duration
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
	Sandbox          string // project's name, for reporting (e.g. the revive line)
	Applied          []reconcile.Change
	AppliedIronProxy []reconcile.Change // BucketEgressRestart changes applied via /vm/apply-iron-proxy
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
		fmt.Fprintln(&b, "Sandbox stopped; config changes will apply on next `devm start`.")
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
	b.WriteString(formatEgress(r.Egress))
	b.WriteString(formatDNSHealth(r))
	b.WriteString(formatCAHealth(r))
	b.WriteString(formatProxyHealth(r))
	b.WriteString(formatIronProxyHealth(r))
	b.WriteString(formatLANListener(r.Routing))
	b.WriteString(formatApproveState(r))
	b.WriteString(formatPopSessions(r))
	return b.String()
}

// formatApproveState renders the approve-gate divergence line. Never
// blocks `devm status`: silent when ApproveState is nil and there was
// no error (old daemon, 404 — backward compat), otherwise reports the
// daemon's verdict or, failing that, that the check itself failed.
func formatApproveState(r StatusResult) string {
	switch {
	case r.ApproveState != nil && r.ApproveState.Diverged:
		return "\nApprove gate: devm.yaml has changed since last approval — Review with `devm approve` or the menu bar.\n"
	case r.ApproveState != nil:
		return "\nApprove gate: up to date.\n"
	case r.ApproveError != "":
		return fmt.Sprintf("\nApprove gate: check failed: %s\n", r.ApproveError)
	default:
		return ""
	}
}

// formatPopSessions renders the pop-session count + oldest age line.
// Silent when PopSessions is nil (old daemon, or the probe was
// unreachable) or the count is zero — a project with no open pop
// sessions has nothing worth reporting.
func formatPopSessions(r StatusResult) string {
	if r.PopSessions == nil || r.PopSessions.Count == 0 {
		return ""
	}
	return fmt.Sprintf("\nPop sessions: %d active (oldest: %s)\n",
		r.PopSessions.Count, r.PopSessions.OldestAge.Round(time.Second))
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

// formatEgress renders the egress policy line under Routing. Silent
// under the default "restricted" state (a per-project baseline is
// implied). Highlighted (uppercase, with a countdown) under
// "passthrough" to make the user aware the window is open. Silent
// entirely when eg is nil (VM not running, or daemon didn't return
// the field).
func formatEgress(eg *serviceapi.EgressStatus) string {
	if eg == nil {
		return ""
	}
	if eg.Policy != "passthrough" || eg.PassthroughExpiresAt == nil {
		return "  egress:  RESTRICTED\n"
	}
	remaining := time.Until(*eg.PassthroughExpiresAt).Round(time.Second)
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Sprintf("  egress:  PASSTHROUGH — auto-restores in %s\n", remaining)
}

func formatRouting(r serviceapi.RoutingStatus) string {
	var b strings.Builder
	b.WriteString("\nRouting:\n")
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
	if r.ProxyHealth.Rebind != nil && r.ProxyHealth.Rebind.State == serviceapi.RebindFailed {
		fmt.Fprintf(&b, "\nproxy listeners: UNBOUND — %s\n",
			r.ProxyHealth.Rebind.LastError)
		b.WriteString("  Recovery: `devm stop && devm start` (from the project directory)\n")
	}
	return b.String()
}

// formatLANListener renders the shared LAN dispatcher's bind state —
// daemon-scope, not per-project (see internal/serviceapi/lan.go): the
// listener binds once, for as long as any project has at least one
// ExposeHost route, so this is a single top-level row rather than one
// per project/route. r.Routing.LANExposedCount is a count across all
// projects (computed client-side in RoutingStatusFromDaemon from the
// /routes response), and reconcileLAN's own invariant — bound iff that
// count is > 0 — is what lets this render "bound"/"not bound" without
// a dedicated daemon endpoint.
func formatLANListener(r serviceapi.RoutingStatus) string {
	if r.LANExposedCount == 0 {
		return "\nLAN listener: not bound\n"
	}
	return fmt.Sprintf("\nLAN listener: bound on 0.0.0.0:%d (%d hostnames exposed)\n",
		serviceapi.LANDispatchPort, r.LANExposedCount)
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
// VMs). IRON-PROXY always reflects the iron-proxy subsystem's own
// verdict (r.Proxy.Status) — a stuck startup rebind is a *different*
// subsystem (the daemon's ProxyServer listeners on :80/:443) and is
// never folded into that column, so a project with healthy iron-proxy
// but a failed rebind doesn't misleadingly read as iron-proxy being
// broken. Instead, failed rebinds are called out in a footer block
// below the table (mirroring formatIronProxyHealth's single-project
// "proxy listeners: UNBOUND" line), since the per-project error text
// and recovery command don't fit in a terse column value. Honors
// UseColor: green "ok", red "MISSING"/"STALE" and the footer note.
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
	orphaned := false
	for i, r := range rows {
		vmState := "stopped"
		if r.VMRunning {
			vmState = "running"
		}
		proxyCol, reconcileCol, colored := "—", "—", ""
		if r.Orphaned {
			// A running devm VM the daemon has no state for: no proxy
			// verdict is computable, and reconcile can't reach it.
			orphaned = true
			lines[i] = line{project: r.Name, vm: "ORPHANED", proxy: "—", reconcile: "—"}
			widths[0] = max(widths[0], utf8.RuneCountInString(r.Name))
			widths[1] = max(widths[1], utf8.RuneCountInString("ORPHANED"))
			continue
		}
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
		lines[i] = line{project: r.Name, vm: vmState, proxy: proxyCol, reconcile: reconcileCol, colored: colored}
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

	// Failed rebinds are a distinct subsystem from iron-proxy (see doc
	// comment above) — call them out per-project below the table
	// rather than overloading IRON-PROXY, mirroring the single-project
	// "proxy listeners: UNBOUND" line formatIronProxyHealth emits.
	notePrinted := false
	for _, r := range rows {
		if !r.VMRunning || r.Proxy.Rebind == nil || r.Proxy.Rebind.State != serviceapi.RebindFailed {
			continue
		}
		if !notePrinted {
			fmt.Fprintln(&b)
			notePrinted = true
		}
		note := fmt.Sprintf("Note: project %s has proxy listeners UNBOUND — %s — Recovery: cd to project %s's directory, then `devm stop && devm start`",
			r.Name, r.Proxy.Rebind.LastError, r.Name)
		fmt.Fprintln(&b, red(note))
	}

	if orphaned {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, red("Note: ORPHANED = running VM with devm artifacts but no daemon state; its softnet may be squatting pool IP binds. Recovery: `devm stop` (or `devm teardown`) from that project's directory."))
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
		restart, teardown := partitionRecreateRequired(r.RecreateRequired)
		// Both sections can share one pending recreate — print the
		// session-hangup warning once for the whole reconcile rather
		// than once per section.
		hangupPrinted := false
		if len(restart) > 0 {
			fmt.Fprintf(&b, "%d change(s) require restart:\n", len(restart))
			for _, c := range restart {
				fmt.Fprintln(&b, "  "+formatChange(c))
			}
			fmt.Fprintln(&b)
			fmt.Fprintln(&b, "Restart sandbox (`devm stop` + `devm start`) to apply. No teardown, no data loss.")
			if len(r.Sessions) > 0 && !hangupPrinted {
				fmt.Fprintf(&b, "Will hang up %d active session(s).\n", len(r.Sessions))
				hangupPrinted = true
			}
			fmt.Fprintln(&b)
		}
		if len(teardown) > 0 {
			fmt.Fprintf(&b, "%d change(s) require recreate:\n", len(teardown))
			for _, c := range teardown {
				fmt.Fprintln(&b, "  "+formatChange(c))
			}
			fmt.Fprintln(&b)
			fmt.Fprintln(&b, "Teardown + recreate sandbox? This WIPES installed packages and volume data,")
			fmt.Fprintln(&b, "then re-runs install.")
			if len(r.Sessions) > 0 && !hangupPrinted {
				fmt.Fprintf(&b, "Will hang up %d active session(s).\n", len(r.Sessions))
				hangupPrinted = true
			}
		}
	}
	return b.String()
}

// partitionRecreateRequired splits a RecreateRequired list into the
// restart-bucket (BucketRestartVM — VM stop + cold start, no teardown)
// and everything else (BucketTeardownVM — full delete + cold start),
// so the text/JSON formatters can surface "restart" as its own
// category distinct from "recreate", matching each change's real
// severity instead of collapsing them under a single flavor.
func partitionRecreateRequired(changes []reconcile.Change) (restart, teardown []reconcile.Change) {
	for _, c := range changes {
		if c.Bucket() == reconcile.BucketRestartVM {
			restart = append(restart, c)
		} else {
			teardown = append(teardown, c)
		}
	}
	return restart, teardown
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
		Status       string                   `json:"status"`
		NeedsSecrets bool                     `json:"needs_secrets"`
		Rebind       *serviceapi.RebindReport `json:"rebind,omitempty"`
	}
	type approveState struct {
		Diverged      bool    `json:"diverged"`
		ApprovedSince *string `json:"approved_since"`
	}
	type popSessions struct {
		Count            int   `json:"count"`
		OldestAgeSeconds int64 `json:"oldest_age_seconds"`
	}
	type project struct {
		Sandbox        string                   `json:"sandbox"`
		State          string                   `json:"state"`
		Sessions       []sess                   `json:"sessions"`
		PendingChanges pending                  `json:"pending_changes"`
		Drift          []drift                  `json:"drift"`
		Routing        serviceapi.RoutingStatus `json:"routing"`
		Egress         *serviceapi.EgressStatus `json:"egress,omitempty"`
		IronProxy      *ironProxy               `json:"iron_proxy,omitempty"`
		ApproveState   *approveState            `json:"approve_state"`
		PopSessions    *popSessions             `json:"pop_sessions"`
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
			Egress:         r.Egress,
		}
		if r.ProxyHealth != nil {
			b.Project.IronProxy = &ironProxy{
				Status:       string(r.ProxyHealth.Status),
				NeedsSecrets: r.ProxyHealth.NeedsSecrets,
				Rebind:       r.ProxyHealth.Rebind,
			}
		}
		if r.ApproveState != nil {
			b.Project.ApproveState = &approveState{
				Diverged:      r.ApproveState.Diverged,
				ApprovedSince: r.ApproveState.ApprovedSince,
			}
		}
		if r.PopSessions != nil && r.PopSessions.Count > 0 {
			b.Project.PopSessions = &popSessions{
				Count:            r.PopSessions.Count,
				OldestAgeSeconds: int64(r.PopSessions.OldestAge.Round(time.Second).Seconds()),
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
	// changeSet is the shape of both the restart_required and
	// recreate_required blocks — same fields, different membership
	// (partitionRecreateRequired splits by bucket).
	type changeSet struct {
		Changes  []changeJSON `json:"changes"`
		Sessions []sess       `json:"sessions"`
	}
	type body struct {
		Rendered         bool         `json:"rendered"`
		SandboxState     string       `json:"sandbox_state"`
		Applied          []changeJSON `json:"applied"`
		AppliedIronProxy []changeJSON `json:"applied_iron_proxy,omitempty"`
		IronProxyRevived bool         `json:"iron_proxy_revived,omitempty"`
		RestartRequired  *changeSet   `json:"restart_required,omitempty"`
		RecreateRequired *changeSet   `json:"recreate_required,omitempty"`
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
		restart, teardown := partitionRecreateRequired(r.RecreateRequired)
		sessions := make([]sess, len(r.Sessions))
		for i, s := range r.Sessions {
			sessions[i] = sess{PID: s.PID, TTY: s.TTY, Comm: s.Comm, User: s.User}
		}
		if len(restart) > 0 {
			changes := make([]changeJSON, len(restart))
			for i, c := range restart {
				changes[i] = toJSON(c)
			}
			out.RestartRequired = &changeSet{Changes: changes, Sessions: sessions}
		}
		if len(teardown) > 0 {
			changes := make([]changeJSON, len(teardown))
			for i, c := range teardown {
				changes[i] = toJSON(c)
			}
			out.RecreateRequired = &changeSet{Changes: changes, Sessions: sessions}
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
	case reconcile.KindStartupChange:
		return "startup_change"
	case reconcile.KindPackageAdd:
		return "package_add"
	case reconcile.KindPackageRemove:
		return "package_remove"
	case reconcile.KindImageChange:
		return "image_change"
	case reconcile.KindIdentityChange:
		return "identity_change"
	case reconcile.KindDockerToggle:
		return "docker_toggle"
	case reconcile.KindDiskChange:
		return "disk_change"
	case reconcile.KindMemoryChange:
		return "memory_change"
	case reconcile.KindCpuChange:
		return "cpu_change"
	case reconcile.KindTemplateChange:
		return "template_change"
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
	case reconcile.KindServiceDirectChange:
		return "service_direct_change"
	case reconcile.KindSecretAdd:
		return "secret_add"
	case reconcile.KindSecretRemove:
		return "secret_remove"
	case reconcile.KindSecretChange:
		return "secret_change"
	case reconcile.KindIronProxyDown:
		return "iron_proxy_down"
	case reconcile.KindVolumeChange:
		return "volume_change"
	case reconcile.KindRepoChange:
		return "repo_change"
	case reconcile.KindSSHEndpointHealed:
		return "ssh_endpoint_healed"
	case reconcile.KindCommandsChange:
		return "commands_change"
	}
	return "unknown"
}

// formatChange returns a one-line, human-readable description of a Change.
func formatChange(c reconcile.Change) string {
	switch c.Kind {
	case reconcile.KindSSHEndpointHealed:
		return fmt.Sprintf("~ ssh endpoint healed: %s:22 was answered by a foreign host key, project moved to %s", c.Old, c.New)
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
	case reconcile.KindStartupChange:
		return "~ startup commands"
	case reconcile.KindPackageAdd:
		return fmt.Sprintf("+ package %s", c.Key)
	case reconcile.KindPackageRemove:
		return fmt.Sprintf("- package %s", c.Key)
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
	case reconcile.KindServiceDirectChange:
		state := "off"
		if c.New == "true" {
			state = "on"
		}
		return fmt.Sprintf("~ service direct: %s: %s", c.Service, state)
	case reconcile.KindImageChange:
		return "~ base image"
	case reconcile.KindIdentityChange:
		return "~ project identity"
	case reconcile.KindDockerToggle:
		return "~ docker"
	case reconcile.KindDiskChange:
		return fmt.Sprintf("~ disk: %s → %s", c.Old, c.New)
	case reconcile.KindMemoryChange:
		return fmt.Sprintf("~ memory: %s → %s", c.Old, c.New)
	case reconcile.KindCpuChange:
		return fmt.Sprintf("~ cpu: %s → %s", c.Old, c.New)
	case reconcile.KindTemplateChange:
		switch {
		case c.Old == "" && c.New != "":
			return fmt.Sprintf("+ template: %s → %s", c.Service, c.Detail)
		case c.Old != "" && c.New == "":
			return fmt.Sprintf("- template: %s (sandbox file persists; recreate to wipe)", c.Detail)
		default:
			return fmt.Sprintf("~ template: %s → %s", c.Service, c.Detail)
		}
	case reconcile.KindVolumeChange:
		switch c.Op {
		case reconcile.OpAdd:
			return fmt.Sprintf("+ volume %s at %s", c.Key, c.New)
		case reconcile.OpRemove:
			return fmt.Sprintf("- volume %s", c.Key)
		default:
			return fmt.Sprintf("~ volume %s.%s: %s → %s", c.Key, c.Field,
				formatChangeValue(c.OldValue), formatChangeValue(c.NewValue))
		}
	case reconcile.KindRepoChange:
		switch c.Op {
		case reconcile.OpAdd:
			return fmt.Sprintf("+ repo %s", c.Key)
		case reconcile.OpRemove:
			return fmt.Sprintf("- repo %s", c.Key)
		default:
			return fmt.Sprintf("~ repo %s.%s: %s → %s", c.Key, c.Field,
				formatChangeValue(c.OldValue), formatChangeValue(c.NewValue))
		}
	case reconcile.KindCommandsChange:
		switch c.Op {
		case reconcile.OpAdd:
			return fmt.Sprintf("+ command %s/%s", c.Repo, c.Key)
		case reconcile.OpRemove:
			return fmt.Sprintf("- command %s/%s", c.Repo, c.Key)
		default:
			return fmt.Sprintf("~ command %s/%s.%s: %s → %s", c.Repo, c.Key, c.Field,
				formatChangeValue(c.OldValue), formatChangeValue(c.NewValue))
		}
	}
	return "(unknown change)"
}

// maxChangeValueDisplayLen caps how much of a long field value
// (typically a repo URL) formatChange prints inline before eliding it.
const maxChangeValueDisplayLen = 60

// formatChangeValue renders a KindRepoChange/KindVolumeChange field's
// typed Old/NewValue for display: nil pointers as "(unset)", empty
// strings/slices as `""`, and anything past maxChangeValueDisplayLen
// truncated with a trailing ellipsis so a long URL doesn't blow out
// the reconcile summary line.
func formatChangeValue(v any) string {
	var s string
	switch t := v.(type) {
	case nil:
		return "(unset)"
	case *string:
		if t == nil {
			return "(unset)"
		}
		s = *t
	case *bool:
		if t == nil {
			return "(unset)"
		}
		s = strconv.FormatBool(*t)
	case string:
		s = t
	case []string:
		s = strings.Join(t, ",")
	default:
		s = fmt.Sprintf("%v", t)
	}
	if len(s) > maxChangeValueDisplayLen {
		return fmt.Sprintf("%q…", s[:maxChangeValueDisplayLen])
	}
	return fmt.Sprintf("%q", s)
}

// formatIronProxyChange renders a KindNetwork* or KindSecret* change
// under the "network egress" section header. Simpler than
// formatChange's long switch: just the kinds that live in this bucket.
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
