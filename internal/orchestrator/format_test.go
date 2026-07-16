package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatStatusText_RunningInSync(t *testing.T) {
	out := FormatStatusText(StatusResult{HasProject: true,
		Sandbox: "x", State: "running",
		Sessions:    []Session{{PID: 27, Comm: "bash", TTY: "pts/1", User: "agent"}},
		PendingLive: 0, PendingRecreate: 0,
	})
	assert.Contains(t, out, "Sandbox: x")
	assert.Contains(t, out, "State:   running")
	assert.Contains(t, out, "pts/1: bash (PID 27, owner agent)")
	assert.Contains(t, out, "In sync.")
}

func TestFormatStatusText_RunningWithPending(t *testing.T) {
	out := FormatStatusText(StatusResult{HasProject: true,
		Sandbox: "x", State: "running",
		PendingLive: 2, PendingRecreate: 1,
	})
	assert.Contains(t, out, "Pending changes: 2 live, 1 require recreate")
	assert.Contains(t, out, "Run `devm reconcile` to apply.")
}

func TestFormatStatusText_Stopped(t *testing.T) {
	out := FormatStatusText(StatusResult{HasProject: true, Sandbox: "x", State: "stopped"})
	assert.Contains(t, out, "Sandbox stopped; config changes will apply on next `devm shell`.")
	assert.NotContains(t, out, "Active sessions")
}

func TestFormatReconcileText_LiveOnly(t *testing.T) {
	out := FormatReconcileText(ReconcileResult{
		Applied: []reconcile.Change{{Kind: reconcile.KindPortAdd, Service: "api", Key: "8080", New: "8080"}},
	})
	assert.Contains(t, out, "Applied 1 live change")
	assert.Contains(t, out, "+ port 8080 (api)")
}

func TestFormatReconcileText_RecreatePending(t *testing.T) {
	out := FormatReconcileText(ReconcileResult{
		Applied: []reconcile.Change{{Kind: reconcile.KindPortAdd, Service: "api", Key: "8080", New: "8080"}},
		RecreateRequired: []reconcile.Change{
			{Kind: reconcile.KindPackagesChange},
		},
		Flavor:   reconcile.FlavorTeardownShell,
		Sessions: []Session{{PID: 27, Comm: "bash", TTY: "pts/1", User: "agent"}},
	})
	assert.Contains(t, out, "Applied 1 live change")
	assert.Contains(t, out, "1 change(s) require recreate")
	assert.Contains(t, out, "~ packages")
	assert.Contains(t, out, "Teardown + recreate sandbox?")
	assert.Contains(t, out, "Will hang up 1 active session")
	assert.NotContains(t, out, "require restart")
}

func TestFormatReconcileText_RestartPending(t *testing.T) {
	// KindStartupChange is BucketRestartVM — distinct "restart" category,
	// not folded into the "recreate" (teardown) section.
	out := FormatReconcileText(ReconcileResult{
		RecreateRequired: []reconcile.Change{
			{Kind: reconcile.KindStartupChange},
		},
		Flavor:   reconcile.FlavorStopShell,
		Sessions: []Session{{PID: 27, Comm: "bash", TTY: "pts/1", User: "agent"}},
	})
	assert.Contains(t, out, "1 change(s) require restart")
	assert.Contains(t, out, "~ startup commands")
	assert.Contains(t, out, "Restart sandbox (`devm stop` + `devm shell`) to apply")
	assert.Contains(t, out, "Will hang up 1 active session")
	assert.NotContains(t, out, "require recreate")
	assert.NotContains(t, out, "Teardown")
}

func TestFormatReconcileText_RestartAndRecreatePending_BothSectionsRender(t *testing.T) {
	out := FormatReconcileText(ReconcileResult{
		RecreateRequired: []reconcile.Change{
			{Kind: reconcile.KindStartupChange},
			{Kind: reconcile.KindPackagesChange},
		},
		Flavor: reconcile.FlavorTeardownShell,
	})
	assert.Contains(t, out, "1 change(s) require restart")
	assert.Contains(t, out, "~ startup commands")
	assert.Contains(t, out, "1 change(s) require recreate")
	assert.Contains(t, out, "~ packages")
}

func TestFormatReconcileText_IronProxyRestartAppliedNormalPath(t *testing.T) {
	r := ReconcileResult{
		AppliedIronProxy: []reconcile.Change{
			{Kind: reconcile.KindNetworkAdd, Key: "api.cloudflare.com", New: "api.cloudflare.com"},
			{Kind: reconcile.KindSecretAdd, Key: "CLOUDFLARE_TOKEN"},
		},
	}
	out := FormatReconcileText(r)
	assert.Contains(t, out, "Applied 2 network egress change")
	assert.Contains(t, out, "network.allow: api.cloudflare.com")
	assert.Contains(t, out, "secret: CLOUDFLARE_TOKEN")
	// No revive line when Revived is false (default).
	assert.NotContains(t, out, "was not running")
}

func TestFormatReconcileText_IronProxyRestartVMOff(t *testing.T) {
	r := ReconcileResult{
		SandboxState: "stopped",
		AppliedIronProxy: []reconcile.Change{
			{Kind: reconcile.KindNetworkAdd, Key: "api.example.com", New: "api.example.com"},
		},
	}
	out := FormatReconcileText(r)
	assert.Contains(t, out, "Recorded 1 network egress change")
	assert.NotContains(t, out, "Applied")
}

func TestFormatReconcileText_IronProxyRestartRevived(t *testing.T) {
	r := ReconcileResult{
		AppliedIronProxy: []reconcile.Change{
			{Kind: reconcile.KindNetworkAdd, Key: "x.com", New: "x.com"},
		},
		IronProxyRevived: true,
		Sandbox:          "myproj",
	}
	out := FormatReconcileText(r)
	assert.Contains(t, out, "iron-proxy for myproj was not running — respawned")
}

func TestFormatStatusJSON(t *testing.T) {
	js := FormatStatusJSON(StatusResult{HasProject: true,
		Sandbox: "x", State: "running",
		Sessions:    []Session{{PID: 27, Comm: "bash", TTY: "pts/1", User: "agent"}},
		PendingLive: 2, PendingRecreate: 1,
		ProxyHealth: &serviceapi.ProxyHealth{Status: serviceapi.ProxyStale, NeedsSecrets: true},
	})
	var parsed map[string]any
	assert.NoError(t, json.Unmarshal([]byte(js), &parsed))
	proj := parsed["project"].(map[string]any)
	assert.Equal(t, "x", proj["sandbox"])
	assert.Equal(t, "running", proj["state"])
	pending := proj["pending_changes"].(map[string]any)
	assert.Equal(t, float64(2), pending["live"])
	assert.Equal(t, float64(1), pending["recreate"])
	// Global health block emits both true and false bools explicitly —
	// no omitempty swallowing the false case.
	health := parsed["health"].(map[string]any)
	assert.Contains(t, health, "ca_trusted")
	assert.Contains(t, health, "dns_healthy")
	assert.Contains(t, health, "proxy_healthy")
	ironProxy := proj["iron_proxy"].(map[string]any)
	assert.Equal(t, "stale", ironProxy["status"])
	assert.Equal(t, true, ironProxy["needs_secrets"])
}

func TestFormatStatusJSON_IronProxyNilOmitted(t *testing.T) {
	js := FormatStatusJSON(StatusResult{HasProject: true, Sandbox: "x", State: "running"})
	var parsed map[string]any
	assert.NoError(t, json.Unmarshal([]byte(js), &parsed))
	proj := parsed["project"].(map[string]any)
	assert.NotContains(t, proj, "iron_proxy")
}

func TestFormatReconcileJSON(t *testing.T) {
	js := FormatReconcileJSON(ReconcileResult{
		Rendered: true, SandboxState: "running",
		Applied:          []reconcile.Change{{Kind: reconcile.KindPortAdd, Service: "api", Key: "8080", New: "8080"}},
		RecreateRequired: []reconcile.Change{{Kind: reconcile.KindPackagesChange}},
		Flavor:           reconcile.FlavorTeardownShell,
		Sessions:         []Session{{PID: 27, Comm: "bash", TTY: "pts/1", User: "agent"}},
		NextAction:       "needs_approval",
	})
	var parsed map[string]any
	assert.NoError(t, json.Unmarshal([]byte(js), &parsed))
	assert.Equal(t, true, parsed["rendered"])
	assert.Equal(t, "needs_approval", parsed["next_action"])
	rec := parsed["recreate_required"].(map[string]any)
	changes := rec["changes"].([]any)
	require.Len(t, changes, 1)
	assert.Equal(t, "packages_change", changes[0].(map[string]any)["kind"])
	assert.NotContains(t, parsed, "restart_required")
}

func TestFormatReconcileJSON_RestartRequired_SeparateFromRecreate(t *testing.T) {
	js := FormatReconcileJSON(ReconcileResult{
		Rendered: true, SandboxState: "running",
		RecreateRequired: []reconcile.Change{
			{Kind: reconcile.KindStartupChange},
			{Kind: reconcile.KindPackagesChange},
		},
		Flavor:     reconcile.FlavorTeardownShell,
		NextAction: "needs_approval",
	})
	var parsed map[string]any
	assert.NoError(t, json.Unmarshal([]byte(js), &parsed))

	restart := parsed["restart_required"].(map[string]any)
	restartChanges := restart["changes"].([]any)
	require.Len(t, restartChanges, 1)
	assert.Equal(t, "startup_change", restartChanges[0].(map[string]any)["kind"])

	recreate := parsed["recreate_required"].(map[string]any)
	recreateChanges := recreate["changes"].([]any)
	require.Len(t, recreateChanges, 1)
	assert.Equal(t, "packages_change", recreateChanges[0].(map[string]any)["kind"])
}

func TestFormatStatusText_ProxyNone(t *testing.T) {
	res := StatusResult{HasProject: true,
		Routing: serviceapi.RoutingStatus{Proxy: "none"},
	}
	text := FormatStatusText(res)
	assert.Contains(t, text, "proxy: none (devm route disabled)")
}

func TestFormatStatusText_ProxyUnreachable(t *testing.T) {
	res := StatusResult{HasProject: true,
		Routing: serviceapi.RoutingStatus{Proxy: "devm", ProxyReachable: false},
	}
	text := FormatStatusText(res)
	assert.Contains(t, text, "proxy: devm (unreachable)")
}

func TestFormatStatusText_NoRoutes(t *testing.T) {
	res := StatusResult{HasProject: true,
		Routing: serviceapi.RoutingStatus{Proxy: "devm", ProxyReachable: true, Mode: ""},
	}
	text := FormatStatusText(res)
	assert.Contains(t, text, "mode: (no routes)")
}

func TestFormatStatusText_VMMode_WithRoutes(t *testing.T) {
	res := StatusResult{HasProject: true,
		Routing: serviceapi.RoutingStatus{
			Proxy: "devm", ProxyReachable: true, Mode: "vm",
			Routes: []serviceapi.RouteStatus{
				{Hostname: "api.foo.test", Dial: "localhost:55432", Mode: "vm"},
				{Hostname: "app.foo.test", Dial: "localhost:53000", Mode: "vm"},
			},
		},
	}
	text := FormatStatusText(res)
	assert.Contains(t, text, "mode:")
	assert.Contains(t, text, "vm")
	assert.Contains(t, text, "api.foo.test")
	assert.Contains(t, text, "localhost:55432")
	assert.NotContains(t, text, "resolves")
}

func TestFormatStatusText_MixedMode_TagsRoutes(t *testing.T) {
	res := StatusResult{HasProject: true,
		Routing: serviceapi.RoutingStatus{
			Proxy: "devm", ProxyReachable: true, Mode: "mixed (drift)",
			Routes: []serviceapi.RouteStatus{
				{Hostname: "api.foo.test", Dial: "localhost:55432", Mode: "vm"},
				{Hostname: "app.foo.test", Dial: "localhost:3000", Mode: "local"},
			},
		},
	}
	text := FormatStatusText(res)
	assert.Contains(t, text, "mixed (drift)")
	assert.Contains(t, text, "(vm)")
	assert.Contains(t, text, "(local)")
}

func TestFormatStatusText_DNSLine_SilentWhenHealthy(t *testing.T) {
	res := StatusResult{HasProject: true,
		Sandbox: "test", State: "running",
		DNSHealthy: true,
	}
	out := FormatStatusText(res)
	assert.NotContains(t, out, "dns:", "DNS line should be invisible when healthy")
}

func TestFormatStatusText_DNSLine_RedWhenUnhealthy(t *testing.T) {
	res := StatusResult{HasProject: true,
		Sandbox: "test", State: "running",
		DNSHealthy: false, DNSError: "resolving foo: timeout",
	}
	out := FormatStatusText(res)
	assert.Contains(t, out, "dns: NOT WORKING")
	assert.Contains(t, out, "resolving foo: timeout")
	assert.Contains(t, out, "devm install")
}

func TestFormatStatusText_CALine_SilentWhenTrusted(t *testing.T) {
	res := StatusResult{HasProject: true,
		Sandbox: "x", State: "running",
		DNSHealthy: true, CATrusted: true, ProxyHealthy: true,
	}
	assert.NotContains(t, FormatStatusText(res), "ca:")
}

func TestFormatStatusText_CALine_RedWhenUntrusted(t *testing.T) {
	res := StatusResult{HasProject: true,
		Sandbox: "x", State: "running",
		DNSHealthy: true, CATrusted: false, ProxyHealthy: true,
	}
	out := FormatStatusText(res)
	assert.Contains(t, out, "ca: NOT TRUSTED")
	assert.Contains(t, out, "devm install")
}

func TestFormatStatusText_ProxyLine_SilentWhenHealthy(t *testing.T) {
	res := StatusResult{HasProject: true,
		Sandbox: "x", State: "running",
		DNSHealthy: true, CATrusted: true, ProxyHealthy: true,
	}
	assert.NotContains(t, FormatStatusText(res), "proxy: NOT LISTENING")
}

func TestFormatStatusText_ProxyLine_RedWhenDown(t *testing.T) {
	res := StatusResult{HasProject: true,
		Sandbox: "x", State: "running",
		DNSHealthy: true, CATrusted: true,
		ProxyHealthy: false,
		ProxyError:   "dial tcp 127.0.0.1:443: connect: connection refused",
	}
	out := FormatStatusText(res)
	assert.Contains(t, out, "proxy: NOT LISTENING")
	assert.Contains(t, out, "connection refused")
}

func TestFormatStatusText_IronProxyHealth_OK(t *testing.T) {
	res := StatusResult{HasProject: true,
		Sandbox: "x", State: "running",
		ProxyHealth: &serviceapi.ProxyHealth{Status: serviceapi.ProxyOK},
	}
	out := FormatStatusText(res)
	assert.Contains(t, out, "iron-proxy: ok")
}

func TestFormatStatusText_IronProxyHealth_Missing(t *testing.T) {
	res := StatusResult{HasProject: true,
		Sandbox: "x", State: "running",
		ProxyHealth: &serviceapi.ProxyHealth{Status: serviceapi.ProxyMissing},
	}
	out := FormatStatusText(res)
	assert.Contains(t, out, "iron-proxy: MISSING (run 'devm reconcile')")
}

func TestFormatStatusText_IronProxyHealth_Stale(t *testing.T) {
	res := StatusResult{HasProject: true,
		Sandbox: "x", State: "running",
		ProxyHealth: &serviceapi.ProxyHealth{Status: serviceapi.ProxyStale},
	}
	out := FormatStatusText(res)
	assert.Contains(t, out, "iron-proxy: STALE (run 'devm reconcile')")
}

func TestFormatStatusText_IronProxyHealth_NilOmitsLine(t *testing.T) {
	res := StatusResult{HasProject: true, Sandbox: "x", State: "running"}
	out := FormatStatusText(res)
	assert.NotContains(t, out, "iron-proxy:")
}

func TestFormatDaemonStatus_MismatchColor(t *testing.T) {
	d := DaemonStatus{
		Running: true, BinaryPath: "/opt/devm/bin/devm",
		Fingerprint: "abc", FingerprintMatchesCLI: false,
	}
	t.Run("plain_when_disabled", func(t *testing.T) {
		UseColor = false
		out := formatDaemonStatus(d)
		assert.Contains(t, out, "MISMATCH")
		assert.NotContains(t, out, "\x1b[")
	})
	t.Run("red_when_enabled", func(t *testing.T) {
		UseColor = true
		defer func() { UseColor = false }()
		out := formatDaemonStatus(d)
		assert.Contains(t, out, "\x1b[31m")
		assert.Contains(t, out, "\x1b[0m")
	})
}

func TestFormatStatusAllText_TableShape(t *testing.T) {
	rows := []serviceapi.ProjectStatus{
		{Name: "sewtrue", VMRunning: true, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyMissing}},
		{Name: "everstone", VMRunning: true, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyOK}},
		{Name: "ship5", VMRunning: false, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyMissing}},
	}
	UseColor = false
	out := FormatStatusAllText(rows)

	assert.Contains(t, out, "PROJECT")
	assert.Contains(t, out, "VM")
	assert.Contains(t, out, "IRON-PROXY")
	assert.Contains(t, out, "RECONCILE")

	assert.Regexp(t, `sewtrue\s+running\s+MISSING\s+required`, out)
	assert.Regexp(t, `everstone\s+running\s+ok\s+—`, out)
	assert.Regexp(t, `ship5\s+stopped\s+—\s+—`, out)
}

func TestFormatStatusAllText_StaleShowsReconcileRequired(t *testing.T) {
	rows := []serviceapi.ProjectStatus{
		{Name: "p", VMRunning: true, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyStale}},
	}
	UseColor = false
	out := FormatStatusAllText(rows)
	assert.Regexp(t, `p\s+running\s+STALE\s+required`, out)
}

func TestFormatStatusAllText_Empty(t *testing.T) {
	UseColor = false
	out := FormatStatusAllText(nil)
	assert.Contains(t, out, "No projects")
}

func TestFormatStatusAllText_Color(t *testing.T) {
	rows := []serviceapi.ProjectStatus{
		{Name: "ok-proj", VMRunning: true, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyOK}},
		{Name: "bad-proj", VMRunning: true, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyMissing}},
	}
	t.Run("plain_when_disabled", func(t *testing.T) {
		UseColor = false
		out := FormatStatusAllText(rows)
		assert.NotContains(t, out, "\x1b[")
	})
	t.Run("colored_when_enabled", func(t *testing.T) {
		UseColor = true
		defer func() { UseColor = false }()
		out := FormatStatusAllText(rows)
		assert.Contains(t, out, "\x1b[32mok\x1b[0m")
		assert.Contains(t, out, "\x1b[31mMISSING\x1b[0m")
	})
}

func TestFormatStatusAllJSON(t *testing.T) {
	rows := []serviceapi.ProjectStatus{
		{Name: "p", VMRunning: true, Proxy: serviceapi.ProxyHealth{Status: serviceapi.ProxyOK}},
	}
	out := FormatStatusAllJSON(rows)
	assert.Contains(t, out, `"name": "p"`)
	assert.Contains(t, out, `"vm_running": true`)
	assert.Contains(t, out, `"status": "ok"`)
}

func TestFormatChange_Template(t *testing.T) {
	// Added template.
	added := reconcile.Change{Kind: reconcile.KindTemplateChange, Service: "web", Detail: "/etc/caddy/Caddyfile", New: "installed"}
	assert.Equal(t, "+ template: web → /etc/caddy/Caddyfile", formatChange(added))

	// Changed template.
	changed := reconcile.Change{Kind: reconcile.KindTemplateChange, Service: "web", Detail: "/etc/caddy/Caddyfile", Old: "previous", New: "updated"}
	assert.Equal(t, "~ template: web → /etc/caddy/Caddyfile", formatChange(changed))

	// Removed template.
	removed := reconcile.Change{Kind: reconcile.KindTemplateChange, Service: "", Detail: "00-web-Caddyfile.sh", Old: "previous"}
	assert.Equal(t, "- template: 00-web-Caddyfile.sh (sandbox file persists; recreate to wipe)", formatChange(removed))

	// JSON mapping.
	assert.Equal(t, "template_change", changeKindJSON(reconcile.KindTemplateChange))
}

func TestFormatChange_ServiceDirect(t *testing.T) {
	turnedOn := reconcile.Change{Kind: reconcile.KindServiceDirectChange, Service: "db", Old: "false", New: "true"}
	assert.Equal(t, "~ service direct: db: on", formatChange(turnedOn))

	turnedOff := reconcile.Change{Kind: reconcile.KindServiceDirectChange, Service: "db", Old: "true", New: "false"}
	assert.Equal(t, "~ service direct: db: off", formatChange(turnedOff))

	assert.Equal(t, "service_direct_change", changeKindJSON(reconcile.KindServiceDirectChange))
}

func TestFormatChange_Startup(t *testing.T) {
	change := reconcile.Change{Kind: reconcile.KindStartupChange}
	assert.Equal(t, "~ startup commands", formatChange(change))
	assert.Equal(t, "startup_change", changeKindJSON(reconcile.KindStartupChange))
}
