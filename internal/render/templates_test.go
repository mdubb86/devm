package render

import (
	"testing"

	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderTemplates_Empty(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{ID: "x", SandboxName: "x", HostnameApex: "x.local"},
	}
	got, err := RenderTemplates(cfg, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, got)
}
