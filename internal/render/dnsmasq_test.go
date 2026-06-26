package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDnsmasqConfig_HasWildcardTestEntry(t *testing.T) {
	got := string(DnsmasqConfig())
	assert.Equal(t, "address=/test/127.0.0.1\n", got)
}

func TestDnsmasqConfig_ParsableForm(t *testing.T) {
	// Sanity: the output is a single non-empty line ending in \n.
	got := string(DnsmasqConfig())
	assert.True(t, strings.HasSuffix(got, "\n"))
	lines := strings.Split(strings.TrimSpace(got), "\n")
	assert.Len(t, lines, 1, "config is a single directive")
}
