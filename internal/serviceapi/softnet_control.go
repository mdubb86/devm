package serviceapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/mdubb86/devm/internal/softnet"
)

// Endpoint mirrors softnet's ForwardTargets exactly (op/policy on the
// envelope; the targets here) so a setPolicy message marshals into the wire
// format softnet's control listener expects.
type Endpoint struct {
	HTTP  string `json:"http"`
	HTTPS string `json:"https"`
	DNS   string `json:"dns"`
	NTP   string `json:"ntp"`

	GuestHTTP  string `json:"guest_http,omitempty"`
	GuestHTTPS string `json:"guest_https,omitempty"`

	// Pop is the Mac-side host:port softnet forwards guest TCP destined
	// for 192.168.127.1:81 to — the daemon's per-project pop listener.
	Pop string `json:"pop,omitempty"`
}

// softnetClient is the daemon-side handle to one VM's softnet control
// socket. softnet reads newline-delimited JSON control messages from this
// socket and applies them (setPolicy / setExposeMap) at its own euid.
type softnetClient struct {
	sock string
}

func newSoftnetClient(sock string) *softnetClient {
	return &softnetClient{sock: sock}
}

// dial connects to the control socket with a few retries, since softnet
// may still be starting up (it creates the listener early in its
// lifecycle, but the daemon can race it right after spawning the VM).
func (c *softnetClient) dial() (net.Conn, error) {
	var lastErr error
	for i := 0; i < 5; i++ {
		conn, err := net.DialTimeout("unix", c.sock, 2*time.Second)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("dial softnet control socket %s: %w", c.sock, lastErr)
}

// send writes msg as one JSON line and closes the connection. softnet's
// control listener reads one line per connection.
func (c *softnetClient) send(msg map[string]any) error {
	conn, err := c.dial()
	if err != nil {
		return err
	}
	defer conn.Close()

	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal control message: %w", err)
	}
	b = append(b, '\n')

	if _, err := conn.Write(b); err != nil {
		return fmt.Errorf("write softnet control message: %w", err)
	}
	return nil
}

// setPolicy tells softnet to switch its coarse egress policy. ep is the
// forward targets to forward to when pol is ENFORCED; nil for LOCKED/OPEN,
// in which case the forward_targets key is omitted entirely.
func (c *softnetClient) setPolicy(pol string, ep *Endpoint) error {
	msg := map[string]any{
		"op":     "setPolicy",
		"policy": pol,
	}
	if ep != nil {
		msg["forward_targets"] = ep
	}
	if err := c.send(msg); err != nil {
		return err
	}
	log.Printf("softnet-push: setPolicy sock=%s policy=%s forward_targets=%v", c.sock, pol, ep != nil)
	return nil
}

// setExposeMap tells softnet which host->guest ingress port mappings to
// forward, then reads softnet's per-port ack from the same connection.
// A missing ack (dead or wedged softnet — every current softnet replies)
// and any reported bind failure both surface as errors: a silently
// unbound :22 is a dead SSH endpoint behind a "successful" start, the
// exact failure shape of the orphaned-VM incident.
func (c *softnetClient) setExposeMap(ports []softnet.ExposePort) error {
	conn, err := c.dial()
	if err != nil {
		return err
	}
	defer conn.Close()

	b, err := json.Marshal(map[string]any{
		"op":     "setExposeMap",
		"expose": ports,
	})
	if err != nil {
		return fmt.Errorf("marshal control message: %w", err)
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write softnet control message: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("softnet sent no setExposeMap ack (sock=%s) — softnet is dead or predates the ack protocol; `devm stop` then start the VM to replace it: %w", c.sock, err)
	}
	var ack softnet.ExposeAck
	if err := json.Unmarshal(line, &ack); err != nil {
		return fmt.Errorf("decode setExposeMap ack: %w", err)
	}
	if !ack.OK {
		var failed []string
		for _, r := range ack.Results {
			if !r.OK {
				failed = append(failed, fmt.Sprintf("%s:%d -> guest:%d: %s", r.BindIP, r.HostPort, r.GuestPort, r.Error))
			}
		}
		return fmt.Errorf("softnet failed to bind ingress ports: %s", strings.Join(failed, "; "))
	}
	log.Printf("softnet-push: setExposeMap sock=%s ports=%d acked", c.sock, len(ports))
	return nil
}

// setTestHosts pushes the set of direct-service hostnames softnet's .test
// DNS answers with loopback. Replace-not-merge on the softnet side.
func (c *softnetClient) setTestHosts(hosts []string) error {
	if err := c.send(map[string]any{
		"op":                "setTestHosts",
		"direct_test_hosts": hosts,
	}); err != nil {
		return err
	}
	log.Printf("softnet-push: setTestHosts sock=%s hosts=%d", c.sock, len(hosts))
	return nil
}

// shutdown asks softnet to exit now. softnet is a child process `tart run
// --net-softnet` forks internally (see /vm/start's ensureSoftnetSymlink
// comment) — the daemon's supervisor only manages the `tart run` process
// itself, so a plain SIGTERM to that process doesn't reach softnet: pexec
// only escalates to a process-group-wide signal if `tart run` outlives a
// short grace window, and in practice it exits fast on SIGTERM, so softnet
// is never actually signaled and survives as an orphan holding the
// project's bound port. This control message is the reliable path; softnet
// applies it in applyControl (internal/softnet/control.go) by cancelling
// its own run context, which now (see softnet.go) also force-closes its
// vm-fd connection so a blocked read can't leave it hung after the guest
// has gone quiet.
func (c *softnetClient) shutdown() error {
	return c.send(map[string]any{"op": "shutdown"})
}

// softnetStore tracks each running project's softnet control socket path,
// mirroring projectInfoStore's mutex-guarded map pattern.
type softnetStore struct {
	mu sync.Mutex
	m  map[string]string
}

func newSoftnetStore() *softnetStore {
	return &softnetStore{m: make(map[string]string)}
}

func (s *softnetStore) put(projectID, sock string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[projectID] = sock
}

func (s *softnetStore) get(projectID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[projectID]
}

func (s *softnetStore) del(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, projectID)
}

var softnetState = newSoftnetStore()

// SetSoftnetControlSockForTest registers projectID's softnet control
// socket directly in the daemon's in-memory softnetState map, bypassing
// the normal /vm/start, /vm/apply-iron-proxy, and discoverSoftnet
// registration paths. Test-only seam: softnetState only being populated
// by those code paths is a real production contract (see expose.go's
// pushExposeMap, which now fails loud on an unregistered project — the
// adopt-in-place fix this seam exists to keep testable). It exists so
// tests outside this package that drive a real serviceapi.Server (e.g.
// internal/orchestrator's reconcile tests, via RunReconcile) can stand
// in for that registration without wiring up a full iron-proxy config
// + spawn just to satisfy pushExposeMap's fail-loud check.
func SetSoftnetControlSockForTest(projectID, sock string) {
	softnetState.put(projectID, sock)
}
