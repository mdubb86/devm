package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cronCfg(m map[string]string) schema.Config {
	return schema.Config{Project: schema.Project{Name: "p"}, Cron: m}
}

func TestComputeAllChanges_CronAdd(t *testing.T) {
	changes, err := ComputeAllChanges(cronCfg(nil), cronCfg(map[string]string{"backup": "0 3 * * *"}), t.TempDir(), t.TempDir(), nil, nil, nil, "", "")
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, KindCronAdd, changes[0].Kind)
	assert.Equal(t, "backup", changes[0].Key)
	assert.Equal(t, "0 3 * * *", changes[0].New)
}

func TestComputeAllChanges_CronRemove(t *testing.T) {
	changes, err := ComputeAllChanges(cronCfg(map[string]string{"backup": "0 3 * * *"}), cronCfg(nil), t.TempDir(), t.TempDir(), nil, nil, nil, "", "")
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, KindCronRemove, changes[0].Kind)
	assert.Equal(t, "backup", changes[0].Key)
	assert.Equal(t, "0 3 * * *", changes[0].Old)
}

func TestComputeAllChanges_CronScheduleChanged(t *testing.T) {
	changes, err := ComputeAllChanges(
		cronCfg(map[string]string{"backup": "0 3 * * *", "same": "* * * * *"}),
		cronCfg(map[string]string{"backup": "0 4 * * *", "same": "* * * * *"}),
		t.TempDir(), t.TempDir(), nil, nil, nil, "", "")
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, KindCronChange, changes[0].Kind)
	assert.Equal(t, "backup", changes[0].Key)
	assert.Equal(t, "0 3 * * *", changes[0].Old)
	assert.Equal(t, "0 4 * * *", changes[0].New)
}

func TestCronChangesAllLiveBucket(t *testing.T) {
	assert.Equal(t, BucketLive, KindCronAdd.Bucket())
	assert.Equal(t, BucketLive, KindCronRemove.Bucket())
	assert.Equal(t, BucketLive, KindCronChange.Bucket())
}

func TestComputeAllChanges_CronAddOrderSorted(t *testing.T) {
	changes, err := ComputeAllChanges(cronCfg(nil),
		cronCfg(map[string]string{"zeta": "0 5 * * *", "alpha": "0 3 * * *"}),
		t.TempDir(), t.TempDir(), nil, nil, nil, "", "")
	require.NoError(t, err)
	require.Len(t, changes, 2)
	assert.Equal(t, "alpha", changes[0].Key)
	assert.Equal(t, "zeta", changes[1].Key)
}

// cronChangeShape returns the Change the differ emits for kind: Add has
// only New, Remove only Old, Change both.
func cronChangeShape(kind ChangeKind) Change {
	c := Change{Kind: kind, Key: "backup"}
	if kind != KindCronAdd {
		c.Old = "0 3 * * *"
	}
	if kind != KindCronRemove {
		c.New = "0 4 * * *"
	}
	return c
}

func TestApplyLive_CronChange_PipesBundle(t *testing.T) {
	for _, kind := range []ChangeKind{KindCronAdd, KindCronRemove, KindCronChange} {
		dir := t.TempDir()
		tr, log := fakeTartForApplyLive(t, dir)
		cfg := schema.Config{
			Project: schema.Project{Name: "p"},
			Cron:    map[string]string{"backup": "0 4 * * *"},
		}
		err := ApplyLive(tr, "p-vm", []Change{
			cronChangeShape(kind),
		}, cfg, dir, dir, nil, nil, nil, nil, nil, identity.Config{}, "", nil)
		require.NoError(t, err, "kind=%v", kind)

		_, statErr := os.Stat(filepath.Join(dir, ".devm"))
		require.True(t, os.IsNotExist(statErr), ".devm/ must not be written for kind=%v", kind)
		assert.Equal(t, 1, countCalls(t, log, "exec -i"), "expected one bundle pipe for kind=%v", kind)
	}
}
