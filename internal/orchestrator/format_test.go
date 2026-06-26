package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
)

func TestFormatStatusText_RunningInSync(t *testing.T) {
	out := FormatStatusText(StatusResult{
		Sandbox: "x", State: "running",
		Sessions:    []sandbox.Session{{PID: 27, Comm: "bash", TTY: "pts/1", User: "agent"}},
		PendingLive: 0, PendingRecreate: 0,
	})
	assert.Contains(t, out, "Sandbox: x")
	assert.Contains(t, out, "State:   running")
	assert.Contains(t, out, "pts/1: bash (PID 27, owner agent)")
	assert.Contains(t, out, "In sync.")
}

func TestFormatStatusText_RunningWithPending(t *testing.T) {
	out := FormatStatusText(StatusResult{
		Sandbox: "x", State: "running",
		PendingLive: 2, PendingRecreate: 1,
	})
	assert.Contains(t, out, "Pending changes: 2 live, 1 require recreate")
	assert.Contains(t, out, "Run `devm reconcile` to apply.")
}

func TestFormatStatusText_Stopped(t *testing.T) {
	out := FormatStatusText(StatusResult{Sandbox: "x", State: "stopped"})
	assert.Contains(t, out, "Sandbox stopped; config changes will apply on next `devm shell`.")
	assert.NotContains(t, out, "Active sessions")
}

func TestFormatReconcileText_LiveOnly(t *testing.T) {
	out := FormatReconcileText(ReconcileResult{
		Applied: []Change{{Kind: KindPortAdd, Service: "api", Key: "8080", New: "8080"}},
	})
	assert.Contains(t, out, "Applied 1 live change")
	assert.Contains(t, out, "+ port 8080 (api)")
}

func TestFormatReconcileText_RecreatePending(t *testing.T) {
	out := FormatReconcileText(ReconcileResult{
		Applied: []Change{{Kind: KindPortAdd, Service: "api", Key: "8080", New: "8080"}},
		RecreateRequired: []Change{
			{Kind: KindEnvChange, Service: "api", Key: "LOG_LEVEL", Old: "info", New: "debug"},
		},
		Flavor:   FlavorStopShell,
		Sessions: []sandbox.Session{{PID: 27, Comm: "bash", TTY: "pts/1", User: "agent"}},
	})
	assert.Contains(t, out, "Applied 1 live change")
	assert.Contains(t, out, "1 change(s) require recreate")
	assert.Contains(t, out, `env: api.LOG_LEVEL: "info" → "debug"`)
	assert.Contains(t, out, "Restart sandbox to apply")
	assert.Contains(t, out, "Will hang up 1 active session")
}

func TestFormatStatusJSON(t *testing.T) {
	js := FormatStatusJSON(StatusResult{
		Sandbox: "x", State: "running",
		Sessions:    []sandbox.Session{{PID: 27, Comm: "bash", TTY: "pts/1", User: "agent"}},
		PendingLive: 2, PendingRecreate: 1,
	})
	var parsed map[string]any
	assert.NoError(t, json.Unmarshal([]byte(js), &parsed))
	assert.Equal(t, "x", parsed["sandbox"])
	assert.Equal(t, "running", parsed["state"])
	pending := parsed["pending_changes"].(map[string]any)
	assert.Equal(t, float64(2), pending["live"])
	assert.Equal(t, float64(1), pending["recreate"])
}

func TestFormatReconcileJSON(t *testing.T) {
	js := FormatReconcileJSON(ReconcileResult{
		Rendered: true, SandboxState: "running",
		Applied:          []Change{{Kind: KindPortAdd, Service: "api", Key: "8080", New: "8080"}},
		RecreateRequired: []Change{{Kind: KindEnvChange, Service: "api", Key: "LOG_LEVEL", Old: "info", New: "debug"}},
		Flavor:           FlavorStopShell,
		Sessions:         []sandbox.Session{{PID: 27, Comm: "bash", TTY: "pts/1", User: "agent"}},
		NextAction:       "needs_approval",
	})
	var parsed map[string]any
	assert.NoError(t, json.Unmarshal([]byte(js), &parsed))
	assert.Equal(t, true, parsed["rendered"])
	assert.Equal(t, "needs_approval", parsed["next_action"])
	rec := parsed["recreate_required"].(map[string]any)
	assert.Equal(t, "stop_shell", rec["flavor"])
}

func TestFormatStatusText_ProxyNone(t *testing.T) {
	res := StatusResult{
		Routing: serviceapi.RoutingStatus{Proxy: "none"},
	}
	text := FormatStatusText(res)
	assert.Contains(t, text, "proxy: none (devm route disabled)")
}

func TestFormatStatusText_ProxyUnreachable(t *testing.T) {
	res := StatusResult{
		Routing: serviceapi.RoutingStatus{Proxy: "devm", ProxyReachable: false},
	}
	text := FormatStatusText(res)
	assert.Contains(t, text, "proxy: devm (unreachable)")
}

func TestFormatStatusText_NoRoutes(t *testing.T) {
	res := StatusResult{
		Routing: serviceapi.RoutingStatus{Proxy: "devm", ProxyReachable: true, Mode: ""},
	}
	text := FormatStatusText(res)
	assert.Contains(t, text, "mode: (no routes)")
}

func TestFormatStatusText_VMMode_WithRoutes(t *testing.T) {
	res := StatusResult{
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
	res := StatusResult{
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
	res := StatusResult{
		Sandbox: "test", State: "running",
		DNSHealthy: true,
	}
	out := FormatStatusText(res)
	assert.NotContains(t, out, "dns:", "DNS line should be invisible when healthy")
}

func TestFormatStatusText_DNSLine_RedWhenUnhealthy(t *testing.T) {
	res := StatusResult{
		Sandbox: "test", State: "running",
		DNSHealthy: false, DNSError: "resolving foo: timeout",
	}
	out := FormatStatusText(res)
	assert.Contains(t, out, "dns: NOT WORKING")
	assert.Contains(t, out, "resolving foo: timeout")
	assert.Contains(t, out, "devm install")
}

func TestFormatStatusText_CALine_SilentWhenTrusted(t *testing.T) {
	res := StatusResult{
		Sandbox: "x", State: "running",
		DNSHealthy: true, CATrusted: true, ProxyHealthy: true,
	}
	assert.NotContains(t, FormatStatusText(res), "ca:")
}

func TestFormatStatusText_CALine_RedWhenUntrusted(t *testing.T) {
	res := StatusResult{
		Sandbox: "x", State: "running",
		DNSHealthy: true, CATrusted: false, ProxyHealthy: true,
	}
	out := FormatStatusText(res)
	assert.Contains(t, out, "ca: NOT TRUSTED")
	assert.Contains(t, out, "devm install")
}

func TestFormatStatusText_ProxyLine_SilentWhenHealthy(t *testing.T) {
	res := StatusResult{
		Sandbox: "x", State: "running",
		DNSHealthy: true, CATrusted: true, ProxyHealthy: true,
	}
	assert.NotContains(t, FormatStatusText(res), "proxy: NOT LISTENING")
}

func TestFormatStatusText_ProxyLine_RedWhenDown(t *testing.T) {
	res := StatusResult{
		Sandbox: "x", State: "running",
		DNSHealthy: true, CATrusted: true,
		ProxyHealthy: false,
		ProxyError:   "dial tcp 127.0.0.1:443: connect: connection refused",
	}
	out := FormatStatusText(res)
	assert.Contains(t, out, "proxy: NOT LISTENING")
	assert.Contains(t, out, "connection refused")
}

func TestFormatChange_Template(t *testing.T) {
	// Added template.
	added := Change{Kind: KindTemplateChange, Service: "web", Detail: "/etc/caddy/Caddyfile", New: "installed"}
	assert.Equal(t, "+ template: web → /etc/caddy/Caddyfile", formatChange(added))

	// Changed template.
	changed := Change{Kind: KindTemplateChange, Service: "web", Detail: "/etc/caddy/Caddyfile", Old: "previous", New: "updated"}
	assert.Equal(t, "~ template: web → /etc/caddy/Caddyfile", formatChange(changed))

	// Removed template.
	removed := Change{Kind: KindTemplateChange, Service: "", Detail: "00-web-Caddyfile.sh", Old: "previous"}
	assert.Equal(t, "- template: 00-web-Caddyfile.sh (sandbox file persists; recreate to wipe)", formatChange(removed))

	// JSON mapping.
	assert.Equal(t, "template_change", changeKindJSON(KindTemplateChange))
}
