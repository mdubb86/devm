package devmbundle

import "github.com/mdubb86/devm/internal/schema"

// SSHBundleFields are the four SSH-related bytes a project's guest
// bundle needs when cfg.project.ssh is non-empty: the ssh-client
// config body, the per-project client private key, the known_hosts
// lines, and the systemd unit for the guest-side agent bridge.
type SSHBundleFields struct {
	ClientConfig     []byte
	JumpClientPriv   []byte
	KnownHostsAppend []byte
	AgentBridgeUnit  []byte
}

// SSHEnricher is the daemon-wired callback BuildInputFor consults when
// cfg.project.ssh is non-empty. It resolves the project's SSH targets,
// reads the jump/bridge ports and the per-project key material, and
// renders the four bundle bytes.
//
// Package-level var (not a parameter) so apply_live.go and
// RefreshGuestBundle don't both have to thread a *ProxyServer through
// — they live in packages that don't own one, and importing serviceapi
// into devmbundle would introduce a cycle. RunService binds this at
// daemon startup; tests that don't care about SSH bundle bytes leave
// it nil.
var SSHEnricher func(cfg schema.Config, runtimeDir string) (SSHBundleFields, error)
