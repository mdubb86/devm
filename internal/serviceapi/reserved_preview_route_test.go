package serviceapi

import (
	"strings"
	"testing"
)

func TestReservedPreviewRoute_ShapesCorrectly(t *testing.T) {
	r := reservedPreviewRoute("sewtrue", "10.0.0.5", "e2e.test")
	if r.Hostname != "preview.sewtrue.e2e.test" {
		t.Fatalf("hostname = %q", r.Hostname)
	}
	if r.BackendHost != "10.0.0.5" {
		t.Fatalf("backend host = %q", r.BackendHost)
	}
	if r.BackendPort != previewServePort {
		t.Fatalf("backend port = %d, want %d", r.BackendPort, previewServePort)
	}
	if r.Mode != ModeVM {
		t.Fatalf("mode = %v, want ModeVM", r.Mode)
	}
	if r.Project != "sewtrue" {
		t.Fatalf("project = %q", r.Project)
	}
}

func TestIsReservedPreviewHostname(t *testing.T) {
	cases := []struct {
		hostname, projectID, tld string
		want                     bool
	}{
		{"preview.sewtrue.test", "sewtrue", "test", true},
		{"preview.sewtrue.e2e.test", "sewtrue", "e2e.test", true},
		{"preview.other.test", "sewtrue", "test", false},
		{"files.sewtrue.test", "sewtrue", "test", false},
		{"preview.sewtrue.other", "sewtrue", "test", false},
		{"", "sewtrue", "test", false},
	}
	for _, c := range cases {
		got := IsReservedPreviewHostname(c.hostname, c.projectID, c.tld)
		if got != c.want {
			t.Errorf("IsReservedPreviewHostname(%q, %q, %q) = %v, want %v",
				c.hostname, c.projectID, c.tld, got, c.want)
		}
	}
}

func TestFormatReservedPreviewCollisionError_MessageHints(t *testing.T) {
	err := FormatReservedPreviewCollisionError("preview.sewtrue.test")
	msg := err.Error()
	if !strings.Contains(msg, `"preview.sewtrue.test"`) {
		t.Fatalf("err must name the colliding hostname: %q", msg)
	}
	if !strings.Contains(msg, "reserved for devm's bundled preview server") {
		t.Fatalf("err must mention the preview service: %q", msg)
	}
}
