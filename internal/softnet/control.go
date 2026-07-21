package softnet

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
)

// applyControl handles one decoded control message. shutdown is invoked for
// the "shutdown" op — the daemon's signal that this softnet process should
// exit now (see /vm/stop in internal/serviceapi/vm.go). softnet is a child
// `tart run --net-softnet` forks internally, invisible to the daemon's
// process supervisor, so this control message — not a process signal — is
// the reliable way the daemon reaches it at teardown.
func applyControl(e *egress, ing *ingress, m ControlMsg, shutdown func()) error {
	switch m.Op {
	case "setPolicy":
		p, err := ParsePolicy(m.Policy)
		if err != nil {
			return err
		}
		e.setPolicy(p, m.IronProxy)
		return nil
	case "setExposeMap":
		ing.apply(m.Expose)
		return nil
	case "shutdown":
		if shutdown != nil {
			shutdown()
		}
		return nil
	default:
		return nil // unknown ops are ignored, not fatal
	}
}

// serveControl listens on sockPath for newline-delimited JSON ControlMsgs and
// applies them. Returns a Closer that stops the listener. shutdown is
// threaded through to applyControl's "shutdown" op handler (see Run in
// softnet.go, which passes its own cancellation func so a shutdown message
// unblocks the accept loop the same way a SIGTERM does).
func serveControl(sockPath string, e *egress, ing *ingress, shutdown func()) (io.Closer, error) {
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("control listen %s: %w", sockPath, err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var m ControlMsg
					if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
						logf("control unmarshal: %v", err)
						continue
					}
					if err := applyControl(e, ing, m, shutdown); err != nil {
						logf("control apply %s: %v", m.Op, err)
					}
				}
			}(conn)
		}
	}()
	return ln, nil
}
