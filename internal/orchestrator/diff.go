package orchestrator

import (
	"fmt"
	"os"
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
	BucketLive          Bucket = iota // applicable to a running sandbox without ending sessions
	BucketStopShell                   // requires sbx stop + cold start
	BucketTeardownShell               // requires sbx rm + cold start (volumes/install rerun)
)

func (b Bucket) String() string {
	switch b {
	case BucketLive:
		return "live"
	case BucketStopShell:
		return "stop+shell"
	case BucketTeardownShell:
		return "teardown+shell"
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
	KindStartupChange
	KindInstallChange
	KindMaskChange
	KindImageChange
	KindIdentityChange
	KindTemplateChange
	KindMountsChange
	KindPathChange
)

// changeBucket is the single source of truth that maps each ChangeKind
// to its bucket. Bucket() and the diff/bucket table in the design spec
// both reference this map.
var changeBucket = map[ChangeKind]Bucket{
	KindPortAdd:        BucketLive,
	KindPortRemove:     BucketLive,
	KindPortChange:     BucketLive,
	KindNetworkAdd:     BucketLive,
	KindNetworkRemove:  BucketStopShell,
	// Env is materialized in .devm/.env (mount-visible inside the VM)
	// and re-sourced by the with-devm-env wrapper on every `sbx exec`.
	// ApplyLive rewrites .devm/.env on the host; the next exec sees the
	// new values. Running shells keep their old env until they re-exec
	// — hence LIVE.
	KindEnvAdd:        BucketLive,
	KindEnvRemove:     BucketLive,
	KindEnvChange:     BucketLive,
	KindStartupChange: BucketStopShell,
	KindInstallChange:  BucketTeardownShell,
	KindMaskChange:     BucketTeardownShell,
	KindImageChange:    BucketTeardownShell,
	KindIdentityChange: BucketTeardownShell,
	KindTemplateChange: BucketLive,
	// Mounts: sbx run's positional workspaces are baked at create
	// time. Adding/removing/changing a mount requires `sbx rm` and
	// re-create.
	KindMountsChange: BucketTeardownShell,
	// Path is materialized in .devm/.env (same fan-out as Env) — live.
	KindPathChange: BucketLive,
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
	FlavorLiveOnly      FlavorKind = iota // no recreate, only live applies
	FlavorStopShell                       // requires sbx stop + cold start
	FlavorTeardownShell                   // requires sbx rm + cold start
)

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

// ComputeNetworkChanges returns add/remove diffs for allowed_domains,
// sorted alphabetically for determinism.
func ComputeNetworkChanges(old, new schema.Config) []Change {
	oldSet := setFromSlice(old.Network.AllowedDomains)
	newSet := setFromSlice(new.Network.AllowedDomains)
	all := make(map[string]struct{})
	for d := range oldSet {
		all[d] = struct{}{}
	}
	for d := range newSet {
		all[d] = struct{}{}
	}
	sorted := make([]string, 0, len(all))
	for d := range all {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)

	var changes []Change
	for _, d := range sorted {
		_, inOld := oldSet[d]
		_, inNew := newSet[d]
		switch {
		case !inOld && inNew:
			changes = append(changes, Change{Kind: KindNetworkAdd, Key: d, New: d})
		case inOld && !inNew:
			changes = append(changes, Change{Kind: KindNetworkRemove, Key: d, Old: d})
		}
	}
	return changes
}

// ComputeAllChanges returns the full set of diffs between old and new
// configs. Order: ports, network, env (per service), startup (per service),
// install, masks (per service), image, identity, templates. Within each
// section, service names are sorted alphabetically for determinism.
//
// `repoRoot` is required by the templates diff which reads on-disk
// installer scripts.
func ComputeAllChanges(old, new schema.Config, repoRoot string) ([]Change, error) {
	var out []Change
	out = append(out, ComputePortChanges(old, new)...)
	out = append(out, ComputeNetworkChanges(old, new)...)
	out = append(out, computeEnvChanges(old, new)...)
	out = append(out, computeStartupChanges(old, new)...)
	out = append(out, computeInstallChanges(old, new)...)
	out = append(out, computeMountsChanges(old, new)...)
	out = append(out, computeMaskChanges(old, new)...)
	out = append(out, computeImageChange(old, new)...)
	out = append(out, computeIdentityChange(old, new)...)
	out = append(out, computePathChange(old, new)...)
	tmplChanges, err := ComputeTemplateChanges(new, repoRoot)
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

func computeStartupChanges(old, new schema.Config) []Change {
	var out []Change
	for _, svc := range unionServiceNames(old.Services, new.Services) {
		if !startupsEqual(old.Services[svc].Startup, new.Services[svc].Startup) {
			out = append(out, Change{Kind: KindStartupChange, Service: svc})
		}
	}
	return out
}

func startupsEqual(a, b []schema.StartupCommand) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !stringSliceEqual(a[i].Command, b[i].Command) {
			return false
		}
		if a[i].Background != b[i].Background {
			return false
		}
	}
	return true
}

func computeInstallChanges(old, new schema.Config) []Change {
	if stringSliceEqual(old.Install, new.Install) {
		return nil
	}
	return []Change{{Kind: KindInstallChange}}
}

func computeMountsChanges(old, new schema.Config) []Change {
	if stringSliceEqual(old.Mounts, new.Mounts) {
		return nil
	}
	return []Change{{Kind: KindMountsChange}}
}

func computeMaskChanges(old, new schema.Config) []Change {
	var out []Change
	for _, svc := range unionServiceNames(old.Services, new.Services) {
		if !masksEqual(old.Services[svc].Masks, new.Services[svc].Masks) {
			out = append(out, Change{Kind: KindMaskChange, Service: svc})
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

func envOf(s schema.Service) map[string]string {
	if s.Env == nil {
		return map[string]string{}
	}
	return s.Env
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

func setFromSlice(ss []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		out[s] = struct{}{}
	}
	return out
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
// produced from `new` against those currently present on disk under
// `.devm/templates/`. The on-disk scripts ARE the snapshot of last-applied
// template state — we don't need a separate snapshot field.
//
// Emits a Change per template that would differ from its on-disk
// installer (including newly-added templates) and a Change per on-disk
// installer that is no longer in the new config (removal).
func ComputeTemplateChanges(new schema.Config, repoRoot string) ([]Change, error) {
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

	templatesDir := filepath.Join(repoRoot, ".devm", "templates")
	entries, err := os.ReadDir(templatesDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("compute templates: readdir: %w", err)
	}
	onDisk := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".sh" {
			continue
		}
		b, rErr := os.ReadFile(filepath.Join(templatesDir, e.Name()))
		if rErr != nil {
			return nil, fmt.Errorf("compute templates: read %s: %w", e.Name(), rErr)
		}
		onDisk[e.Name()] = b
	}

	var out []Change
	// Additions + changes.
	for path, content := range desired {
		base := filepath.Base(path)
		m := desiredMeta[base]
		existing, ok := onDisk[base]
		if !ok {
			out = append(out, Change{
				Kind: KindTemplateChange, Service: m.Service, Detail: m.Output,
				New: "installed",
			})
			continue
		}
		if string(existing) != content {
			out = append(out, Change{
				Kind: KindTemplateChange, Service: m.Service, Detail: m.Output,
				Old: "previous", New: "updated",
			})
		}
	}
	// Removals.
	for base := range onDisk {
		if _, ok := desired[filepath.Join(templatesDir, base)]; ok {
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
