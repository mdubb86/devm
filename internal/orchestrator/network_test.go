package orchestrator

import (
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconcileNetwork_AppliesAllDomains(t *testing.T) {
	r := &stubRunner{}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	cfg := schema.Config{Network: schema.Network{
		AllowedDomains: []string{"github.com", "api.anthropic.com"},
	}}

	err := ReconcileNetworkWithRunner(sb, cfg, r)
	require.NoError(t, err)
	require.Len(t, r.lastArgs, 2, "one sbx policy allow per domain")
	// Sorted: api.anthropic.com before github.com.
	assert.Contains(t, strings.Join(r.lastArgs[0], " "), "sbx policy allow network x api.anthropic.com")
	assert.Contains(t, strings.Join(r.lastArgs[1], " "), "sbx policy allow network x github.com")
}

func TestReconcileNetwork_NoOpForEmptyConfig(t *testing.T) {
	r := &stubRunner{}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}

	err := ReconcileNetworkWithRunner(sb, schema.Config{}, r)
	require.NoError(t, err)
	assert.Empty(t, r.lastArgs, "no domains -> no sbx calls")
}

func TestReconcileNetwork_StopsOnFirstError(t *testing.T) {
	r := &stubRunner{runErr: assertErr("boom")}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	cfg := schema.Config{Network: schema.Network{
		AllowedDomains: []string{"a.com", "b.com"},
	}}

	err := ReconcileNetworkWithRunner(sb, cfg, r)
	require.Error(t, err)
	assert.Len(t, r.lastArgs, 1, "must abort after first failure, not all-N")
}

// Wildcard pin: the deleted lint_test.go protected the kit-yaml
// quoting path. With network policies now applied at runtime via
// exec.Command (no shell), the risk is that someone could wrap the
// argument or mishandle the wildcard. These tests pin that the
// wildcard reaches the sbx CLI as a single literal argument.
func TestApplyNetworkAllow_WildcardPassesVerbatim(t *testing.T) {
	r := &stubRunner{}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}

	err := applyNetworkAllow(r, sb, "*.openai.com")
	require.NoError(t, err)
	require.Len(t, r.lastArgs, 1)
	// The wildcard must appear as a single argv element, not split or
	// shell-escaped. Joining with spaces is just for readable assertion.
	assert.Equal(t, []string{"sbx", "policy", "allow", "network", "x", "*.openai.com"}, r.lastArgs[0])
}

func TestApplyNetworkRm_WildcardPassesVerbatim(t *testing.T) {
	r := &stubRunner{}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}

	err := applyNetworkRm(r, sb, "*.openai.com")
	require.NoError(t, err)
	require.Len(t, r.lastArgs, 1)
	assert.Equal(t, []string{"sbx", "policy", "rm", "network", "x", "--resource", "*.openai.com"}, r.lastArgs[0])
}

// assertErr is a small helper so we can produce a *recognizable* error
// from the stubRunner without pulling in pkg/errors. Keeps the test
// readable without depending on error-string matching.
type assertErr string

func (e assertErr) Error() string { return string(e) }
