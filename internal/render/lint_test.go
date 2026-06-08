package render

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression for the bug found on 2026-06-05 dogfood: wildcard
// allowed_domains rendered unquoted and the kit loader rejected
// spec.yaml with "did not find expected alphabetic or numeric character"
// because YAML reads a leading '*' as an alias reference.
func TestLintRenderedSpecAcceptsWildcardDomain(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{ID: "test", SandboxName: "test", HostnameApex: "test.local", PortOffset: 51000},
		Network: schema.Network{AllowedDomains: []string{"*.openai.com", "github.com"}},
	}
	require.NoError(t, LintRenderedSpec(cfg, "/tmp/test"))
}

func TestSpecYAMLQuotesWildcardDomain(t *testing.T) {
	// Belt-and-suspenders: assert the rendered output actually contains
	// the quoted form. A future refactor of LintRenderedSpec couldn't
	// silently regress this.
	cfg := schema.Config{
		Project: schema.Project{ID: "test", SandboxName: "test", HostnameApex: "test.local", PortOffset: 51000},
		Network: schema.Network{AllowedDomains: []string{"*.openai.com"}},
	}
	out := SpecYAML(cfg, "/tmp/test")
	assert.Contains(t, out, "'*.openai.com'", "wildcard domain must be quoted in rendered spec")
}
