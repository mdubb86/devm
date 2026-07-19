// Package portbinder is the daemon-side client for the devm root
// port-binder helper (cmd/devm-portbinder).
//
// The helper runs as root, provisions lo0 aliases at boot, and serves
// bind requests over a UDS. Clients call BindTCP with a devm pool IP
// (127.42.0.1..20) and a port; the helper binds the socket and returns
// the FD via SCM_RIGHTS. This lets the user-mode devm daemon bind low
// ports (:22, :80, :443) on per-project loopback IPs without holding
// root itself.
package portbinder

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"syscall"
)

// SocketPath is the well-known UDS the portbinder helper listens on.
// Installed at devm install time; see cmd/devm/service.go. A var (not
// a const) so tests can point BindTCP at a mock helper socket instead
// of the real root-owned one.
var SocketPath = "/var/run/devm-portbinder.sock"

// BindTCP requests the portbinder helper to bind a TCP listening socket
// on ip:port and returns it as a net.Listener. ip must be in the devm
// pool (127.42.0.1..20).
func BindTCP(ip string, port int) (net.Listener, error) {
	return bindTCPViaSock(SocketPath, ip, port)
}

// bindTCPViaSock is the seam BindTCP uses in tests to point at a mock
// helper socket.
func bindTCPViaSock(sockPath, ip string, port int) (net.Listener, error) {
	uc, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("dial portbinder: %w", err)
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
		return nil, fmt.Errorf("portbinder: %s", resp.Error)
	}

	msgs, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) == 0 {
		return nil, fmt.Errorf("parse SCM: %w", err)
	}
	fds, err := syscall.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) == 0 {
		return nil, fmt.Errorf("parse FDs: %w", err)
	}
	f := os.NewFile(uintptr(fds[0]), fmt.Sprintf("portbinder:%s:%d", ip, port))
	ln, err := net.FileListener(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("file listener: %w", err)
	}
	// net.FileListener dup'd the FD; close ours.
	f.Close()
	return ln, nil
}
