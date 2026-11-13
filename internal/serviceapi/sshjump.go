package serviceapi

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/schema"
)

// JumpAllowlist is an exact-match set of (host, port) pairs the
// devm-jump will dial on behalf of a guest's direct-tcpip channel open.
type JumpAllowlist struct {
	entries map[string]struct{}
}

func NewJumpAllowlist(targets []schema.ResolvedSSHTarget) *JumpAllowlist {
	a := &JumpAllowlist{entries: make(map[string]struct{}, len(targets))}
	for _, t := range targets {
		a.entries[net.JoinHostPort(t.HostName, strconv.Itoa(t.Port))] = struct{}{}
	}
	return a
}

func (a *JumpAllowlist) Contains(host string, port int) bool {
	_, ok := a.entries[net.JoinHostPort(host, strconv.Itoa(port))]
	return ok
}

// directTCPExtra is the wire-format payload of a direct-tcpip channel
// open (RFC 4254 §7.2). Fields are populated by ssh.Unmarshal.
type directTCPExtra struct {
	DstHost    string
	DstPort    uint32
	OriginHost string
	OriginPort uint32
}

// JumpServer runs the SSH side of devm-jump on one accepted TCP conn
// per call to ServeConn. Only direct-tcpip channels to hosts/ports in
// the allowlist are honored; session / tcpip-forward / everything else
// is rejected with Prohibited.
type JumpServer struct {
	cfg       *ssh.ServerConfig
	allow     *JumpAllowlist
	projectID string
}

// jumpAcceptLog dedupes direct-tcpip denial messages so a hot-loop
// guest probe stays a per-minute heartbeat in the daemon log rather
// than flooding it.
var jumpAcceptLog = newServiceapiDedupLogger(hairpinSNILogWindow)

// NewJumpServer returns a JumpServer configured to accept exactly one
// client public key (the per-project devm-jump-id key). hostKey is the
// daemon-persistent host key; allow is the (host, port) allowlist
// derived from this project's resolved ssh targets.
func NewJumpServer(hostKey ssh.Signer, clientAuthorizedKey ssh.PublicKey, allow *JumpAllowlist, projectID string) *JumpServer {
	wantMarshal := clientAuthorizedKey.Marshal()
	cfg := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-devm-jump",
		MaxAuthTries:  1,
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(k.Marshal(), wantMarshal) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("sshjump: unauthorized key")
		},
	}
	cfg.AddHostKey(hostKey)
	return &JumpServer{cfg: cfg, allow: allow, projectID: projectID}
}

// ServeConn runs one accepted raw conn through the SSH handshake and
// channel dispatch loop. Blocks until the SSH connection closes, then
// closes the underlying conn. Safe to call in a fresh goroutine per
// accept.
func (s *JumpServer) ServeConn(raw net.Conn) {
	defer raw.Close()
	sc, chans, reqs, err := ssh.NewServerConn(raw, s.cfg)
	if err != nil {
		// Auth failure or protocol error. x/crypto/ssh has already
		// closed the transport; nothing to do here.
		return
	}
	defer sc.Close()
	// Global requests (tcpip-forward etc.) are not supported. Draining
	// the channel is mandatory — x/crypto/ssh otherwise stalls on an
	// unread request.
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		s.handleChannel(newCh)
	}
}

func (s *JumpServer) handleChannel(newCh ssh.NewChannel) {
	if newCh.ChannelType() != "direct-tcpip" {
		_ = newCh.Reject(ssh.Prohibited, "only direct-tcpip permitted")
		return
	}
	var x directTCPExtra
	if err := ssh.Unmarshal(newCh.ExtraData(), &x); err != nil {
		_ = newCh.Reject(ssh.ConnectionFailed, "bad direct-tcpip payload")
		return
	}
	if !s.allow.Contains(x.DstHost, int(x.DstPort)) {
		jumpAcceptLog.Logf(s.projectID+"|"+x.DstHost+"|"+strconv.Itoa(int(x.DstPort)),
			"sshjump: denied %s:%d project=%s", x.DstHost, x.DstPort, s.projectID)
		_ = newCh.Reject(ssh.Prohibited, "destination not allowed")
		return
	}
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(x.DstHost, strconv.Itoa(int(x.DstPort))), 10*time.Second)
	if err != nil {
		daemonlog.Errorf("sshjump: dial %s:%d project=%s: %v", x.DstHost, x.DstPort, s.projectID, err)
		_ = newCh.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, inReqs, err := newCh.Accept()
	if err != nil {
		_ = upstream.Close()
		return
	}
	// Per-channel request drain is mandatory — same deadlock shape as
	// the global reqs chan above.
	go ssh.DiscardRequests(inReqs)
	// Bidirectional splice with half-close on each direction. Same
	// shape as spliceConns in hairpin_sni.go, inlined because ch is an
	// ssh.Channel (not net.Conn) so the TCPConn.CloseWrite assertion
	// over there does not apply.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, ch)
		if tc, ok := upstream.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(ch, upstream)
		_ = ch.CloseWrite()
	}()
	wg.Wait()
	_ = ch.Close()
	_ = upstream.Close()
}
