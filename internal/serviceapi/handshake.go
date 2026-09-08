package serviceapi

import (
	"encoding/json"
	"net/http"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/supervisor"
)

// HandshakeResponse is the body of GET /handshake. Build is always present
// (the daemon-sync fingerprint check the CLI does on every daemon-touching
// command). Proxy carries the project's iron-proxy health so the command
// can report drift to the user — `devm reconcile` is the only thing that
// heals it. Proxy is nil when no project name is supplied, or when the
// named project has no StateCache row (it has never run).
type HandshakeResponse struct {
	Build Build        `json:"build"`
	Proxy *ProxyHealth `json:"proxy,omitempty"`
}

// RegisterHandshakeHandler wires GET /handshake. Build and per-project
// proxy health are both served from cache — cache.SetBuild is called
// once at daemon startup (see runner.go) so cache.Global().Build is
// always the daemon's identity by request time.
func RegisterHandshakeHandler(s *Server, cfg identity.Config, build Build, sup *supervisor.Supervisor, proxy *ProxyServer, cache *StateCache) {
	s.Register("/handshake", func(w http.ResponseWriter, r *http.Request) {
		resp := HandshakeResponse{Build: cache.Global().Build}
		if name := r.URL.Query().Get("name"); name != "" {
			if err := validProjectID(name); err == nil {
				if row, ok := cache.ProjectRow(name); ok {
					h := row.IronProxyHealth
					resp.Proxy = &h
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}
