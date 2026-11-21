package serviceapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi/sshkeys"
)

type staticTartList struct{ vms []tart.VM }

func (s staticTartList) List(context.Context) ([]tart.VM, error) { return s.vms, nil }

// TestDetectOrphanVMs pins the evidence gate: a running VM counts as
// an orphan only when devm sidecar artifacts prove it's devm's
// (iron-proxy config or ssh project dir) AND no state snapshot exists.
// Arbitrary tart VMs (devm-base, hand-made experiments) never appear.
func TestDetectOrphanVMs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// tracked: snapshot present → not an orphan even with sidecars.
	require.NoError(t, WriteStateSnapshot(identity.Prod, "tracked", StateSnapshot{Cfg: schema.Config{}}))
	seedIronProxyConfig(t, "tracked")

	// lost-a: running, iron-proxy config, no snapshot → orphan.
	seedIronProxyConfig(t, "lost-a")

	// lost-b: running, ssh project dir, no snapshot → orphan.
	_, err := sshkeys.EnsureProjectKeypair(identity.Prod, "lost-b")
	require.NoError(t, err)

	// devm-base: running, no sidecars → not devm's problem.
	tr := staticTartList{vms: []tart.VM{
		{Name: "tracked", Running: true},
		{Name: "lost-a", Running: true},
		{Name: "lost-b", Running: true},
		{Name: "devm-base", Running: true},
		{Name: "lost-stopped", Running: false}, // sidecars but not running → nothing to surface
	}}
	seedIronProxyConfig(t, "lost-stopped")

	orphans, err := detectOrphanVMs(context.Background(), identity.Prod, tr)
	require.NoError(t, err)
	assert.Equal(t, []string{"lost-a", "lost-b"}, orphans)
}

func seedIronProxyConfig(t *testing.T, projectID string) {
	t.Helper()
	path, err := IronProxyConfigPath(identity.Prod, projectID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("stub\n"), 0o600))
}
