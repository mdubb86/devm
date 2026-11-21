package render

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestRenderSizingConf_Defaults(t *testing.T) {
	got := RenderSizingConf(schema.Config{})
	assert.Equal(t, "swap=50%\nbuild_cache=15%\n", string(got))
}

func TestRenderSizingConf_SwapAbsolute(t *testing.T) {
	swap := "16G"
	got := RenderSizingConf(schema.Config{Swap: &swap})
	assert.Equal(t, "swap=16G\nbuild_cache=15%\n", string(got))
}

func TestRenderSizingConf_BuildCacheAbsolute(t *testing.T) {
	bc := "6G"
	got := RenderSizingConf(schema.Config{
		Docker: &schema.DockerConfig{Enabled: true, BuildCache: &bc},
	})
	assert.Equal(t, "swap=50%\nbuild_cache=6G\n", string(got))
}

// TestRenderSizingConf_IncludesBothLinesRegardless pins that docker.
// enabled=false does NOT suppress the build_cache= line. The setup
// script's presence (not sizing.conf) gates whether the directive is
// acted on, so sizing.conf always carries both lines.
func TestRenderSizingConf_IncludesBothLinesRegardless(t *testing.T) {
	bc := "6G"
	got := RenderSizingConf(schema.Config{
		Docker: &schema.DockerConfig{Enabled: false, BuildCache: &bc},
	})
	assert.Equal(t, "swap=50%\nbuild_cache=6G\n", string(got))
}
