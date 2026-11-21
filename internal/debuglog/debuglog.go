// Package debuglog is a tiny, category-gated debug logger.
//
// Usage:
//
//	debuglog.Logf("shell", "anchor started: pid=%d", pid)
//
// Output (when enabled) goes to stderr with a category-prefixed,
// timestamped line:
//
//	[devm-shell 15:04:05.123] anchor started: pid=12345
//
// Enable via the DEVM_DEBUG env var:
//
//	DEVM_DEBUG=1          → all categories
//	DEVM_DEBUG=all        → same
//	DEVM_DEBUG=shell      → only "shell"
//	DEVM_DEBUG=shell,ports → "shell" and "ports"
//
// When disabled, calls are essentially a single map lookup + early
// return; cheap enough to leave in production code paths.
package debuglog

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	once    sync.Once
	enabled map[string]bool
)

func init() {
	parse()
}

func parse() {
	once.Do(func() {
		v := strings.TrimSpace(os.Getenv("DEVM_DEBUG"))
		if v == "" {
			return
		}
		if v == "1" || v == "all" {
			enabled = map[string]bool{"*": true}
			return
		}
		enabled = make(map[string]bool)
		for _, c := range strings.Split(v, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				enabled[c] = true
			}
		}
	})
}

// IsEnabled reports whether the named category is enabled.
func IsEnabled(category string) bool {
	if enabled == nil {
		return false
	}
	return enabled["*"] || enabled[category]
}

// Logf writes a debug line to stderr if `category` is enabled via
// DEVM_DEBUG. Format is the same as fmt.Sprintf. A timestamp and
// `[devm-<category>]` prefix are added automatically.
func Logf(category, format string, args ...any) {
	if !IsEnabled(category) {
		return
	}
	fmt.Fprintf(os.Stderr, "[devm-%s %s] %s\n",
		category,
		time.Now().Format("15:04:05.000"),
		fmt.Sprintf(format, args...))
}
