package serviceapi

import (
	"encoding/json"
	"net/http"

	"github.com/mdubb86/devm/internal/supervisor"
)

// HandshakeResponse is the body of GET /handshake. Build is always present
// (the daemon-sync fingerprint check the CLI does on every daemon-touching
// command). Proxy is present only when project_id is supplied, and carries
// the project's iron-proxy health so the command can report drift to the
// user — `devm reconcile` is the only thing that heals it.
type HandshakeResponse struct {
	Build Build        `json:"build"`
	Proxy *ProxyHealth `json:"proxy,omitempty"`
}

// RegisterHandshakeHandler wires GET /handshake. build is the daemon's
// identity (same value /version reports); sup is queried for proxy health.
func RegisterHandshakeHandler(s *Server, build Build, sup *supervisor.Supervisor) {
	s.Register("/handshake", func(w http.ResponseWriter, r *http.Request) {
		resp := HandshakeResponse{Build: build}
		if pid := r.URL.Query().Get("project_id"); pid != "" {
			if err := validProjectID(pid); err == nil {
				h := computeProxyHealth(sup, pid)
				resp.Proxy = &h
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}
