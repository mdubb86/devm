package serviceapi

import (
	"strings"
	"testing"
	"time"
)

func TestDenials_RecordAndSnapshot(t *testing.T) {
	d := NewDenials()
	t0 := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	d.Record("p1", "google.com", t0)
	d.Record("p1", "google.com", t0.Add(1*time.Second))
	d.Record("p1", "example.com", t0.Add(2*time.Second))
	d.Record("p2", "github.com", t0.Add(3*time.Second))

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

func TestDenials_Reset(t *testing.T) {
	d := NewDenials()
	now := time.Now().UTC()
	d.Record("p1", "google.com", now)
	d.Record("p2", "example.com", now)
	d.Reset("p1")
	if snap := d.Snapshot("p1"); len(snap) != 0 {
		t.Errorf("p1 should be empty after reset, got %+v", snap)
	}
	if snap := d.Snapshot("p2"); len(snap) != 1 {
		t.Errorf("p2 should survive p1 reset, got %+v", snap)
	}
}

func TestDenials_TapWriter_ParsesRejectLines(t *testing.T) {
	d := NewDenials()
	w := d.TapWriter("proj")

	// Real iron-proxy reject line from ~/Library/Logs/devm/*.log.
	reject := `{"time":"2026-07-03T09:18:23.105991-05:00","level":"WARN","msg":"request","audit":{"host":"google.com","method":"GET","path":"/","remote_addr":"192.168.64.4:55782","sni":"google.com","mode":"mitm","action":"reject","status_code":403,"duration_ms":0.007},"rejected_by":"allowlist"}` + "\n"
	if _, err := w.Write([]byte(reject)); err != nil {
		t.Fatalf("write reject: %v", err)
	}
	snap := d.Snapshot("proj")
	if len(snap) != 1 || snap[0].Host != "google.com" || snap[0].Count != 1 {
		t.Fatalf("want google.com=1, got %+v", snap)
	}
}

func TestDenials_TapWriter_IgnoresNonRejects(t *testing.T) {
	d := NewDenials()
	w := d.TapWriter("proj")

	// Every category the tap must ignore.
	lines := []string{
		`{"time":"2026-07-03T09:18:23Z","level":"INFO","msg":"request","audit":{"host":"deb.debian.org","action":"allow"}}`,
		`{"time":"2026-07-03T09:18:23Z","level":"INFO","msg":"http proxy starting","addr":":80"}`,
		`error: dns.proxy_ip is required`,
		`{"time":"2026-07-03T09:18:23Z","level":"INFO","msg":"request","audit":{"host":"","action":"reject"}}`, // empty host
		`not json at all`,
	}
	if _, err := w.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if snap := d.Snapshot("proj"); len(snap) != 0 {
		t.Errorf("want no denials from non-reject noise, got %+v", snap)
	}
}

func TestDenials_TapWriter_SplitsAcrossWrites(t *testing.T) {
	// pexec doesn't guarantee whole-line Writes — the tap must accumulate
	// a partial line and consume it once a newline arrives.
	d := NewDenials()
	w := d.TapWriter("proj")
	full := `{"time":"2026-07-03T09:18:23Z","level":"WARN","msg":"request","audit":{"host":"google.com","action":"reject"}}` + "\n"
	half := len(full) / 2
	if _, err := w.Write([]byte(full[:half])); err != nil {
		t.Fatalf("write half: %v", err)
	}
	if snap := d.Snapshot("proj"); len(snap) != 0 {
		t.Fatalf("partial write shouldn't record: %+v", snap)
	}
	if _, err := w.Write([]byte(full[half:])); err != nil {
		t.Fatalf("write rest: %v", err)
	}
	snap := d.Snapshot("proj")
	if len(snap) != 1 || snap[0].Host != "google.com" || snap[0].Count != 1 {
		t.Fatalf("want google.com=1 after split write, got %+v", snap)
	}
}
