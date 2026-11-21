package schema

import (
	"fmt"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/cronspec"
	"gopkg.in/yaml.v3"
)

// Snapshot is one entry under devm.yaml's top-level `snapshots:` map.
// The key under which it appears is the snapshot name. An entry runs
// in one of two mutually exclusive modes:
//
//   - Path mode (Path != nil): devm snapshots the files under the
//     configured guest path directly. Pre/post hooks
//     (pre_backup_<name>, post_backup_<name>, pre_restore_<name>,
//     post_restore_<name>) are all optional.
//   - Stream mode (Path == nil): devm pipes the output of
//     backup_<name> into storage and feeds restore_<name> on the way
//     back. Both hooks are required.
//
// Having `path:` set AND a bare `backup_<name>` or `restore_<name>`
// function declared in devm.sh / devm.me.sh is ambiguous and rejected
// by Config.validateSnapshots.
type Snapshot struct {
	// Path, when set, selects path mode and names the guest-side
	// filesystem path devm snapshots. Nil ⇒ stream mode.
	Path *string `yaml:"path,omitempty"`

	// Cron is the schedule for automatic snapshots (standard 5-field
	// cron expression). Nil = no schedule.
	Cron *string `yaml:"cron,omitempty"`

	// Keep is how many scheduled snapshots to retain; older ones are
	// pruned after each successful backup. Required when Cron is set.
	// Nil = no retention policy (ad-hoc only).
	Keep *int `yaml:"keep,omitempty"`

	// OnStop toggles taking a snapshot during `devm stop`. Nil ⇒
	// default (no stop-time snapshot).
	OnStop *bool `yaml:"on_stop,omitempty"`

	// OnTeardown toggles taking a snapshot during `devm teardown`.
	// Nil ⇒ default (no teardown-time snapshot).
	OnTeardown *bool `yaml:"on_teardown,omitempty"`

	// Timeout nests the three per-phase deadlines (pre / step / post).
	// Nil ⇒ all three phases fall back to their DefaultPreTimeout /
	// DefaultStepTimeout / DefaultPostTimeout defaults.
	Timeout *SnapshotTimeout `yaml:"timeout,omitempty"`
}

// SnapshotTimeout nests the per-phase deadlines for a snapshot
// capture or restore. Each sub-field is the raw duration string from
// devm.yaml (e.g. "30s", "10m"). Nil ⇒ that phase uses its default.
// Validate confirms each present value parses as a Go duration.
type SnapshotTimeout struct {
	// Pre is the deadline for pre_backup_<name> / pre_restore_<name>
	// hooks. Nil ⇒ DefaultPreTimeout.
	Pre *string `yaml:"pre,omitempty"`

	// Step is the deadline for the main capture / restore step —
	// backup_<name> / restore_<name> in stream mode, or the path copy
	// in path mode. Nil ⇒ DefaultStepTimeout.
	Step *string `yaml:"step,omitempty"`

	// Post is the deadline for post_backup_<name> / post_restore_<name>
	// hooks. Nil ⇒ DefaultPostTimeout.
	Post *string `yaml:"post,omitempty"`
}

// DefaultPreTimeout is the per-phase deadline for the pre-hook when
// the snapshot entry does not set timeout.pre.
const DefaultPreTimeout = 30 * time.Second

// DefaultStepTimeout is the per-phase deadline for the main
// capture / restore step when the snapshot entry does not set
// timeout.step.
const DefaultStepTimeout = 10 * time.Minute

// DefaultPostTimeout is the per-phase deadline for the post-hook when
// the snapshot entry does not set timeout.post.
const DefaultPostTimeout = 30 * time.Second

// snapshotKnownFields lists the yaml keys Snapshot accepts. Kept in
// sync with the yaml tags above; enforced by rejectUnknownYAMLKeys in
// UnmarshalYAML so typos surface loudly instead of silently decoding
// to nil.
var snapshotKnownFields = []string{"path", "cron", "keep", "on_stop", "on_teardown", "timeout"}

// snapshotTimeoutKnownFields lists the yaml keys SnapshotTimeout
// accepts. Enforced by rejectUnknownYAMLKeys so typos (`prer:`,
// `steps:`) surface loudly.
var snapshotTimeoutKnownFields = []string{"pre", "step", "post"}

// snapshotAlias strips Snapshot's UnmarshalYAML method so node.Decode
// below calls yaml.v3's reflective decoder instead of recursing into
// us.
type snapshotAlias Snapshot

// snapshotTimeoutAlias is the same trick for SnapshotTimeout.
type snapshotTimeoutAlias SnapshotTimeout

// UnmarshalYAML rejects unknown keys at the snapshot entry, then
// delegates to the reflective decoder via snapshotAlias. A bare
// scalar `timeout:` value (the pre-nested form) hits the nested
// decoder as a scalar-into-mapping error; we catch that and return a
// clear "use nested form" message naming the three sub-keys.
func (s *Snapshot) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("snapshot: expected mapping, got %v", node.Kind)
	}
	if err := rejectUnknownYAMLKeys(node, "snapshot", snapshotKnownFields); err != nil {
		return err
	}
	// Scan for a scalar `timeout:` value before delegating so the
	// error message tells the user what to type, not what the
	// reflective decoder saw.
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == "timeout" && node.Content[i+1].Kind == yaml.ScalarNode {
			return fmt.Errorf(
				"snapshot: timeout must be nested { pre:, step:, post: } — bare %q no longer accepted",
				node.Content[i+1].Value)
		}
	}
	var raw snapshotAlias
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*s = Snapshot(raw)
	return nil
}

// UnmarshalYAML enforces the known-key set on the nested timeout
// mapping before delegating to the reflective decoder.
func (t *SnapshotTimeout) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf(
			"snapshot timeout: must be nested { pre:, step:, post: }, got %v",
			node.Kind)
	}
	if err := rejectUnknownYAMLKeys(node, "snapshot.timeout", snapshotTimeoutKnownFields); err != nil {
		return err
	}
	var raw snapshotTimeoutAlias
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*t = SnapshotTimeout(raw)
	return nil
}

// PreDuration returns the parsed pre-hook deadline. When Pre is nil
// or fails to parse, it returns (DefaultPreTimeout, false); when Pre
// is set and parses, it returns (parsed, true). Validate guarantees
// any configured value parses cleanly, so the false branch after
// validation means "unset, defaulted".
func (t *SnapshotTimeout) PreDuration() (time.Duration, bool) {
	return parsePhaseDuration(t.Pre, DefaultPreTimeout)
}

// StepDuration returns the parsed main-step deadline, falling back to
// DefaultStepTimeout when Step is nil or fails to parse. See
// PreDuration for the (default, false) / (parsed, true) contract.
func (t *SnapshotTimeout) StepDuration() (time.Duration, bool) {
	return parsePhaseDuration(t.Step, DefaultStepTimeout)
}

// PostDuration returns the parsed post-hook deadline, falling back to
// DefaultPostTimeout when Post is nil or fails to parse. See
// PreDuration for the (default, false) / (parsed, true) contract.
func (t *SnapshotTimeout) PostDuration() (time.Duration, bool) {
	return parsePhaseDuration(t.Post, DefaultPostTimeout)
}

// parsePhaseDuration is the shared body of PreDuration / StepDuration
// / PostDuration: return (parsed, true) when set and parses, else
// (fallback, false).
func parsePhaseDuration(raw *string, fallback time.Duration) (time.Duration, bool) {
	if raw == nil {
		return fallback, false
	}
	d, err := time.ParseDuration(*raw)
	if err != nil {
		return fallback, false
	}
	return d, true
}

// validateSnapshots walks each snapshots entry in declared order and
// enforces:
//
//   - Path mode (Path != nil) is mutually exclusive with a bare
//     backup_<name> / restore_<name> function: having both is
//     ambiguous and rejected.
//   - Stream mode (Path == nil) requires both backup_<name> and
//     restore_<name> declared in functions.
//   - cron requires a positive keep (retention policy).
//   - any explicit keep is positive.
//   - each present timeout.<phase> parses as a Go duration.
//
// functions is the merged devm.sh + devm.me.sh function set
// (config.LoadFunctions), so a hook declared in either file counts.
func (c *Config) validateSnapshots(functions []string) error {
	if c.Snapshots.Len() == 0 {
		return nil
	}
	funcSet := make(map[string]bool, len(functions))
	for _, f := range functions {
		funcSet[f] = true
	}
	for _, name := range c.Snapshots.Order {
		snap := c.Snapshots.Entries[name]

		if snap.Path != nil {
			if funcSet["backup_"+name] || funcSet["restore_"+name] {
				return fmt.Errorf(
					"snapshots.%s: ambiguous mode: has path: and backup_%s/restore_%s function; remove one",
					name, name, name)
			}
		} else {
			var missing []string
			if !funcSet["backup_"+name] {
				missing = append(missing, "backup_"+name)
			}
			if !funcSet["restore_"+name] {
				missing = append(missing, "restore_"+name)
			}
			if len(missing) > 0 {
				return fmt.Errorf(
					"snapshots.%s: missing function(s) in devm.sh / devm.me.sh: %s",
					name, strings.Join(missing, ", "))
			}
		}

		if snap.Cron != nil && snap.Keep == nil {
			return fmt.Errorf(
				"snapshots.%s: cron requires keep (how many scheduled snapshots to retain)",
				name)
		}
		if snap.Cron != nil {
			if _, err := cronspec.ToOnCalendar(*snap.Cron); err != nil {
				return fmt.Errorf("snapshots.%s: cron: %w", name, err)
			}
		}
		if snap.Keep != nil && *snap.Keep <= 0 {
			return fmt.Errorf(
				"snapshots.%s: keep %d must be a positive integer",
				name, *snap.Keep)
		}

		if snap.Timeout != nil {
			if err := validateTimeoutPhase(name, "pre", snap.Timeout.Pre); err != nil {
				return err
			}
			if err := validateTimeoutPhase(name, "step", snap.Timeout.Step); err != nil {
				return err
			}
			if err := validateTimeoutPhase(name, "post", snap.Timeout.Post); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateTimeoutPhase is the shared check for one timeout.<phase>
// sub-field. Nil ⇒ no-op (defaulted elsewhere); non-nil must parse.
func validateTimeoutPhase(name, phase string, raw *string) error {
	if raw == nil {
		return nil
	}
	if _, err := time.ParseDuration(*raw); err != nil {
		return fmt.Errorf(
			"snapshots.%s: timeout.%s %q: %v", name, phase, *raw, err)
	}
	return nil
}
