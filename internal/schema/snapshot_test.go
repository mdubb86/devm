package schema

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// mustParseWithFunctions decodes the YAML body into a Config seeded with
// a valid project.name (so Project.Validate passes) and the given
// Functions list (so function-reference validators see it populated).
func mustParseWithFunctions(t *testing.T, body string, functions []string) Config {
	t.Helper()
	c := Config{Project: Project{Name: "p"}}
	require.NoError(t, yaml.Unmarshal([]byte(body), &c))
	c.Functions = functions
	return c
}

// mustLoadYAML decodes body into a Config seeded with a valid
// project.name and asserts the decode succeeds. Returns the config
// for further assertions. Validate is NOT called — tests use this
// for decode-only assertions (round-tripping, accessor parsing) where
// adding synthetic hook functions to appease validateSnapshots would
// add noise.
func mustLoadYAML(t *testing.T, body string) Config {
	t.Helper()
	c := Config{Project: Project{Name: "p"}}
	require.NoError(t, yaml.Unmarshal([]byte(body), &c))
	return c
}

// loadYAMLWithFuncs decodes body into a Config with the given
// functions slice and runs Validate. Returns the config and the
// combined parse-or-validate error — tests use it to assert both
// successful and failing paths.
func loadYAMLWithFuncs(t *testing.T, body string, functions []string) (Config, error) {
	t.Helper()
	c := Config{Project: Project{Name: "p"}}
	if err := yaml.Unmarshal([]byte(body), &c); err != nil {
		return c, err
	}
	c.Functions = functions
	return c, c.Validate()
}

func TestSnapshots_HookPresenceRequired(t *testing.T) {
	c := mustParseWithFunctions(t, `
snapshots:
  pg:
    on_stop: true
`, nil) // neither backup_pg nor restore_pg declared
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backup_pg")
}

func TestSnapshots_HookPresenceRequired_RestoreMissing(t *testing.T) {
	c := mustParseWithFunctions(t, `
snapshots:
  pg:
    on_stop: true
`, []string{"backup_pg"}) // restore_pg missing
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restore_pg")
}

func TestSnapshots_CronRequiresKeep(t *testing.T) {
	c := mustParseWithFunctions(t, `
snapshots:
  pg:
    cron: "*/15 * * * *"
`, []string{"backup_pg", "restore_pg"})
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keep")
}

func TestSnapshots_CronMustParse(t *testing.T) {
	cases := []struct {
		name string
		cron string
	}{
		{"macro", "@hourly"},
		{"six fields", "0 0 * * * *"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mustParseWithFunctions(t, `
snapshots:
  pg:
    cron: "`+tc.cron+`"
    keep: 3
`, []string{"backup_pg", "restore_pg"})
			err := c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "snapshots.pg: cron")
			assert.Contains(t, err.Error(), tc.cron)
		})
	}
}

func TestSnapshots_KeepMustBePositive(t *testing.T) {
	cases := []struct {
		name string
		keep string
	}{
		{"zero", "0"},
		{"negative", "-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mustParseWithFunctions(t, `
snapshots:
  pg:
    cron: "*/15 * * * *"
    keep: `+tc.keep+`
`, []string{"backup_pg", "restore_pg"})
			err := c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "positive")
		})
	}
}

func TestSnapshots_TimeoutStepMustParse(t *testing.T) {
	c := mustParseWithFunctions(t, `
snapshots:
  pg:
    on_stop: true
    timeout: { step: bogus }
`, []string{"backup_pg", "restore_pg"})
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
	assert.Contains(t, err.Error(), "step")
}

func TestSnapshots_UnknownKeyRejected(t *testing.T) {
	var c Config
	err := yaml.Unmarshal([]byte(`
snapshots:
  pg:
    wat: true
`), &c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"wat"`)
}

func TestSnapshots_OrderPreserved(t *testing.T) {
	c := mustParseWithFunctions(t, `
snapshots:
  pg:
    on_stop: true
  redis:
    on_stop: true
`, []string{"backup_pg", "restore_pg", "backup_redis", "restore_redis"})
	require.NoError(t, c.Validate())
	assert.Equal(t, []string{"pg", "redis"}, c.Snapshots.Order)

	// And the reverse order is preserved too.
	c2 := mustParseWithFunctions(t, `
snapshots:
  redis:
    on_stop: true
  pg:
    on_stop: true
`, []string{"backup_pg", "restore_pg", "backup_redis", "restore_redis"})
	require.NoError(t, c2.Validate())
	assert.Equal(t, []string{"redis", "pg"}, c2.Snapshots.Order)
}

func TestSnapshots_Defaults(t *testing.T) {
	c := mustParseWithFunctions(t, `
snapshots:
  pg: {}
`, []string{"backup_pg", "restore_pg"})
	require.NoError(t, c.Validate())
	snap, ok := c.Snapshots.Get("pg")
	require.True(t, ok)
	assert.Nil(t, snap.Path)
	assert.Nil(t, snap.Cron)
	assert.Nil(t, snap.Keep)
	assert.Nil(t, snap.OnStop)
	assert.Nil(t, snap.OnTeardown)
	assert.Nil(t, snap.Timeout)
}

func TestSnapshots_HookPresentInDevmMeSh(t *testing.T) {
	// LoadFunctions merges devm.sh and devm.me.sh into one Functions
	// list before Validate runs. This test simulates that: a hook
	// declared only in devm.me.sh still satisfies validateSnapshots
	// because the caller passed both files' functions.
	c := mustParseWithFunctions(t, `
snapshots:
  pg:
    on_stop: true
`, []string{"backup_pg", "restore_pg"})
	require.NoError(t, c.Validate())
}

func TestSnapshots_CronWithKeep_OK(t *testing.T) {
	c := mustParseWithFunctions(t, `
snapshots:
  pg:
    cron: "*/15 * * * *"
    keep: 5
`, []string{"backup_pg", "restore_pg"})
	require.NoError(t, c.Validate())
	snap, ok := c.Snapshots.Get("pg")
	require.True(t, ok)
	require.NotNil(t, snap.Cron)
	assert.Equal(t, "*/15 * * * *", *snap.Cron)
	require.NotNil(t, snap.Keep)
	assert.Equal(t, 5, *snap.Keep)
}

// TestSnapshot_YAMLRoundTripPreservesFields pins that a Config with a
// populated Snapshot can be yaml.Marshal'd and yaml.Unmarshal'd back
// without any field drifting or losing its value. A regression here
// (missing yaml tag, a non-stringy parsed field, a rename) would
// break snapshot storage round-trips.
func TestSnapshot_YAMLRoundTripPreservesFields(t *testing.T) {
	path := "/home/devm/.claude"
	cron := "*/15 * * * *"
	keep := 5
	onStop := true
	onTeardown := false
	pre := "30s"
	step := "5m"
	post := "1m"

	orig := Snapshot{
		Path:       &path,
		Cron:       &cron,
		Keep:       &keep,
		OnStop:     &onStop,
		OnTeardown: &onTeardown,
		Timeout:    &SnapshotTimeout{Pre: &pre, Step: &step, Post: &post},
	}
	cfg := Config{
		Project:   Project{Name: "p"},
		Snapshots: OrderedMapOf(map[string]Snapshot{"pg": orig}),
	}

	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	var round Config
	require.NoError(t, yaml.Unmarshal(data, &round))

	got, ok := round.Snapshots.Get("pg")
	require.True(t, ok)
	require.NotNil(t, got.Path)
	assert.Equal(t, *orig.Path, *got.Path)
	require.NotNil(t, got.Cron)
	assert.Equal(t, *orig.Cron, *got.Cron)
	require.NotNil(t, got.Keep)
	assert.Equal(t, *orig.Keep, *got.Keep)
	require.NotNil(t, got.OnStop)
	assert.Equal(t, *orig.OnStop, *got.OnStop)
	require.NotNil(t, got.OnTeardown)
	assert.Equal(t, *orig.OnTeardown, *got.OnTeardown)
	require.NotNil(t, got.Timeout)
	require.NotNil(t, got.Timeout.Pre)
	assert.Equal(t, *orig.Timeout.Pre, *got.Timeout.Pre)
	require.NotNil(t, got.Timeout.Step)
	assert.Equal(t, *orig.Timeout.Step, *got.Timeout.Step)
	require.NotNil(t, got.Timeout.Post)
	assert.Equal(t, *orig.Timeout.Post, *got.Timeout.Post)
}

// TestSnapshot_PathMode_NoHooksRequired pins the path-mode rule: an
// entry with `path:` set and no backup_<name>/restore_<name> function
// validates cleanly — path mode's hooks are the pre_/post_ pair and
// both are optional.
func TestSnapshot_PathMode_NoHooksRequired(t *testing.T) {
	cfg := mustLoadYAML(t, `
project: { name: p }
snapshots:
  claude:
    path: /home/devm/.claude
    cron: "0 */4 * * *"
    keep: 7
`)
	snap, ok := cfg.Snapshots.Get("claude")
	require.True(t, ok)
	require.NotNil(t, snap.Path)
	assert.Equal(t, "/home/devm/.claude", *snap.Path)
}

// TestSnapshot_StreamMode_RequiresBothHooks pins that stream mode
// (no `path:`) still demands both backup_<name> and restore_<name>.
func TestSnapshot_StreamMode_RequiresBothHooks(t *testing.T) {
	_, err := loadYAMLWithFuncs(t, `
snapshots:
  pg:
    cron: "0 */6 * * *"
    keep: 5
`, []string{"backup_pg"})
	require.ErrorContains(t, err, "missing function(s)")
	require.ErrorContains(t, err, "restore_pg")
}

// TestSnapshot_AmbiguousMode_PathPlusBareBackup pins the mutex: an
// entry with `path:` AND a declared backup_<name> function is
// rejected — the user must pick one mode.
func TestSnapshot_AmbiguousMode_PathPlusBareBackup(t *testing.T) {
	_, err := loadYAMLWithFuncs(t, `
snapshots:
  foo:
    path: /tmp/foo
`, []string{"backup_foo", "restore_foo"})
	require.ErrorContains(t, err, "ambiguous")
	require.ErrorContains(t, err, "path:")
}

// TestSnapshot_NestedTimeout_RoundTrips pins the nested timeout
// shape: each phase decodes and its accessor returns the parsed
// value.
func TestSnapshot_NestedTimeout_RoundTrips(t *testing.T) {
	cfg := mustLoadYAML(t, `
snapshots:
  pg:
    timeout: { pre: 30s, step: 5m, post: 1m }
`)
	snap, ok := cfg.Snapshots.Get("pg")
	require.True(t, ok)
	require.NotNil(t, snap.Timeout)

	pre, ok := snap.Timeout.PreDuration()
	require.True(t, ok)
	assert.Equal(t, 30*time.Second, pre)
	step, ok := snap.Timeout.StepDuration()
	require.True(t, ok)
	assert.Equal(t, 5*time.Minute, step)
	post, ok := snap.Timeout.PostDuration()
	require.True(t, ok)
	assert.Equal(t, time.Minute, post)
}

// TestSnapshot_NestedTimeout_Defaults pins that unset phases fall
// back to DefaultPreTimeout / DefaultStepTimeout / DefaultPostTimeout
// with ok=false.
func TestSnapshot_NestedTimeout_Defaults(t *testing.T) {
	c, err := loadYAMLWithFuncs(t, `
snapshots:
  pg:
    timeout: { step: 5m }
`, []string{"backup_pg", "restore_pg"})
	require.NoError(t, err)
	snap, _ := c.Snapshots.Get("pg")
	require.NotNil(t, snap.Timeout)

	pre, ok := snap.Timeout.PreDuration()
	assert.False(t, ok)
	assert.Equal(t, DefaultPreTimeout, pre)
	step, ok := snap.Timeout.StepDuration()
	assert.True(t, ok)
	assert.Equal(t, 5*time.Minute, step)
	post, ok := snap.Timeout.PostDuration()
	assert.False(t, ok)
	assert.Equal(t, DefaultPostTimeout, post)
}

// TestSnapshot_BareTimeoutString_Rejected pins that the pre-nested
// `timeout: 10m` form fails decode with a message naming the nested
// sub-keys, nudging the user toward `timeout: { pre:, step:, post: }`.
func TestSnapshot_BareTimeoutString_Rejected(t *testing.T) {
	_, err := loadYAMLWithFuncs(t, `
snapshots:
  pg:
    timeout: 10m
`, []string{"backup_pg", "restore_pg"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
	assert.Contains(t, err.Error(), "pre:")
}

// TestSnapshot_TimeoutUnknownSubKeyRejected pins that typos inside
// the nested timeout block surface loudly, not silently decoded to
// nil.
func TestSnapshot_TimeoutUnknownSubKeyRejected(t *testing.T) {
	_, err := loadYAMLWithFuncs(t, `
snapshots:
  pg:
    timeout: { steps: 5m }
`, []string{"backup_pg", "restore_pg"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"steps"`)
}
