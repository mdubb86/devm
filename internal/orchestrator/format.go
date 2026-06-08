package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdubb86/devm/internal/sandbox"
)

// StatusResult is what `devm status` produces. Drift is non-empty only
// for `--live` invocations.
type StatusResult struct {
	Sandbox         string
	State           string // "running" | "stopped" | "absent"
	Sessions        []sandbox.Session
	PendingLive     int
	PendingRecreate int
	Drift           []DriftItem
}

// DriftItem is one piece of mismatch between snapshot and live sbx state.
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
		Sandbox        string  `json:"sandbox"`
		State          string  `json:"state"`
		Sessions       []sess  `json:"sessions"`
		PendingChanges pending `json:"pending_changes"`
		Drift          []drift `json:"drift"`
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
	case KindStartupChange:
		return "startup_change"
	case KindInstallChange:
		return "install_change"
	case KindMaskChange:
		return "mask_change"
	case KindImageChange:
		return "image_change"
	case KindIdentityChange:
		return "identity_change"
	case KindTemplateChange:
		return "template_change"
	case KindMountsChange:
		return "mounts_change"
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
	case KindStartupChange:
		return fmt.Sprintf("~ startup: %s", c.Service)
	case KindInstallChange:
		return "~ install commands"
	case KindMountsChange:
		return "~ mounts"
	case KindMaskChange:
		return fmt.Sprintf("~ volumes: %s", c.Service)
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
