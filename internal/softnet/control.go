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
// applyControl returns a reply payload for ops that ack (setExposeMap
// answers with an ExposeAck line; every other op replies nil — their
// senders close the connection without reading).
func applyControl(e *egress, ing *ingress, m ControlMsg, shutdown func()) ([]byte, error) {
	switch m.Op {
	case "setPolicy":
		p, err := ParsePolicy(m.Policy)
		if err != nil {
			return nil, err
		}
		e.setPolicy(p, m.ForwardTargets)
		logf("control setPolicy policy=%s forward_targets=%v", p, m.ForwardTargets != nil)
		return nil, nil
	case "setExposeMap":
		results := ing.apply(m.Expose)
		ack := ExposeAck{OK: true, Results: results}
		for _, r := range results {
			if !r.OK {
				ack.OK = false
			}
		}
		logf("control setExposeMap ports=%d ok=%v", len(m.Expose), ack.OK)
		reply, err := json.Marshal(ack)
		if err != nil {
			return nil, err
		}
		return reply, nil
	case "setTestHosts":
		e.setDirectTestHosts(m.DirectTestHosts)
		logf("control setTestHosts hosts=%d", len(m.DirectTestHosts))
		return nil, nil
	case "shutdown":
		logf("control shutdown")
		if shutdown != nil {
			shutdown()
		}
		return nil, nil
	default:
		logf("control unknown op=%q ignored", m.Op)
		return nil, nil // unknown ops are ignored, not fatal
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
					reply, err := applyControl(e, ing, m, shutdown)
					if err != nil {
						logf("control apply %s: %v", m.Op, err)
					}
					if reply != nil {
						if _, err := c.Write(append(reply, '\n')); err != nil {
							logf("control reply %s: %v", m.Op, err)
						}
					}
				}
			}(conn)
		}
	}()
	return ln, nil
}
