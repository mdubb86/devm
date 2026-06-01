package orchestrator

import (
	"sort"
	"strconv"

	"github.com/mtwaage/devm/internal/schema"
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
	// Env is injected at each `sbx exec` (see EnvArgs), so a changed
	// value is picked up by the next `devm shell` with no recreate —
	// hence LIVE. ApplyLive takes no sbx action for env kinds.
	KindEnvAdd:        BucketLive,
	KindEnvRemove:     BucketLive,
	KindEnvChange:     BucketLive,
	KindStartupChange: BucketStopShell,
	KindInstallChange:  BucketTeardownShell,
	KindMaskChange:     BucketTeardownShell,
	KindImageChange:    BucketTeardownShell,
	KindIdentityChange: BucketTeardownShell,
	KindTemplateChange: BucketLive,
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
		oldPort := old.Services[name].Canonical
		newPort := new.Services[name].Canonical
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
// install, masks (per service), image, identity. Within each section,
// service names are sorted alphabetically for determinism.
func ComputeAllChanges(old, new schema.Config) []Change {
	var out []Change
	out = append(out, ComputePortChanges(old, new)...)
	out = append(out, ComputeNetworkChanges(old, new)...)
	out = append(out, computeEnvChanges(old, new)...)
	out = append(out, computeStartupChanges(old, new)...)
	out = append(out, computeInstallChanges(old, new)...)
	out = append(out, computeMaskChanges(old, new)...)
	out = append(out, computeImageChange(old, new)...)
	out = append(out, computeIdentityChange(old, new)...)
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
