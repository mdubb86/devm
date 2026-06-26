package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdubb86/devm/internal/sandbox"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// StatusResult is what `devm status` produces.
type StatusResult struct {
	Sandbox         string
	State           string // "running" | "stopped" | "absent"
	Sessions        []sandbox.Session
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
	Applied          []Change
	RecreateRequired []Change
	Flavor           FlavorKind
	Sessions         []sandbox.Session
	NextAction       string // "applied" | "needs_approval" | "user_refused" | "nothing_to_do"
}

func (f FlavorKind) String() string {
	switch f {
	case FlavorLiveOnly:
		return "live"
	case FlavorStopShell:
		return "stop+shell"
	case FlavorTeardownShell:
		return "teardown+shell"
	}
	return "unknown"
}

// FormatStatusText renders StatusResult for human terminals.
func FormatStatusText(r StatusResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sandbox: %s\n", r.Sandbox)
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
	if len(r.RecreateRequired) > 0 {
		fmt.Fprintf(&b, "%d change(s) require recreate (%s):\n", len(r.RecreateRequired), r.Flavor)
		for _, c := range r.RecreateRequired {
			fmt.Fprintln(&b, "  "+formatChange(c))
		}
		fmt.Fprintln(&b)
		switch r.Flavor {
		case FlavorTeardownShell:
			fmt.Fprintln(&b, "Teardown + recreate sandbox? This WIPES installed packages and volume data,")
			fmt.Fprintln(&b, "then re-runs install.")
		case FlavorStopShell:
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
	type body struct {
		Sandbox        string                   `json:"sandbox"`
		State          string                   `json:"state"`
		Sessions       []sess                   `json:"sessions"`
		PendingChanges pending                  `json:"pending_changes"`
		Drift          []drift                  `json:"drift"`
		Routing        serviceapi.RoutingStatus `json:"routing"`
		DNSHealthy     bool                     `json:"dns_healthy"`
		DNSError       string                   `json:"dns_error,omitempty"`
		CATrusted      bool                     `json:"ca_trusted"`
		ProxyHealthy   bool                     `json:"proxy_healthy"`
		ProxyError     string                   `json:"proxy_error,omitempty"`
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
		Sandbox: r.Sandbox, State: r.State, Sessions: sessions,
		PendingChanges: pending{Live: r.PendingLive, Recreate: r.PendingRecreate},
		Drift:          drifts,
		Routing:        r.Routing,
		DNSHealthy:     r.DNSHealthy,
		DNSError:       r.DNSError,
		CATrusted:      r.CATrusted,
		ProxyHealthy:   r.ProxyHealthy,
		ProxyError:     r.ProxyError,
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
		RecreateRequired *recreate    `json:"recreate_required,omitempty"`
		NextAction       string       `json:"next_action"`
	}

	toJSON := func(c Change) changeJSON {
		return changeJSON{
			Kind: changeKindJSON(c.Kind), Service: c.Service, Key: c.Key,
			Old: c.Old, New: c.New,
		}
	}

	applied := make([]changeJSON, len(r.Applied))
	for i, c := range r.Applied {
		applied[i] = toJSON(c)
	}

	out := body{Rendered: r.Rendered, SandboxState: r.SandboxState, Applied: applied, NextAction: r.NextAction}

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

func changeKindJSON(k ChangeKind) string {
	switch k {
	case KindPortAdd:
		return "port_add"
	case KindPortRemove:
		return "port_remove"
	case KindPortChange:
		return "port_change"
	case KindNetworkAdd:
		return "network_add"
	case KindNetworkRemove:
		return "network_remove"
	case KindEnvAdd:
		return "env_add"
	case KindEnvRemove:
		return "env_remove"
	case KindEnvChange:
		return "env_change"
	case KindInstallChange:
		return "install_change"
	case KindPackagesChange:
		return "packages_change"
	case KindMaskAddRemove:
		return "mask_add_remove"
	case KindImageChange:
		return "image_change"
	case KindIdentityChange:
		return "identity_change"
	case KindTemplateChange:
		return "template_change"
	case KindMountAddRemove:
		return "mount_add_remove"
	case KindServiceExecChange:
		return "service_exec_change"
	case KindServiceRestartChange:
		return "service_restart_change"
	case KindServiceAfterChange:
		return "service_after_change"
	case KindServiceWorkdirChange:
		return "service_workdir_change"
	case KindServiceUserChange:
		return "service_user_change"
	case KindServiceSystemdOverrideChange:
		return "service_systemd_override_change"
	case KindServiceHostnameChange:
		return "service_hostname_change"
	}
	return "unknown"
}

func flavorJSON(f FlavorKind) string {
	switch f {
	case FlavorLiveOnly:
		return "live"
	case FlavorStopShell:
		return "stop_shell"
	case FlavorTeardownShell:
		return "teardown_shell"
	}
	return "unknown"
}

// formatChange returns a one-line, human-readable description of a Change.
func formatChange(c Change) string {
	switch c.Kind {
	case KindPortAdd:
		return fmt.Sprintf("+ port %s (%s)", c.New, c.Service)
	case KindPortRemove:
		return fmt.Sprintf("- port %s (%s)", c.Old, c.Service)
	case KindPortChange:
		return fmt.Sprintf("~ port %s: %s → %s", c.Service, c.Old, c.New)
	case KindNetworkAdd:
		return fmt.Sprintf("+ allow network %s", c.New)
	case KindNetworkRemove:
		return fmt.Sprintf("- allow network %s", c.Old)
	case KindEnvAdd:
		return fmt.Sprintf("+ env: %s.%s = %q", c.Service, c.Key, c.New)
	case KindEnvRemove:
		return fmt.Sprintf("- env: %s.%s", c.Service, c.Key)
	case KindEnvChange:
		return fmt.Sprintf("~ env: %s.%s: %q → %q", c.Service, c.Key, c.Old, c.New)
	case KindInstallChange:
		return "~ install commands"
	case KindPackagesChange:
		return "~ packages"
	case KindMountAddRemove:
		return "~ mounts"
	case KindMaskAddRemove:
		return fmt.Sprintf("~ volumes: %s", c.Service)
	case KindServiceExecChange:
		return fmt.Sprintf("~ service exec: %s", c.Service)
	case KindServiceRestartChange:
		return fmt.Sprintf("~ service restart: %s", c.Service)
	case KindServiceAfterChange:
		return fmt.Sprintf("~ service after: %s", c.Service)
	case KindServiceWorkdirChange:
		return fmt.Sprintf("~ service workdir: %s", c.Service)
	case KindServiceUserChange:
		return fmt.Sprintf("~ service user: %s", c.Service)
	case KindServiceSystemdOverrideChange:
		return fmt.Sprintf("~ service systemd override: %s", c.Service)
	case KindServiceHostnameChange:
		return fmt.Sprintf("~ service hostname: %s: %q → %q", c.Service, c.Old, c.New)
	case KindImageChange:
		return "~ base image"
	case KindIdentityChange:
		return "~ project identity"
	case KindTemplateChange:
		switch {
		case c.Old == "" && c.New != "":
			return fmt.Sprintf("+ template: %s → %s", c.Service, c.Detail)
		case c.Old != "" && c.New == "":
			return fmt.Sprintf("- template: %s (sandbox file persists; recreate to wipe)", c.Detail)
		default:
			return fmt.Sprintf("~ template: %s → %s", c.Service, c.Detail)
		}
	}
	return "(unknown change)"
}
