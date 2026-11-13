package serviceapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
)

// VMSSHBundleFieldsRequest is the body shape for POST /vm/ssh-bundle-fields.
// The CLI-side provisioner posts the config it's about to bundle so the
// daemon can produce the four SSH bytes using the same single write path
// daemon-side callers use (computeSSHBundleFields).
type VMSSHBundleFieldsRequest struct {
	Name string        `json:"name"`
	Cfg  schema.Config `json:"cfg"`
}

// VMSSHBundleFieldsResponse carries the four blobs install.sh needs to
// stage the client-side ssh feature in the guest. Each []byte field
// JSON-encodes as base64; nil fields are omitted on the wire.
type VMSSHBundleFieldsResponse struct {
	ClientConfig     []byte `json:"client_config,omitempty"`
	JumpClientPriv   []byte `json:"jump_client_priv,omitempty"`
	KnownHostsAppend []byte `json:"known_hosts_append,omitempty"`
	AgentBridgeUnit  []byte `json:"agent_bridge_unit,omitempty"`
}

// handleSSHBundleFields serves POST /vm/ssh-bundle-fields. Callers must
// have already invoked /vm/start — that's when the per-project jump key
// and listeners get created and ProjectSnapshot has JumpPort/BridgePort
// to splice into the bundle. Returns 400 when cfg.ssh is empty (the
// caller gates on that).
func handleSSHBundleFields(ident identity.Config, proxy *ProxyServer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req VMSSHBundleFieldsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("bad json: %v", err), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if req.Cfg.SSH == nil || len(*req.Cfg.SSH) == 0 {
			http.Error(w, "cfg.ssh required", http.StatusBadRequest)
			return
		}
		if proxy == nil {
			http.Error(w, "ssh bundle fields: proxy not wired", http.StatusInternalServerError)
			return
		}
		f, err := computeSSHBundleFields(req.Cfg, ident.RuntimeDir(), proxy)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp := VMSSHBundleFieldsResponse{
			ClientConfig:     f.ClientConfig,
			JumpClientPriv:   f.JumpClientPriv,
			KnownHostsAppend: f.KnownHostsAppend,
			AgentBridgeUnit:  f.AgentBridgeUnit,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}
