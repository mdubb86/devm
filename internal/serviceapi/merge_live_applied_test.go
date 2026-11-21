package serviceapi

import (
	"testing"

	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeLiveApplied_Cron(t *testing.T) {
	cases := []struct {
		name    string
		old     map[string]string
		new     map[string]string
		applied reconcile.Change
	}{
		{
			name:    "add",
			old:     map[string]string{"a": "0 1 * * *"},
			new:     map[string]string{"a": "0 1 * * *", "b": "0 2 * * *"},
			applied: reconcile.Change{Kind: reconcile.KindCronAdd, Key: "b", New: "0 2 * * *"},
		},
		{
			name:    "remove",
			old:     map[string]string{"a": "0 1 * * *", "b": "0 2 * * *"},
			new:     map[string]string{"a": "0 1 * * *"},
			applied: reconcile.Change{Kind: reconcile.KindCronRemove, Key: "b", Old: "0 2 * * *"},
		},
		{
			name:    "change",
			old:     map[string]string{"a": "0 1 * * *"},
			new:     map[string]string{"a": "0 5 * * *"},
			applied: reconcile.Change{Kind: reconcile.KindCronChange, Key: "a", Old: "0 1 * * *", New: "0 5 * * *"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := schema.Config{Project: schema.Project{Name: "p"}, Cron: tc.old}
			newCfg := schema.Config{
				Project: schema.Project{Name: "p"},
				Cron:    tc.new,
				// Teardown-bucket change pending alongside; not applied live.
				Docker: &schema.DockerConfig{Enabled: true},
			}

			merged := mergeLiveApplied(old, newCfg, []reconcile.Change{tc.applied})

			assert.Equal(t, tc.new, merged.Cron)
			assert.False(t, merged.DockerEnabled(), "pending teardown field must keep its old value")
		})
	}
}

// liveKindMerge records how mergeLiveApplied treats each BucketLive
// kind. An empty-snapshot entry means the change never alters the
// recorded config, so the merge has nothing to copy.
var liveKindMerge = map[reconcile.ChangeKind]string{
	reconcile.KindPortAdd:                      "services.<svc>.Port/BindIP",
	reconcile.KindPortRemove:                   "services.<svc>.Port/BindIP",
	reconcile.KindPortChange:                   "services.<svc>.Port/BindIP",
	reconcile.KindNetworkAdd:                   "Network",
	reconcile.KindNetworkRemove:                "Network",
	reconcile.KindEnvAdd:                       "Env or services.<svc>.Env",
	reconcile.KindEnvRemove:                    "Env or services.<svc>.Env",
	reconcile.KindEnvChange:                    "Env or services.<svc>.Env",
	reconcile.KindPackageAdd:                   "Packages",
	reconcile.KindPackageRemove:                "Packages",
	reconcile.KindCronAdd:                      "Cron",
	reconcile.KindCronRemove:                   "Cron",
	reconcile.KindCronChange:                   "Cron",
	reconcile.KindTemplateChange:               "services.<svc>.Templates",
	reconcile.KindPathChange:                   "Path",
	reconcile.KindServiceExecChange:            "services.<svc>.ExecFunc/ExecArgv",
	reconcile.KindServiceRestartChange:         "services.<svc>.Restart",
	reconcile.KindServiceAfterChange:           "services.<svc>.After",
	reconcile.KindServiceWorkdirChange:         "services.<svc>.WorkDir",
	reconcile.KindServiceUserChange:            "services.<svc>.User",
	reconcile.KindServiceSystemdOverrideChange: "services.<svc>.Systemd",
	reconcile.KindServiceHostnameChange:        "services.<svc>.Hostname",
	reconcile.KindServiceDirectChange:          "services.<svc>.Direct",
	reconcile.KindVolumeChange:                 "Volumes",
	reconcile.KindRepoChange:                   "Repos",
	reconcile.KindCommandsChange:               "Repos",
	reconcile.KindSSHEndpointHealed:            "no snapshot field: synthetic, healed in place by the daemon",
	reconcile.KindBundleRefresh:                "no snapshot field: synthetic, daemon fingerprint drift",
}

// A new live kind must be added to mergeLiveApplied (or declared
// snapshot-free) or its change is applied but never recorded, so it
// re-diffs on every reconcile.
func TestEveryLiveKindHasMergeHandling(t *testing.T) {
	live := map[reconcile.ChangeKind]bool{}
	for _, k := range reconcile.AllKinds() {
		if k.Bucket() != reconcile.BucketLive {
			continue
		}
		live[k] = true
		_, ok := liveKindMerge[k]
		require.True(t, ok, "live ChangeKind %d missing from liveKindMerge", k)
	}
	for k := range liveKindMerge {
		assert.True(t, live[k], "liveKindMerge lists non-live kind %d", k)
	}
}
