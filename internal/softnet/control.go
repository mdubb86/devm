package softnet

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
)

func applyControl(e *egress, ing *ingress, m ControlMsg) error {
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
	default:
		return nil // unknown ops are ignored, not fatal
	}
}

// serveControl listens on sockPath for newline-delimited JSON ControlMsgs and
// applies them. Returns a Closer that stops the listener.
func serveControl(sockPath string, e *egress, ing *ingress) (io.Closer, error) {
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
					if err := applyControl(e, ing, m); err != nil {
						logf("control apply %s: %v", m.Op, err)
					}
				}
			}(conn)
		}
	}()
	return ln, nil
}
