package render

import (
	"fmt"

	"github.com/mdubb86/devm/internal/schema"
)

// Defaults used when the corresponding schema field is nil. Expressed
// as percentages so an undersized / resized guest still gets a sane
// value — the setup script resolves the percent against the current
// RAM / disk at install time.
const (
	defaultSwap       = "50%"
	defaultBuildCache = "15%"
)

// RenderSizingConf returns the body of /etc/devm/sizing.conf: exactly
// two lines, swap= and build_cache=, both always present. The setup
// script's presence (not sizing.conf) gates whether build_cache= is
// acted on, so docker.enabled=false does not suppress the line.
func RenderSizingConf(c schema.Config) []byte {
	swap := defaultSwap
	if c.Swap != nil {
		swap = *c.Swap
	}
	buildCache := defaultBuildCache
	if c.Docker != nil && c.Docker.BuildCache != nil {
		buildCache = *c.Docker.BuildCache
	}
	return []byte(fmt.Sprintf("swap=%s\nbuild_cache=%s\n", swap, buildCache))
}
