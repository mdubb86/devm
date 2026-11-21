package serviceapi

import (
	"encoding/json"
	"net/http"
)

// UserSSHAuthSockRequest is the body of POST /env/ssh-auth-sock. Empty
// Path clears the cached value.
type UserSSHAuthSockRequest struct {
	Path string `json:"path"`
}

// handleUserSSHAuthSock stores the CLI-pushed SSH_AUTH_SOCK on the
// ProxyServer. The agent-bridge reads it at ServeConn time to dial
// Mac's ssh-agent. See ProxySSHAuthSockResolver for why this has to
// come from the CLI rather than the daemon's own environment.
func handleUserSSHAuthSock(proxy *ProxyServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req UserSSHAuthSockRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if proxy == nil {
			http.Error(w, "proxy server not configured", http.StatusInternalServerError)
			return
		}
		proxy.SetUserSSHAuthSock(req.Path)
		w.WriteHeader(http.StatusNoContent)
	}
}
