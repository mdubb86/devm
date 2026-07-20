// Package helper is the daemon-side client for the devm root
// helper (cmd/devm-helper).
//
// The helper runs as root, provisions lo0 aliases at boot, and serves
// bind requests over a UDS. Clients call BindTCP with an IP in the
// identity's pool (see identity.Config.PoolStart/PoolEnd) and a port;
// the helper binds the socket and returns the FD via SCM_RIGHTS. This
// lets the user-mode devm daemon bind low ports (:22, :80, :443) on
// per-project loopback IPs without holding root itself.
package helper

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"syscall"

	"github.com/mdubb86/devm/internal/identity"
)

// Client dials one specific helper UDS. Constructed per-identity
// (NewClient(cfg)) so a prod daemon dials the prod helper socket and
// an e2e daemon dials the e2e helper socket — never the other's.
type Client struct {
	socketPath string
}

// NewClient builds a Client bound to cfg's helper socket
// (cfg.HelperSocketPath).
func NewClient(cfg identity.Config) *Client {
	return &Client{socketPath: cfg.HelperSocketPath}
}

// BindTCP requests the helper to bind a TCP listening socket on
// ip:port and returns it as a net.Listener. ip must be in the
// identity's pool (see identity.Config.PoolStart/PoolEnd).
func (c *Client) BindTCP(ip string, port int) (net.Listener, error) {
	return bindTCPViaSock(c.socketPath, ip, port)
}

// bindTCPViaSock is the seam BindTCP uses in tests to point at a mock
// helper socket.
func bindTCPViaSock(sockPath, ip string, port int) (net.Listener, error) {
	uc, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("dial helper: %w", err)
	}
	defer uc.Close()
	unixConn := uc.(*net.UnixConn)

	req, _ := json.Marshal(struct {
		Op    string `json:"op"`
		IP    string `json:"ip"`
		Port  int    `json:"port"`
		Proto string `json:"proto"`
	}{Op: "bind", IP: ip, Port: port, Proto: "tcp"})
	// The helper reads requests with bufio.ReadBytes('\n'); without the
	// trailing newline it blocks forever waiting for a delimiter.
	if _, err := unixConn.Write(append(req, '\n')); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	buf := make([]byte, 4096)
	oob := make([]byte, 4096)
	n, oobn, _, _, err := unixConn.ReadMsgUnix(buf, oob)
	if err != nil {
		return nil, fmt.Errorf("read reply: %w", err)
	}

	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		return nil, fmt.Errorf("decode reply: %w", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("helper: %s", resp.Error)
	}

	msgs, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) == 0 {
		return nil, fmt.Errorf("parse SCM: %w", err)
	}
	fds, err := syscall.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) == 0 {
		return nil, fmt.Errorf("parse FDs: %w", err)
	}
	f := os.NewFile(uintptr(fds[0]), fmt.Sprintf("helper:%s:%d", ip, port))
	ln, err := net.FileListener(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("file listener: %w", err)
	}
	// net.FileListener dup'd the FD; close ours.
	f.Close()
	return ln, nil
}
