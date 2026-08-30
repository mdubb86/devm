package serviceapi

import (
	"testing"
	"time"
)

func TestDenials_RecordAndSnapshot(t *testing.T) {
	d := NewDenials()
	t0 := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	d.Record("p1", "google.com", "/", "GET", t0)
	d.Record("p1", "google.com", "/", "GET", t0.Add(1*time.Second))
	d.Record("p1", "example.com", "/", "GET", t0.Add(2*time.Second))
	d.Record("p2", "github.com", "/", "GET", t0.Add(3*time.Second))

	got := d.Snapshot("p1")
	if len(got) != 2 {
		t.Fatalf("want 2 hosts in p1, got %d: %+v", len(got), got)
	}
	// Sorted count desc.
	if got[0].Host != "google.com" || got[0].Count != 2 {
		t.Errorf("want google.com=2 first, got %+v", got[0])
	}
	if got[1].Host != "example.com" || got[1].Count != 1 {
		t.Errorf("want example.com=1 second, got %+v", got[1])
	}
	if !got[0].FirstSeen.Equal(t0) {
		t.Errorf("firstSeen: want %v, got %v", t0, got[0].FirstSeen)
	}
	if !got[0].LastSeen.Equal(t0.Add(1 * time.Second)) {
		t.Errorf("lastSeen: want %v, got %v", t0.Add(1*time.Second), got[0].LastSeen)
	}

	// p2 is isolated.
	got2 := d.Snapshot("p2")
	if len(got2) != 1 || got2[0].Host != "github.com" {
		t.Errorf("p2 snapshot: %+v", got2)
	}
}

// TestDenials_RecordsPathSeparately pins that a reject at
// github.com/anthropics/... and a reject at github.com/other/... roll
// up as two distinct entries — same host, different actionable path
// to allowlist. Path-scoped rules (v0.18.0+) make each URL its own
// unit of denial.
func TestDenials_RecordsPathSeparately(t *testing.T) {
	d := NewDenials()
	t0 := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	d.Record("p1", "github.com", "/anthropics/foo", "GET", t0)
	d.Record("p1", "github.com", "/anthropics/foo", "GET", t0.Add(1*time.Second))
	d.Record("p1", "github.com", "/other/bar", "POST", t0.Add(2*time.Second))

	got := d.Snapshot("p1")
	if len(got) != 2 {
		t.Fatalf("want 2 (host, path) entries, got %d: %+v", len(got), got)
	}
	// Sorted count desc — the /anthropics/foo entry has count 2.
	if got[0].Host != "github.com" || got[0].Path != "/anthropics/foo" || got[0].Count != 2 {
		t.Errorf("want github.com/anthropics/foo count=2 first, got %+v", got[0])
	}
	if got[0].Method != "GET" {
		t.Errorf("method: want GET, got %q", got[0].Method)
	}
	if got[1].Host != "github.com" || got[1].Path != "/other/bar" || got[1].Count != 1 {
		t.Errorf("want github.com/other/bar count=1 second, got %+v", got[1])
	}
	if got[1].Method != "POST" {
		t.Errorf("method: want POST, got %q", got[1].Method)
	}
}

func TestDenials_ClearProject(t *testing.T) {
	d := NewDenials()
	now := time.Now().UTC()
	d.Record("p1", "google.com", "/", "GET", now)
	d.Record("p2", "example.com", "/", "GET", now)
	d.clearProject("p1")
	if snap := d.Snapshot("p1"); len(snap) != 0 {
		t.Errorf("p1 should be empty after clearProject, got %+v", snap)
	}
	if snap := d.Snapshot("p2"); len(snap) != 1 {
		t.Errorf("p2 should survive p1's clearProject, got %+v", snap)
	}
}
