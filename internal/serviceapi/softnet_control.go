package serviceapi

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/mdubb86/devm/internal/softnet"
)

// Endpoint mirrors softnet's IronProxyEndpoint exactly (op/policy on the
// envelope; http/https/dns/ntp here) so a setPolicy message marshals into
// the wire format softnet's control listener expects.
type Endpoint struct {
	HTTP  string `json:"http"`
	HTTPS string `json:"https"`
	DNS   string `json:"dns"`
	NTP   string `json:"ntp"`
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
// iron-proxy endpoint to forward to when pol is ENFORCED; nil for
// LOCKED/OPEN, in which case the iron_proxy key is omitted entirely.
func (c *softnetClient) setPolicy(pol string, ep *Endpoint) error {
	msg := map[string]any{
		"op":     "setPolicy",
		"policy": pol,
	}
	if ep != nil {
		msg["iron_proxy"] = ep
	}
	return c.send(msg)
}

// setExposeMap tells softnet which host->guest ingress port mappings to
// forward. Used by Plan 4's `devm expose`.
func (c *softnetClient) setExposeMap(ports []softnet.ExposePort) error {
	msg := map[string]any{
		"op":     "setExposeMap",
		"expose": ports,
	}
	return c.send(msg)
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
