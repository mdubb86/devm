package serviceapi

import (
	"bufio"
	"net"
	"path/filepath"
	"strings"
	"testing"

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
