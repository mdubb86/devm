package serviceapi

import (
	"net"
	"strconv"
	"sync"

	"github.com/mdubb86/devm/internal/daemonlog"
)

// agentBridgeLog dedupes bridge-side rejections and unresolvable-sock
// warnings. Same window as hairpin's SNI log so a hot-loop guest can't
// drown the daemon stdout log.
var agentBridgeLog = newServiceapiDedupLogger(hairpinSNILogWindow)

// AgentBridge serves one accepted TCP connection from the guest: it
// resolves Mac's SSH_AUTH_SOCK, dials it, and shuttles messages through
// the per-type filter (REQUEST_IDENTITIES / SIGN_REQUEST forward;
// everything else is answered locally with AgentFailure without ever
// reaching Mac's agent).
type AgentBridge struct {
	projectID   string
	resolveSock func() string
}

// NewAgentBridge builds a bridge bound to projectID. resolveSock is a
// test seam; production callers pass DefaultSSHAuthSockResolver().
func NewAgentBridge(projectID string, resolveSock func() string) *AgentBridge {
	return &AgentBridge{projectID: projectID, resolveSock: resolveSock}
}

// ServeConn handles one accepted TCP connection: resolve Mac's agent
// socket, dial it, and shuttle messages through the filter. Returns
// when either side closes.
func (b *AgentBridge) ServeConn(guestConn net.Conn) {
	defer guestConn.Close()
	sock := b.resolveSock()
	if sock == "" {
		agentBridgeLog.Logf(b.projectID+"|nosock",
			"agent-bridge: SSH_AUTH_SOCK unresolvable project=%s", b.projectID)
		_ = WriteAgentMessage(guestConn, AgentFailure, nil)
		return
	}
	macConn, err := net.Dial("unix", sock)
	if err != nil {
		daemonlog.Errorf("agent-bridge: dial %s project=%s: %v", sock, b.projectID, err)
		_ = WriteAgentMessage(guestConn, AgentFailure, nil)
		return
	}
	defer macConn.Close()

	// guestMu serializes frame writes to guestConn across both
	// directions: the filter goroutine's AgentFailure rejects and the
	// mac->guest relay of Mac's responses. WriteAgentMessage issues
	// three sequential Writes per frame, so holding mu across the whole
	// call is what makes the wire atomic at the frame boundary.
	var guestMu sync.Mutex
	writeGuestFrame := func(typ byte, payload []byte) error {
		guestMu.Lock()
		defer guestMu.Unlock()
		return WriteAgentMessage(guestConn, typ, payload)
	}

	var once sync.Once
	stop := make(chan struct{})
	closeStop := func() { once.Do(func() { close(stop) }) }

	// guest -> mac: filter per-message.
	go func() {
		defer closeStop()
		for {
			typ, payload, err := ReadAgentMessage(guestConn)
			if err != nil {
				return
			}
			switch AgentFilterVerdictFor(typ) {
			case AgentForward:
				if err := WriteAgentMessage(macConn, typ, payload); err != nil {
					return
				}
			case AgentReject:
				agentBridgeLog.Logf(b.projectID+"|reject|"+strconv.Itoa(int(typ)),
					"agent-bridge: rejected type=%d project=%s", typ, b.projectID)
				if err := writeGuestFrame(AgentFailure, nil); err != nil {
					return
				}
			default:
				// AgentFilterVerdictFor returns only Forward/Reject. A new
				// verdict added without updating this switch is an
				// invariant violation; fail loudly instead of hanging the
				// guest waiting for a response that never comes.
				panic("agent-bridge: unhandled filter verdict")
			}
		}
	}()
	// mac -> guest: re-frame each response under the same lock as
	// rejects. A raw io.Copy would split frames across Writes whenever
	// macConn.Read returns partial bytes, and the filter's AgentFailure
	// could slip in between those Writes and corrupt the wire.
	go func() {
		defer closeStop()
		for {
			typ, payload, err := ReadAgentMessage(macConn)
			if err != nil {
				return
			}
			if err := writeGuestFrame(typ, payload); err != nil {
				return
			}
		}
	}()
	// Wait for either direction to finish; the defers then close both
	// conns, which unblocks whichever goroutine is still parked on a
	// read.
	<-stop
}

// ProxySSHAuthSockResolver returns a resolver bound to the ProxyServer's
// CLI-published SSH_AUTH_SOCK cache. The spec's `launchctl asuser` path
// is unusable from a non-root LaunchDaemon in the system domain
// (crossing into the aqua bootstrap requires root); the CLI runs in
// aqua and pushes the value via POST /env/ssh-auth-sock before any
// guest SSH operation. Empty return means nothing has been pushed yet,
// which ServeConn treats the same as an unresolvable sock.
func ProxySSHAuthSockResolver(p *ProxyServer) func() string {
	return p.UserSSHAuthSock
}
