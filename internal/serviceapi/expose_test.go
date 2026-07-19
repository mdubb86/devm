package serviceapi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/softnet"
)

func TestComputeExposeMap_ServicesAndSSH(t *testing.T) {
	cfg := schema.Config{Services: map[string]schema.Service{
		"db":     {Port: 5432, Direct: true, Hostname: "db.test", BindIP: ""},
		"web":    {Port: 3000, Hostname: "web.test"},
		"noport": {}, // masks/exec-only service: no port -> not exposed
	}}
	got := computeExposeMap(cfg, "127.42.0.1")

	// Expect one entry per service WITH a port, plus SSH.
	byGuest := map[int]softnet.ExposePort{}
	for _, p := range got {
		byGuest[p.GuestPort] = p
	}
	if len(got) != 3 {
		t.Fatalf("want 3 expose ports (db, web, ssh), got %d: %+v", len(got), got)
	}
	if p := byGuest[5432]; p.HostPort != 5432 || p.BindIP != "127.42.0.1" {
		t.Errorf("db: want host 5432 bind 127.42.0.1, got %+v", p)
	}
	if p := byGuest[3000]; p.HostPort != 3000 || p.BindIP != "127.42.0.1" {
		t.Errorf("web: want host 3000 bind 127.42.0.1, got %+v", p)
	}
	if p := byGuest[22]; p.HostPort != 22 || p.BindIP != "127.42.0.1" {
		t.Errorf("ssh: want host 22 bind 127.42.0.1, got %+v", p)
	}
}

func TestComputeExposeMap_SSHAlwaysPresent(t *testing.T) {
	cfg := schema.Config{Services: map[string]schema.Service{
		"db": {Port: 5432, Direct: true, Hostname: "db.test"},
	}}
	got := computeExposeMap(cfg, "127.42.0.3")
	haveSSH := false
	for _, p := range got {
		if p.GuestPort == 22 {
			haveSSH = true
			if p.HostPort != 22 || p.BindIP != "127.42.0.3" {
				t.Fatalf("ssh entry wrong shape: %+v", p)
			}
		}
	}
	if !haveSSH {
		t.Fatalf("ssh must always be present, got %+v", got)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 (db + ssh), got %d: %+v", len(got), got)
	}
}

func TestComputeExposeMap_BindsProjectIP(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "myapp"},
		Services: map[string]schema.Service{
			"api": {Port: 3000, Hostname: "api.myapp.test"},
			"db":  {Port: 5432, Hostname: "db.myapp.test", Direct: true},
		},
	}
	ports := computeExposeMap(cfg, "127.42.0.1")
	require.Len(t, ports, 3) // api, db, ssh
	for _, p := range ports {
		assert.Equal(t, "127.42.0.1", p.BindIP, "bind IP for %d", p.GuestPort)
	}
	// SSH must be present at :22.
	haveSSH := false
	for _, p := range ports {
		if p.GuestPort == 22 && p.HostPort == 22 {
			haveSSH = true
			break
		}
	}
	assert.True(t, haveSSH, "expected an SSH entry on :22")
}

func TestComputeExposeMap_BindIPFieldIgnoredNowUsesProjectIP(t *testing.T) {
	// V1 non-goal: per-service bind_ip (LAN exposure) is parsed but
	// ignored — every entry binds on the project's allocated IP
	// regardless of what the service declares.
	cfg := schema.Config{Services: map[string]schema.Service{
		"db": {Port: 5432, Direct: true, Hostname: "db.test", BindIP: "0.0.0.0"},
	}}
	got := computeExposeMap(cfg, "127.42.0.2")
	for _, p := range got {
		if p.GuestPort == 5432 {
			assert.Equal(t, "127.42.0.2", p.BindIP)
		}
	}
}

// TestPushExposeMap_ConflictRefusesDispatch pins the fail-loud contract:
// when a second project's expose map collides with a first project's
// already-claimed host port, pushExposeMap must return the conflict
// error WITHOUT dispatching to softnet — so the colliding listener is
// never bound and the project can never silently misroute another
// project's hostname. Project "conflict-b" is given a socket path that
// doesn't exist; if pushExposeMap dialed it, the error returned would be
// a dial/connect failure, not the portClaims conflict message.
func TestPushExposeMap_ConflictRefusesDispatch(t *testing.T) {
	t.Cleanup(func() {
		exposeClaims.release("conflict-a")
		exposeClaims.release("conflict-b")
		softnetState.del("conflict-b")
	})

	ports := []softnet.ExposePort{{GuestPort: 5432, BindIP: "127.0.0.1", HostPort: 15432}}

	if err := pushExposeMap("conflict-a", ports); err != nil {
		t.Fatalf("first project's claim must succeed: %v", err)
	}

	softnetState.put("conflict-b", "/nonexistent/softnet.sock")
	err := pushExposeMap("conflict-b", ports)
	if err == nil {
		t.Fatal("want conflict error when project b's map collides with a's claimed port, got nil")
	}
	if got := err.Error(); !containsAll(got, "127.0.0.1:15432", "conflict-a") {
		t.Fatalf("want conflict error naming the key and owning project, got: %v", got)
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
