package serviceapi

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/softnet"
)

func TestSetPolicyWire(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		line, _ := r.ReadString('\n')
		got <- line
	}()

	if err := newSoftnetClient(sock).setPolicy("ENFORCED", &Endpoint{HTTPS: "127.0.0.1:8443"}); err != nil {
		t.Fatal(err)
	}

	line := <-got
	if !strings.Contains(line, `"op":"setPolicy"`) || !strings.Contains(line, `"policy":"ENFORCED"`) || !strings.Contains(line, "127.0.0.1:8443") {
		t.Fatalf("bad wire: %s", line)
	}
}

func TestSetPolicyWireNilEndpoint(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		line, _ := r.ReadString('\n')
		got <- line
	}()

	if err := newSoftnetClient(sock).setPolicy("OPEN", nil); err != nil {
		t.Fatal(err)
	}

	line := <-got
	if !strings.Contains(line, `"op":"setPolicy"`) || !strings.Contains(line, `"policy":"OPEN"`) || strings.Contains(line, "iron_proxy") {
		t.Fatalf("bad wire (expected no iron_proxy key): %s", line)
	}
}

func TestSetExposeMapWire(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		line, _ := r.ReadString('\n')
		got <- line
		// Ack the push — setExposeMap reads one reply line.
		_, _ = c.Write([]byte(`{"ok":true,"results":[{"bind_ip":"127.0.0.1","host_port":2222,"guest_port":22,"ok":true}]}` + "\n"))
	}()

	ports := []softnet.ExposePort{{GuestPort: 22, BindIP: "127.0.0.1", HostPort: 2222}}
	if err := newSoftnetClient(sock).setExposeMap(ports); err != nil {
		t.Fatal(err)
	}

	line := <-got
	if !strings.Contains(line, `"op":"setExposeMap"`) || !strings.Contains(line, `"guest_port":22`) || !strings.Contains(line, `"host_port":2222`) {
		t.Fatalf("bad wire: %s", line)
	}
}

// A softnet that never acks (dead, wedged, or predating the ack
// protocol) must surface as a push error, not a silent success.
func TestSetExposeMap_NoAckErrors(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sn-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		r := bufio.NewReader(c)
		_, _ = r.ReadString('\n')
		c.Close() // read the push, reply with nothing
	}()

	err = newSoftnetClient(sock).setExposeMap([]softnet.ExposePort{{GuestPort: 22, BindIP: "127.0.0.1", HostPort: 2222}})
	if err == nil || !strings.Contains(err.Error(), "ack") {
		t.Fatalf("expected no-ack error, got %v", err)
	}
}

// A failed bind reported in the ack must surface as an error naming
// the endpoint that failed.
func TestSetExposeMap_BindFailureSurfacesError(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sn-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		_, _ = r.ReadString('\n')
		_, _ = c.Write([]byte(`{"ok":false,"results":[{"bind_ip":"127.42.0.2","host_port":22,"guest_port":22,"ok":false,"error":"address already in use"}]}` + "\n"))
	}()

	err = newSoftnetClient(sock).setExposeMap([]softnet.ExposePort{{GuestPort: 22, BindIP: "127.42.0.2", HostPort: 22}})
	if err == nil || !strings.Contains(err.Error(), "127.42.0.2:22") || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("expected bind-failure error naming the endpoint, got %v", err)
	}
}

func TestSetTestHostsWire(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		line, _ := r.ReadString('\n')
		got <- line
	}()

	if err := newSoftnetClient(sock).setTestHosts([]string{"db.test", "cache.test"}); err != nil {
		t.Fatal(err)
	}

	line := <-got
	if !strings.Contains(line, `"op":"setTestHosts"`) || !strings.Contains(line, `"direct_test_hosts":["db.test","cache.test"]`) {
		t.Fatalf("bad wire: %s", line)
	}
}

// TestEndpointDecodesIntoForwardTargets pins the wire contract between the
// daemon's Endpoint and softnet's ForwardTargets. They are separate structs in
// separate packages; only this round-trip keeps them honest.
func TestEndpointDecodesIntoForwardTargets(t *testing.T) {
	ep := &Endpoint{
		HTTP:       "127.0.0.1:1",
		HTTPS:      "127.0.0.1:2",
		DNS:        "127.0.0.1:3",
		NTP:        "127.0.0.1:4",
		GuestHTTP:  "127.0.0.1:5",
		GuestHTTPS: "127.0.0.1:6",
	}
	msg := map[string]any{"op": "setPolicy", "policy": "ENFORCED", "forward_targets": ep}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got softnet.ControlMsg
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ForwardTargets == nil {
		t.Fatal("forward_targets did not decode")
	}
	if got.ForwardTargets.GuestHTTP != ep.GuestHTTP || got.ForwardTargets.GuestHTTPS != ep.GuestHTTPS {
		t.Fatalf("guest targets = %q/%q want %q/%q",
			got.ForwardTargets.GuestHTTP, got.ForwardTargets.GuestHTTPS, ep.GuestHTTP, ep.GuestHTTPS)
	}
	if got.ForwardTargets.HTTPS != ep.HTTPS {
		t.Fatalf("HTTPS = %q want %q", got.ForwardTargets.HTTPS, ep.HTTPS)
	}
}

func TestSoftnetStore(t *testing.T) {
	s := &softnetStore{m: make(map[string]string)}

	if got := s.get("proj1"); got != "" {
		t.Fatalf("expected empty for unknown project, got %q", got)
	}

	s.put("proj1", "/tmp/proj1.sock")
	if got := s.get("proj1"); got != "/tmp/proj1.sock" {
		t.Fatalf("get after put: got %q", got)
	}

	s.del("proj1")
	if got := s.get("proj1"); got != "" {
		t.Fatalf("expected empty after del, got %q", got)
	}
}
