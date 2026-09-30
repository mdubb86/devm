package render

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRenderGdevmServeUnit_ProducesSystemdService pins that the
// rendered unit file has the shape systemd expects and points at the
// gdevm binary path we expect inside the guest.
func TestRenderGdevmServeUnit_ProducesSystemdService(t *testing.T) {
	got := string(RenderGdevmServeUnit())
	assert.Contains(t, got, "[Unit]")
	assert.Contains(t, got, "[Service]")
	assert.Contains(t, got, "ExecStart=/usr/local/bin/gdevm serve")
	assert.Contains(t, got, "Restart=always")
	assert.Contains(t, got, "RestartSec=1")
	assert.Contains(t, got, "[Install]")
	assert.Contains(t, got, "WantedBy=multi-user.target")
}

// TestRenderGdevmServeUnit_Deterministic guards against accidental
// non-determinism (e.g. map iteration) creeping into a function that
// bundle fingerprinting relies on being stable across builds of the
// same source.
func TestRenderGdevmServeUnit_Deterministic(t *testing.T) {
	a := RenderGdevmServeUnit()
	b := RenderGdevmServeUnit()
	assert.Equal(t, a, b)
}
