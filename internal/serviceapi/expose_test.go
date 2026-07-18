package serviceapi

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/softnet"
)

func TestComputeExposeMap_ServicesAndSSH(t *testing.T) {
	cfg := schema.Config{Services: map[string]schema.Service{
		"db":     {Port: 5432, Direct: true, Hostname: "db.test", BindIP: ""},
		"web":    {Port: 3000, Hostname: "web.test"},
		"noport": {}, // masks/exec-only service: no port -> not exposed
	}}
	got := computeExposeMap(cfg, 2200)

	// Expect one entry per service WITH a port, plus SSH.
	byGuest := map[int]softnet.ExposePort{}
	for _, p := range got {
		byGuest[p.GuestPort] = p
	}
	if len(got) != 3 {
		t.Fatalf("want 3 expose ports (db, web, ssh), got %d: %+v", len(got), got)
	}
	if p := byGuest[5432]; p.HostPort != 5432 || p.BindIP != "127.0.0.1" {
		t.Errorf("db: want host 5432 bind 127.0.0.1, got %+v", p)
	}
	if p := byGuest[3000]; p.HostPort != 3000 || p.BindIP != "127.0.0.1" {
		t.Errorf("web: want host 3000 bind 127.0.0.1, got %+v", p)
	}
	if p := byGuest[22]; p.HostPort != 2200 || p.BindIP != softnet.HostLoopIP {
		t.Errorf("ssh: want host 2200 bind loopback, got %+v", p)
	}
}

func TestComputeExposeMap_NoSSHWhenZero(t *testing.T) {
	cfg := schema.Config{Services: map[string]schema.Service{
		"db": {Port: 5432, Direct: true, Hostname: "db.test"},
	}}
	got := computeExposeMap(cfg, 0)
	for _, p := range got {
		if p.GuestPort == 22 {
			t.Fatalf("ssh port must be omitted when sshHostPort==0: %+v", got)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want 1 (db only), got %d: %+v", len(got), got)
	}
}

func TestComputeExposeMap_BindIPHonored(t *testing.T) {
	cfg := schema.Config{Services: map[string]schema.Service{
		"db": {Port: 5432, Direct: true, Hostname: "db.test", BindIP: "0.0.0.0"},
	}}
	got := computeExposeMap(cfg, 0)
	if len(got) != 1 || got[0].BindIP != "0.0.0.0" {
		t.Fatalf("want bind 0.0.0.0 honored, got %+v", got)
	}
}
