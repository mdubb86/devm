package reconcile

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/schema"
)

// Bucket categorises how invasive a change is to apply.
type Bucket int

const (
	BucketLive Bucket = iota // applicable to a running sandbox without ending sessions
	// BucketStopShell — requires VM stop + cold start. No ChangeKind
	// uses this bucket today; reserved for a future change type that
	// needs to bounce the VM without rebuilding it from scratch.
	BucketStopShell
	BucketTeardownShell // requires VM delete + cold start (volumes/install rerun)
	// BucketIronProxyRestart — regenerate iron-proxy config and respawn
	// iron-proxy on the same MAC_HOST:port. No VM cycle. Parallel to
	// BucketLive and BucketTeardownShell (not a severity step): a single
	// reconcile can produce changes in any combination of buckets.
	BucketIronProxyRestart
)

func (b Bucket) String() string {
	switch b {
	case BucketLive:
		return "live"
	case BucketStopShell:
		return "stop+shell"
	case BucketTeardownShell:
		return "teardown+shell"
	case BucketIronProxyRestart:
		return "iron-proxy-restart"
	}
	return "unknown"
}

// ChangeKind enumerates every kind of difference the diff machinery detects.
type ChangeKind int

const (
	KindPortAdd ChangeKind = iota
	KindPortRemove
	KindPortChange
	KindNetworkAdd
	KindNetworkRemove
	KindEnvAdd
	KindEnvRemove
	KindEnvChange
	KindInstallChange
	KindPackagesChange
	KindMaskAddRemove
	KindImageChange
	KindIdentityChange
	KindDockerToggle
	KindTemplateChange
	KindMountAddRemove
	KindPathChange
	KindServiceExecChange
	KindServiceRestartChange
	KindServiceAfterChange
	KindServiceWorkdirChange
	KindServiceUserChange
	KindServiceSystemdOverrideChange
	KindServiceHostnameChange
	// KindSecret* — value-drift of a `!secret NAME` reference (same
	// declaration, different keychain value). Env-diff already covers
	// reference syntax changes; these track the resolved values.
	KindSecretAdd
	KindSecretRemove
	KindSecretChange
)

// changeBucket is the single source of truth that maps each ChangeKind
// to its bucket. Bucket() and the diff/bucket table in the design spec
// both reference this map.
var changeBucket = map[ChangeKind]Bucket{
	KindPortAdd:       BucketLive,
	KindPortRemove:    BucketLive,
	KindPortChange:    BucketLive,
	KindNetworkAdd:    BucketIronProxyRestart,
	KindNetworkRemove: BucketIronProxyRestart,
	// Env changes are applied by rewriting the unit file and restarting
	// the service via tart exec — no VM recreate needed.
	KindEnvAdd:    BucketLive,
	KindEnvRemove: BucketLive,
	KindEnvChange: BucketLive,
	// install: commands happen on first boot; can't re-run cleanly on a
	// half-installed VM.
	KindInstallChange: BucketTeardownShell,
	// apt packages similarly — recreate is cleaner than diffing.
	KindPackagesChange: BucketTeardownShell,
	// virtio-fs mounts are set at tart run time; requires full recreate.
	KindMountAddRemove: BucketTeardownShell,
	// mount --bind masks are applied at boot; requires full recreate.
	KindMaskAddRemove:  BucketTeardownShell,
	KindImageChange:    BucketTeardownShell,
	KindIdentityChange: BucketTeardownShell,
	KindDockerToggle:   BucketTeardownShell,
	KindTemplateChange: BucketLive,
	// Path is materialized in .devm/.env (same fan-out as Env) — live.
	KindPathChange: BucketLive,
	// Service unit changes: re-render unit, daemon-reload, restart unit
	// via tart exec — no VM recreate needed.
	KindServiceExecChange:            BucketLive,
	KindServiceRestartChange:         BucketLive,
	KindServiceAfterChange:           BucketLive,
	KindServiceWorkdirChange:         BucketLive,
	KindServiceUserChange:            BucketLive,
	KindServiceSystemdOverrideChange: BucketLive,
	// Hostname: re-render Caddyfile, push to Mac proxy — live.
	KindServiceHostnameChange: BucketLive,
	// Secrets: iron-proxy config carries resolved values; a rotation
	// requires regenerating that config and respawning iron-proxy.
	KindSecretAdd:    BucketIronProxyRestart,
	KindSecretRemove: BucketIronProxyRestart,
	KindSecretChange: BucketIronProxyRestart,
}

// Bucket returns the bucket this ChangeKind belongs to.
func (k ChangeKind) Bucket() Bucket { return changeBucket[k] }

// Change is one diff entry between old and new configs.
type Change struct {
	Kind    ChangeKind
	Service string // service name when applicable; empty otherwise
	Key     string // sub-key: env var name, sandbox port, domain, mask path
	Old     string // formatted previous value; empty for adds
	New     string // formatted new value; empty for removes
	Detail  string // freeform extra info for the formatter
}

func (c Change) Bucket() Bucket { return c.Kind.Bucket() }

// FlavorKind names the recreate flavor required to apply a set of changes.
type FlavorKind int

const (
	FlavorLiveOnly FlavorKind = iota // no recreate, only live applies
	// FlavorStopShell — requires VM stop + cold start. Unreachable
	// today (no ChangeKind sits in BucketStopShell), kept paired with
	// the bucket so adding a future BucketStopShell ChangeKind doesn't
	// also need a flavor change.
	FlavorStopShell
	FlavorTeardownShell // requires VM delete + cold start
)

// String implements fmt.Stringer so FlavorKind renders directly in %s
// format verbs (used by orchestrator's format.go and error messages).
func (f FlavorKind) String() string {
	switch f {
	case FlavorLiveOnly:
		return "live"
	case FlavorStopShell:
		return "stop+shell"
	case FlavorTeardownShell:
		return "teardown+shell"
	}
	return "unknown"
}

// RecreateFlavor picks the max severity across all changes' buckets.
func RecreateFlavor(changes []Change) FlavorKind {
	max := FlavorLiveOnly
	for _, c := range changes {
		switch c.Bucket() {
		case BucketStopShell:
			if max < FlavorStopShell {
				max = FlavorStopShell
			}
		case BucketTeardownShell:
			return FlavorTeardownShell // can't go higher
		}
	}
	return max
}

// ComputePortChanges returns diffs for service canonical ports between
// old and new configs, sorted by service name for determinism.
func ComputePortChanges(old, new schema.Config) []Change {
	names := unionServiceNames(old.Services, new.Services)
	var changes []Change
	for _, name := range names {
		oldPort := old.Services[name].Port
		newPort := new.Services[name].Port
		if oldPort == newPort {
			continue
		}
		switch {
		case oldPort == 0 && newPort != 0:
			changes = append(changes, Change{
				Kind: KindPortAdd, Service: name,
				Key: strconv.Itoa(newPort), New: strconv.Itoa(newPort),
			})
		case oldPort != 0 && newPort == 0:
			changes = append(changes, Change{
				Kind: KindPortRemove, Service: name,
				Key: strconv.Itoa(oldPort), Old: strconv.Itoa(oldPort),
			})
		default:
			changes = append(changes, Change{
				Kind: KindPortChange, Service: name,
				Key: strconv.Itoa(newPort),
				Old: strconv.Itoa(oldPort), New: strconv.Itoa(newPort),
			})
		}
	}
	return changes
}

// ComputeAllChanges returns the full set of diffs between old and new
// configs. Order: ports, network, env (per service), service unit fields
// (per service), install, packages, mounts, masks (per service), image,
// identity, templates, path. Within each section, service names are sorted
// alphabetically for determinism.
//
// `repoRoot` is required by the templates diff to render the desired
// installer scripts. `lastAppliedTemplates` is the last-applied baseline
// (basename -> rendered content, from the daemon's persisted
// StateSnapshot); pass nil when there is none (e.g. cold-start with no
// prior snapshot), which surfaces every declared template as an add.
func ComputeAllChanges(old, new schema.Config, repoRoot string, lastAppliedTemplates map[string]string) ([]Change, error) {
	var out []Change
	out = append(out, ComputePortChanges(old, new)...)
	out = append(out, computeGlobalEnvChanges(old, new)...)
	out = append(out, computeEnvChanges(old, new)...)
	out = append(out, computeServiceUnitChanges(old, new)...)
	out = append(out, computeHostnameChanges(old, new)...)
	out = append(out, computeInstallChanges(old, new)...)
	out = append(out, computePackagesChange(old, new)...)
	out = append(out, computeMountAddRemove(old, new)...)
	out = append(out, computeMaskAddRemove(old, new)...)
	out = append(out, computeImageChange(old, new)...)
	out = append(out, computeIdentityChange(old, new)...)
	out = append(out, computeDockerChange(old, new)...)
	out = append(out, computePathChange(old, new)...)
	tmplChanges, err := ComputeTemplateChanges(new, repoRoot, lastAppliedTemplates)
	if err != nil {
		return nil, err
	}
	out = append(out, tmplChanges...)
	return out, nil
}

func computePathChange(old, new schema.Config) []Change {
	if pathEqual(old.Path, new.Path) {
		return nil
	}
	return []Change{{
		Kind: KindPathChange,
		Old:  strings.Join(old.Path, ":"),
		New:  strings.Join(new.Path, ":"),
	}}
}

func pathEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// computeGlobalEnvChanges diffs the project-level env map (cfg.Env),
// distinct from computeEnvChanges below which diffs each service's own
// env block. Service is left empty on these Change entries to mark
// them as global-scoped — ApplyLive pipes cfg.Env unprefixed into
// /opt/devm/.env via the devmbundle, so a global-scope change is real
// and must surface for reconcile to pick up.
func computeGlobalEnvChanges(old, new schema.Config) []Change {
	oEnv := globalEnvOf(old)
	nEnv := globalEnvOf(new)
	var out []Change
	for _, k := range unionStringKeys(oEnv, nEnv) {
		oVal, oOk := oEnv[k]
		nVal, nOk := nEnv[k]
		switch {
		case !oOk && nOk:
			out = append(out, Change{Kind: KindEnvAdd, Key: k, New: nVal})
		case oOk && !nOk:
			out = append(out, Change{Kind: KindEnvRemove, Key: k, Old: oVal})
		case oOk && nOk && oVal != nVal:
			out = append(out, Change{Kind: KindEnvChange, Key: k, Old: oVal, New: nVal})
		}
	}
	return out
}

func globalEnvOf(cfg schema.Config) map[string]string {
	out := make(map[string]string, len(cfg.Env))
	for k, v := range cfg.Env {
		out[k] = v.Render()
	}
	return out
}

func computeEnvChanges(old, new schema.Config) []Change {
	var out []Change
	for _, svc := range unionServiceNames(old.Services, new.Services) {
		oEnv := envOf(old.Services[svc])
		nEnv := envOf(new.Services[svc])
		for _, k := range unionStringKeys(oEnv, nEnv) {
			oVal, oOk := oEnv[k]
			nVal, nOk := nEnv[k]
			switch {
			case !oOk && nOk:
				out = append(out, Change{Kind: KindEnvAdd, Service: svc, Key: k, New: nVal})
			case oOk && !nOk:
				out = append(out, Change{Kind: KindEnvRemove, Service: svc, Key: k, Old: oVal})
			case oOk && nOk && oVal != nVal:
				out = append(out, Change{Kind: KindEnvChange, Service: svc, Key: k, Old: oVal, New: nVal})
			}
		}
	}
	return out
}

// computeServiceUnitChanges emits per-field changes for the Tart-era
// service unit fields (exec, restart, after, workdir, user, systemd).
// Each field maps to its own ChangeKind so the bucket logic and formatter
// can handle them individually.
func computeServiceUnitChanges(old, new schema.Config) []Change {
	var out []Change
	for _, svc := range unionServiceNames(old.Services, new.Services) {
		o, n := old.Services[svc], new.Services[svc]
		if !stringSliceEqual(o.Exec, n.Exec) {
			out = append(out, Change{Kind: KindServiceExecChange, Service: svc})
		}
		if o.Restart != n.Restart {
			out = append(out, Change{Kind: KindServiceRestartChange, Service: svc})
		}
		if !stringSliceEqual(o.After, n.After) {
			out = append(out, Change{Kind: KindServiceAfterChange, Service: svc})
		}
		if o.WorkDir != n.WorkDir {
			out = append(out, Change{Kind: KindServiceWorkdirChange, Service: svc})
		}
		if o.User != n.User {
			out = append(out, Change{Kind: KindServiceUserChange, Service: svc})
		}
		if o.Systemd != n.Systemd {
			out = append(out, Change{Kind: KindServiceSystemdOverrideChange, Service: svc})
		}
	}
	return out
}

// computeHostnameChanges emits KindServiceHostnameChange for services whose
// hostname field differs between old and new.
func computeHostnameChanges(old, new schema.Config) []Change {
	var out []Change
	for _, svc := range unionServiceNames(old.Services, new.Services) {
		o, n := old.Services[svc], new.Services[svc]
		if o.Hostname != n.Hostname {
			out = append(out, Change{Kind: KindServiceHostnameChange, Service: svc,
				Old: o.Hostname, New: n.Hostname})
		}
	}
	return out
}

func computeInstallChanges(old, new schema.Config) []Change {
	if stringSliceEqual(old.Install, new.Install) {
		return nil
	}
	return []Change{{Kind: KindInstallChange}}
}

func computePackagesChange(old, new schema.Config) []Change {
	if stringSliceEqual(old.Packages, new.Packages) {
		return nil
	}
	return []Change{{Kind: KindPackagesChange}}
}

func computeMountAddRemove(old, new schema.Config) []Change {
	if stringSliceEqual(old.Mounts, new.Mounts) {
		return nil
	}
	return []Change{{Kind: KindMountAddRemove}}
}

func computeMaskAddRemove(old, new schema.Config) []Change {
	var out []Change
	for _, svc := range unionServiceNames(old.Services, new.Services) {
		if !masksEqual(old.Services[svc].Masks, new.Services[svc].Masks) {
			out = append(out, Change{Kind: KindMaskAddRemove, Service: svc})
		}
	}
	return out
}

func masksEqual(a, b []schema.Mask) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func computeImageChange(old, new schema.Config) []Change {
	if old.BaseImage == new.BaseImage {
		return nil
	}
	return []Change{{Kind: KindImageChange}}
}

func computeIdentityChange(old, new schema.Config) []Change {
	if old.Project == new.Project {
		return nil
	}
	return []Change{{Kind: KindIdentityChange}}
}

func computeDockerChange(old, new schema.Config) []Change {
	if old.Docker == new.Docker {
		return nil
	}
	return []Change{{Kind: KindDockerToggle}}
}

// computeNetworkChanges diffs cfg.Network.Domains() between old and
// new configs. Order-preserving on the input list would be
// unnecessary — a sorted comparison keeps diff output deterministic
// regardless of the yaml order in the user's file.
func computeNetworkChanges(old, new schema.Config) []Change {
	oldSet := make(map[string]struct{}, len(old.Network.Domains()))
	for _, h := range old.Network.Domains() {
		oldSet[h] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(new.Network.Domains()))
	for _, h := range new.Network.Domains() {
		newSet[h] = struct{}{}
	}
	var out []Change
	for _, h := range new.Network.Domains() {
		if _, ok := oldSet[h]; !ok {
			out = append(out, Change{Kind: KindNetworkAdd, Key: h, New: h})
		}
	}
	for _, h := range old.Network.Domains() {
		if _, ok := newSet[h]; !ok {
			out = append(out, Change{Kind: KindNetworkRemove, Key: h, Old: h})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func envOf(s schema.Service) map[string]string {
	out := make(map[string]string, len(s.Env))
	for k, v := range s.Env {
		out[k] = v.Render()
	}
	return out
}

func unionStringKeys(a, b map[string]string) []string {
	set := make(map[string]struct{})
	for k := range a {
		set[k] = struct{}{}
	}
	for k := range b {
		set[k] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func unionServiceNames(a, b map[string]schema.Service) []string {
	set := make(map[string]struct{})
	for k := range a {
		set[k] = struct{}{}
	}
	for k := range b {
		set[k] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ComputeTemplateChanges diffs the installer scripts that would be
// produced from `new` against `lastApplied`, the rendered content of
// each template as of the last successful apply (basename -> content),
// sourced from the daemon's persisted StateSnapshot.TemplateContents.
// Pass nil when there is no prior snapshot; every declared template
// then surfaces as an add.
//
// Emits a Change per template that would differ from its last-applied
// content (including newly-added templates) and a Change per
// last-applied template that is no longer in the new config (removal).
func ComputeTemplateChanges(new schema.Config, repoRoot string, lastApplied map[string]string) ([]Change, error) {
	desired, err := render.RenderTemplates(new, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("compute templates: %w", err)
	}

	// Map basename -> service+output for the new set (so we can recover
	// detail when reporting). Walk the cfg again deterministically.
	type meta struct{ Service, Output string }
	desiredMeta := map[string]meta{}
	svcNames := make([]string, 0, len(new.Services))
	for n := range new.Services {
		svcNames = append(svcNames, n)
	}
	sort.Strings(svcNames)
	idx := 0
	for _, svc := range svcNames {
		for _, tmpl := range new.Services[svc].Templates {
			base := fmt.Sprintf("%02d-%s-%s.sh", idx, svc, filepath.Base(tmpl.Output))
			desiredMeta[base] = meta{Service: svc, Output: tmpl.Output}
			idx++
		}
	}

	desiredBasenames := make(map[string]struct{}, len(desired))
	for path := range desired {
		desiredBasenames[filepath.Base(path)] = struct{}{}
	}

	var out []Change
	// Additions + changes.
	for path, content := range desired {
		base := filepath.Base(path)
		m := desiredMeta[base]
		existing, ok := lastApplied[base]
		if !ok {
			out = append(out, Change{
				Kind: KindTemplateChange, Service: m.Service, Detail: m.Output,
				New: "installed",
			})
			continue
		}
		if existing != content {
			out = append(out, Change{
				Kind: KindTemplateChange, Service: m.Service, Detail: m.Output,
				Old: "previous", New: "updated",
			})
		}
	}
	// Removals.
	for base := range lastApplied {
		if _, ok := desiredBasenames[base]; ok {
			continue
		}
		out = append(out, Change{
			Kind: KindTemplateChange, Service: "", Detail: base,
			Old: "previous",
		})
	}

	// Deterministic ordering by detail (output path / basename).
	sort.Slice(out, func(i, j int) bool { return out[i].Detail < out[j].Detail })
	return out, nil
}
