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
	// BucketRestartVM — requires VM stop + cold start, no teardown; the
	// provisioner re-establishes the change on the next boot. Used by
	// KindStartupChange: a `startup:` edit is re-rendered into
	// /opt/devm/startup.sh but only takes effect on the guest's next boot.
	BucketRestartVM
	BucketTeardownVM // requires VM delete + cold start (volumes/install rerun)
	// BucketEgressRestart — regenerate iron-proxy config and respawn
	// iron-proxy on the same MAC_HOST:port. No VM cycle. Parallel to
	// BucketLive and BucketTeardownVM (not a severity step): a single
	// reconcile can produce changes in any combination of buckets.
	BucketEgressRestart
)

func (b Bucket) String() string {
	switch b {
	case BucketLive:
		return "live"
	case BucketRestartVM:
		return "restart"
	case BucketTeardownVM:
		return "teardown"
	case BucketEgressRestart:
		return "egress-restart"
	}
	return "unknown"
}

// Op categorizes a per-field diff entry (KindRepoChange, KindVolumeChange)
// as an add of a whole map entry, a remove of a whole map entry, or a
// mutation of one field on an entry present in both old and new.
type Op int

const (
	OpAdd Op = iota
	OpRemove
	OpMutate
)

func (o Op) String() string {
	switch o {
	case OpAdd:
		return "add"
	case OpRemove:
		return "remove"
	case OpMutate:
		return "mutate"
	}
	return "unknown"
}

// RepoOp and VolumeOp name Op at the call sites that produce
// KindRepoChange / KindVolumeChange entries; both share Op's
// OpAdd/OpRemove/OpMutate values.
type RepoOp = Op
type VolumeOp = Op

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
	// KindPackageAdd / KindPackageRemove fire once per apt package
	// present in exactly one of old/new `packages:` (set semantics —
	// reordering the list is a no-op). Key = package name. BucketLive:
	// a running VM converges via apt in a transient egress window; a
	// stopped VM converges in the next boot's open window.
	KindPackageAdd
	KindPackageRemove
	// KindMaskChange fires when the top-level `masks:` list differs
	// between old and new config. Emitted once per changed path; Key
	// = mask path (workspace-relative), Old = path in old config (or
	// empty if new-only), New = path in new (or empty if removed).
	// Bucket: BucketLive — mask add/remove is a guest-side mount --bind
	// or umount, no VM restart needed.
	KindMaskChange
	KindImageChange
	KindIdentityChange
	KindDockerToggle
	KindDiskChange
	KindMemoryChange
	KindCpuChange
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
	KindServiceDirectChange
	// KindSecret* — value-drift of a `!secret NAME` reference (same
	// declaration, different keychain value). Env-diff already covers
	// reference syntax changes; these track the resolved values.
	KindSecretAdd
	KindSecretRemove
	KindSecretChange
	// KindStartupChange fires when the ordered `startup:` command list
	// differs between old and new config. Content edits and add/remove
	// of the key both surface here; see the changeBucket comment for
	// why this is BucketRestartVM (VM stop + cold start, not a
	// teardown) rather than BucketLive.
	KindStartupChange
	// KindIronProxyDown is a synthetic change: not produced by diffing
	// old vs new config, but emitted by the reconcile handler when a
	// running VM's iron-proxy is missing or stale (see
	// serviceapi.computeProxyHealth). Carries no config drift of its
	// own — it exists purely to route through the same
	// BucketEgressRestart / AppliedIronProxy path that respawns
	// iron-proxy.
	KindIronProxyDown
	// KindVolumeChange fires when the top-level `volumes:` map differs
	// between old and new config. One Change per changed volume per
	// field: Op=OpAdd/OpRemove for whole-entry add/remove (Key=volume
	// name, Old/New=guest path), Op=OpMutate per changed field on a
	// volume present in both (Field="path"/"label"/"ignore").
	// Bucket: BucketLive — volumes hydrate via mutagen sync against an
	// already-provisioned guest directory, no VM cycle needed.
	KindVolumeChange
	// KindRepoChange fires when Config.Repos differs between old and
	// new config. One Change per changed repo entry per field:
	// Op=OpAdd/OpRemove for whole-entry add/remove (Key=repo name),
	// Op=OpMutate per changed field on a repo present in both
	// (Field="URL"/"Secret"/"Label"/"Volume"/"Primary"/"Ignore").
	// Bucket: BucketRestartVM for URL/Secret (iron-proxy clones the
	// repo at VM boot using these values); BucketLive for every other
	// field (label/ignore/volume/primary only affect the mutagen
	// session, not the clone). See Change.Bucket().
	KindRepoChange
	// KindSSHEndpointHealed is a synthetic change: emitted by the
	// reconcile handler after it detected the project's :22 answered
	// with a foreign SSH host key (a cross-wired ProjectIP) and healed
	// it by reallocating the IP and rebinding listeners. Old = the
	// cross-wired IP, New = the replacement.
	KindSSHEndpointHealed
)

// changeBucket is the single source of truth that maps each ChangeKind
// to its bucket. Bucket() and the diff/bucket table in the design spec
// both reference this map.
var changeBucket = map[ChangeKind]Bucket{
	KindPortAdd:       BucketLive,
	KindPortRemove:    BucketLive,
	KindPortChange:    BucketLive,
	KindNetworkAdd:    BucketEgressRestart,
	KindNetworkRemove: BucketEgressRestart,
	// Env changes are applied by rewriting the unit file and restarting
	// the service via tart exec — no VM recreate needed.
	KindEnvAdd:    BucketLive,
	KindEnvRemove: BucketLive,
	KindEnvChange: BucketLive,
	// install: commands happen on first boot; can't re-run cleanly on a
	// half-installed VM.
	KindInstallChange: BucketTeardownVM,
	// apt is idempotent and declarative — unlike install: scripts,
	// package changes converge on a live VM.
	KindPackageAdd:    BucketLive,
	KindPackageRemove: BucketLive,
	// virtio-fs mounts are set at tart run time; requires full recreate.
	KindMountAddRemove: BucketTeardownVM,
	// mount --bind masks are applied inside the running guest — no VM
	// restart needed.
	KindMaskChange:     BucketLive,
	KindImageChange:    BucketTeardownVM,
	KindIdentityChange: BucketTeardownVM,
	KindDockerToggle:   BucketTeardownVM,
	// Disk size is baked at clone / `tart set` time; grow-only, so a
	// change recreates from base and re-applies the new size.
	KindDiskChange: BucketTeardownVM,
	// Memory and Cpu are boot params applied via `tart set` on the
	// stopped VM — no full recreate required.
	KindMemoryChange:   BucketRestartVM,
	KindCpuChange:      BucketRestartVM,
	KindTemplateChange: BucketLive,
	// Path is materialized in /etc/environment (same fan-out as Env) — live.
	KindPathChange: BucketLive,
	// Service unit changes: re-render unit, daemon-reload, restart unit
	// via tart exec — no VM recreate needed.
	KindServiceExecChange:            BucketLive,
	KindServiceRestartChange:         BucketLive,
	KindServiceAfterChange:           BucketLive,
	KindServiceWorkdirChange:         BucketLive,
	KindServiceUserChange:            BucketLive,
	KindServiceSystemdOverrideChange: BucketLive,
	// Hostname: re-push routes to the daemon's guest-origin listener — live.
	KindServiceHostnameChange: BucketLive,
	// Direct: re-push routes (DNS), re-push the softnet expose map and
	// direct-host DNS set — live.
	KindServiceDirectChange: BucketLive,
	// startup: re-rendered into /opt/devm/startup.sh; a live bundle
	// re-pipe carries the new content to the guest, but it only takes
	// effect on the VM's NEXT boot (startup: is a boot hook, not a
	// running-service field) — VM stop + cold start, no teardown.
	KindStartupChange: BucketRestartVM,
	// Secrets: iron-proxy config carries resolved values; a rotation
	// requires regenerating that config and respawning iron-proxy.
	KindSecretAdd:    BucketEgressRestart,
	KindSecretRemove: BucketEgressRestart,
	KindSecretChange: BucketEgressRestart,
	// Synthetic heal signal — same bucket as the rest of the
	// egress-restart family since it's applied the same way.
	KindIronProxyDown: BucketEgressRestart,
	// Volumes hydrate via mutagen sync against an already-provisioned
	// guest directory — no VM cycle needed. Repo URL/Secret overrides
	// this default; see Change.Bucket().
	KindVolumeChange: BucketLive,
	KindRepoChange:   BucketLive,
	// Synthetic heal signal: the daemon fixed it in-place during the
	// reconcile — nothing left for the CLI to dispatch.
	KindSSHEndpointHealed: BucketLive,
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

	// Op, Field, OldValue, and NewValue are populated on KindRepoChange
	// and KindVolumeChange entries: Op categorizes the entry as an
	// add/remove of a whole map key or a mutation of one field on a key
	// present in both old and new; Field names that field (e.g. "URL",
	// "path"); OldValue/NewValue carry the typed field value (not the
	// formatted Old/New strings) for consumers that need the raw data
	// (e.g. apply-live's mutagen/repo-clone handling).
	Op                 Op
	Field              string
	OldValue, NewValue any
}

// Bucket returns the bucket this Change belongs to. Most kinds route
// solely on Kind (see changeBucket); KindRepoChange is the one
// exception — a URL or Secret mutation requires an iron-proxy /
// VM-boot re-clone (BucketRestartVM) while every other repo field only
// affects the live mutagen session (BucketLive, changeBucket's
// default for KindRepoChange).
func (c Change) Bucket() Bucket {
	if c.Kind == KindRepoChange && (c.Field == "URL" || c.Field == "Secret") {
		return BucketRestartVM
	}
	return c.Kind.Bucket()
}

// FlavorKind names the recreate flavor required to apply a set of changes.
type FlavorKind int

const (
	FlavorLiveOnly FlavorKind = iota // no recreate, only live applies
	// FlavorRestartVM — requires VM stop + cold start, no teardown.
	// Reached whenever a change sits in BucketRestartVM (e.g.
	// KindStartupChange) and nothing more severe is also pending.
	FlavorRestartVM
	FlavorTeardownVM // requires VM delete + cold start
)

// String implements fmt.Stringer so FlavorKind renders directly in %s
// format verbs (used by orchestrator's format.go and error messages).
func (f FlavorKind) String() string {
	switch f {
	case FlavorLiveOnly:
		return "live"
	case FlavorRestartVM:
		return "restart"
	case FlavorTeardownVM:
		return "teardown"
	}
	return "unknown"
}

// RecreateFlavor picks the max severity across all changes' buckets.
func RecreateFlavor(changes []Change) FlavorKind {
	max := FlavorLiveOnly
	for _, c := range changes {
		switch c.Bucket() {
		case BucketRestartVM:
			if max < FlavorRestartVM {
				max = FlavorRestartVM
			}
		case BucketTeardownVM:
			return FlavorTeardownVM // can't go higher
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
// (per service), install, startup, packages, mounts, volumes, repos,
// masks (per service), image, identity, templates, path, secrets.
// Within each section, service/volume/repo names are sorted
// alphabetically for determinism.
//
// `repoRoot` and `daemonRuntimeDir` are required by the templates diff
// to render the desired installer scripts (source reads and the
// rendering namespace, respectively). `lastAppliedTemplates` is the
// last-applied baseline (basename -> rendered content, from the
// daemon's persisted StateSnapshot); pass nil when there is none (e.g.
// cold-start with no prior snapshot), which surfaces every declared
// template as an add.
// `oldSecretHashes` is the last-applied SecretHashes map from the same
// snapshot; `newSecretHashes` is the freshly-hashed set the CLI just
// resolved. Both nil means "no secret drift to consider".
func ComputeAllChanges(
	old, new schema.Config,
	repoRoot, daemonRuntimeDir string,
	lastAppliedTemplates map[string]string,
	oldSecretHashes, newSecretHashes map[string]string,
) ([]Change, error) {
	var out []Change
	out = append(out, ComputePortChanges(old, new)...)
	out = append(out, computeNetworkChanges(old, new)...)
	out = append(out, computeGlobalEnvChanges(old, new)...)
	out = append(out, computeEnvChanges(old, new)...)
	out = append(out, computeServiceUnitChanges(old, new)...)
	out = append(out, computeDirectChanges(old, new)...)
	out = append(out, computeHostnameChanges(old, new)...)
	out = append(out, computeInstallChanges(old, new)...)
	out = append(out, computeStartupChanges(old, new)...)
	out = append(out, computePackagesChange(old, new)...)
	out = append(out, computeMountAddRemove(old, new)...)
	out = append(out, computeVolumeChanges(old, new)...)
	out = append(out, computeRepoChanges(old, new)...)
	out = append(out, computeMaskChanges(old, new)...)
	out = append(out, computeImageChange(old, new)...)
	out = append(out, computeIdentityChange(old, new)...)
	out = append(out, computeDockerChange(old, new)...)
	out = append(out, computeDiskChange(old, new)...)
	out = append(out, computeResourceChange(old, new)...)
	out = append(out, computePathChange(old, new)...)
	tmplChanges, err := ComputeTemplateChanges(new, repoRoot, daemonRuntimeDir, lastAppliedTemplates)
	if err != nil {
		return nil, err
	}
	out = append(out, tmplChanges...)
	out = append(out, computeSecretChanges(newSecretHashes, oldSecretHashes)...)
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
// /etc/environment via the devmbundle, so a global-scope change is real
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

// computeDirectChanges emits KindServiceDirectChange for services whose
// Direct field differs between old and new (covers add: absent→direct,
// remove: direct→absent, and flip).
func computeDirectChanges(old, new schema.Config) []Change {
	var out []Change
	for _, svc := range unionServiceNames(old.Services, new.Services) {
		o, n := old.Services[svc].Direct, new.Services[svc].Direct
		if o != n {
			out = append(out, Change{Kind: KindServiceDirectChange, Service: svc,
				Old: strconv.FormatBool(o), New: strconv.FormatBool(n)})
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

// computeStartupChanges emits KindStartupChange when the ordered
// `startup:` command list differs between old and new config. Compared
// as an ordered slice (like Install/Packages/Mounts) rather than by
// membership — reordering the boot commands is itself a meaningful
// change.
func computeStartupChanges(old, new schema.Config) []Change {
	if stringSliceEqual(old.Startup, new.Startup) {
		return nil
	}
	return []Change{{Kind: KindStartupChange}}
}

// PackageDrift diffs the `packages:` list between old and new config as a
// set — apt packages are unordered, so reordering the list is a no-op.
// Returns the package names present only in new (adds) and only in old
// (removes), both sorted for determinism. Shared by computePackagesChange
// (reconcile's live-VM diff) and the cold-start provisioner, which converges
// the same drift in a stopped VM's next boot open window instead.
func PackageDrift(old, new schema.Config) (adds, removes []string) {
	oldSet := make(map[string]struct{}, len(old.Packages))
	for _, p := range old.Packages {
		oldSet[p] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(new.Packages))
	for _, p := range new.Packages {
		newSet[p] = struct{}{}
	}
	for p := range newSet {
		if _, ok := oldSet[p]; !ok {
			adds = append(adds, p)
		}
	}
	for p := range oldSet {
		if _, ok := newSet[p]; !ok {
			removes = append(removes, p)
		}
	}
	sort.Strings(adds)
	sort.Strings(removes)
	return adds, removes
}

// computePackagesChange diffs the `packages:` list as a set — apt
// packages are unordered, so reordering the list is a no-op. Emits one
// KindPackageAdd/KindPackageRemove per package present in exactly one
// of old/new, adds sorted before removes.
func computePackagesChange(old, new schema.Config) []Change {
	adds, removes := PackageDrift(old, new)
	var out []Change
	for _, p := range adds {
		out = append(out, Change{Kind: KindPackageAdd, Key: p, New: p})
	}
	for _, p := range removes {
		out = append(out, Change{Kind: KindPackageRemove, Key: p, Old: p})
	}
	return out
}

func computeMountAddRemove(old, new schema.Config) []Change {
	if stringSliceEqual(old.Mounts, new.Mounts) {
		return nil
	}
	return []Change{{Kind: KindMountAddRemove}}
}

// computeVolumeChanges emits one KindVolumeChange per changed
// Config.Volumes entry. A key present in exactly one of old/new emits
// a single OpAdd/OpRemove Change; a key present in both emits one
// OpMutate Change per changed field (path, label, ignore). Sorted by
// name for deterministic output.
func computeVolumeChanges(old, new schema.Config) []Change {
	var out []Change
	for _, n := range unionVolumeNames(old.Volumes, new.Volumes) {
		oldVol, oldOk := old.Volumes[n]
		newVol, newOk := new.Volumes[n]
		switch {
		case !oldOk && newOk:
			out = append(out, Change{Kind: KindVolumeChange, Op: OpAdd, Key: n,
				New: newVol.Path, NewValue: newVol})
		case oldOk && !newOk:
			out = append(out, Change{Kind: KindVolumeChange, Op: OpRemove, Key: n,
				Old: oldVol.Path, OldValue: oldVol})
		default:
			out = append(out, volumeFieldChanges(n, oldVol, newVol)...)
		}
	}
	return out
}

// volumeFieldChanges diffs a single volume present in both old and new,
// emitting one OpMutate Change per field that differs.
func volumeFieldChanges(name string, o, n schema.Volume) []Change {
	var out []Change
	if o.Path != n.Path {
		out = append(out, Change{Kind: KindVolumeChange, Op: OpMutate, Key: name, Field: "path",
			Old: o.Path, New: n.Path, OldValue: o.Path, NewValue: n.Path})
	}
	if !stringPtrEqual(o.Label, n.Label) {
		out = append(out, Change{Kind: KindVolumeChange, Op: OpMutate, Key: name, Field: "label",
			Old: formatStringPtr(o.Label), New: formatStringPtr(n.Label),
			OldValue: o.Label, NewValue: n.Label})
	}
	if !stringSliceEqual(o.Ignore, n.Ignore) {
		out = append(out, Change{Kind: KindVolumeChange, Op: OpMutate, Key: name, Field: "ignore",
			Old: strings.Join(o.Ignore, ","), New: strings.Join(n.Ignore, ","),
			OldValue: o.Ignore, NewValue: n.Ignore})
	}
	return out
}

// computeRepoChanges emits one KindRepoChange per changed
// Config.Repos entry. A key present in exactly one of old/new emits a
// single OpAdd/OpRemove Change; a key present in both emits one
// OpMutate Change per changed field (URL, Secret, Label, Volume,
// Primary, Ignore). Sorted by name for deterministic output.
func computeRepoChanges(old, new schema.Config) []Change {
	var out []Change
	for _, n := range unionRepoNames(old.Repos, new.Repos) {
		oldRepo, oldOk := old.Repos[n]
		newRepo, newOk := new.Repos[n]
		switch {
		case !oldOk && newOk:
			out = append(out, Change{Kind: KindRepoChange, Op: OpAdd, Key: n})
		case oldOk && !newOk:
			out = append(out, Change{Kind: KindRepoChange, Op: OpRemove, Key: n})
		default:
			out = append(out, repoFieldChanges(n, oldRepo, newRepo)...)
		}
	}
	return out
}

// repoFieldChanges diffs a single repo present in both old and new,
// emitting one OpMutate Change per field that differs. Field names
// match the RepoConfig Go field names (not the yaml tags) so
// Change.Bucket()'s URL/Secret check and any downstream apply-live
// switch can compare against them directly.
func repoFieldChanges(name string, o, n schema.RepoConfig) []Change {
	var out []Change
	if !stringPtrEqual(o.URL, n.URL) {
		out = append(out, Change{Kind: KindRepoChange, Op: OpMutate, Key: name, Field: "URL",
			Old: formatStringPtr(o.URL), New: formatStringPtr(n.URL),
			OldValue: o.URL, NewValue: n.URL})
	}
	if o.Secret != n.Secret {
		out = append(out, Change{Kind: KindRepoChange, Op: OpMutate, Key: name, Field: "Secret",
			Old: o.Secret, New: n.Secret, OldValue: o.Secret, NewValue: n.Secret})
	}
	if !stringPtrEqual(o.Label, n.Label) {
		out = append(out, Change{Kind: KindRepoChange, Op: OpMutate, Key: name, Field: "Label",
			Old: formatStringPtr(o.Label), New: formatStringPtr(n.Label),
			OldValue: o.Label, NewValue: n.Label})
	}
	if !boolPtrEqual(o.Volume, n.Volume) {
		out = append(out, Change{Kind: KindRepoChange, Op: OpMutate, Key: name, Field: "Volume",
			Old: formatBoolPtr(o.Volume), New: formatBoolPtr(n.Volume),
			OldValue: o.Volume, NewValue: n.Volume})
	}
	if !boolPtrEqual(o.Primary, n.Primary) {
		out = append(out, Change{Kind: KindRepoChange, Op: OpMutate, Key: name, Field: "Primary",
			Old: formatBoolPtr(o.Primary), New: formatBoolPtr(n.Primary),
			OldValue: o.Primary, NewValue: n.Primary})
	}
	if !stringSliceEqual(o.Ignore, n.Ignore) {
		out = append(out, Change{Kind: KindRepoChange, Op: OpMutate, Key: name, Field: "Ignore",
			Old: strings.Join(o.Ignore, ","), New: strings.Join(n.Ignore, ","),
			OldValue: o.Ignore, NewValue: n.Ignore})
	}
	return out
}

// unionVolumeNames returns the sorted union of keys across both
// Volumes maps, for deterministic diff-walk ordering.
func unionVolumeNames(a, b map[string]schema.Volume) []string {
	set := make(map[string]struct{}, len(a)+len(b))
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

// unionRepoNames returns the sorted union of keys across both Repos
// maps, for deterministic diff-walk ordering.
func unionRepoNames(a, b map[string]schema.RepoConfig) []string {
	set := make(map[string]struct{}, len(a)+len(b))
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

// stringPtrEqual compares two string pointers: both nil is equal, one
// nil and one non-nil is not equal, and both non-nil compares the
// dereferenced values.
func stringPtrEqual(a, b *string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	return *a == *b
}

// boolPtrEqual compares two bool pointers: both nil is equal, one nil
// and one non-nil is not equal, and both non-nil compares the
// dereferenced values.
func boolPtrEqual(a, b *bool) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	return *a == *b
}

// formatStringPtr renders a *string for a Change's Old/New display:
// empty string when nil, the dereferenced value otherwise.
func formatStringPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// formatBoolPtr renders a *bool for a Change's Old/New display: empty
// string when nil, "true"/"false" otherwise.
func formatBoolPtr(p *bool) string {
	if p == nil {
		return ""
	}
	return strconv.FormatBool(*p)
}

// computeMaskChanges emits one KindMaskChange per path that
// differs between old and new. Removes have Old set, New empty;
// adds have Old empty, New set. Sorted by path for deterministic
// output.
func computeMaskChanges(old, new schema.Config) []Change {
	oldSet := map[string]struct{}{}
	newSet := map[string]struct{}{}
	for _, p := range old.Masks {
		oldSet[p] = struct{}{}
	}
	for _, p := range new.Masks {
		newSet[p] = struct{}{}
	}
	all := map[string]struct{}{}
	for p := range oldSet {
		all[p] = struct{}{}
	}
	for p := range newSet {
		all[p] = struct{}{}
	}
	if len(all) == 0 {
		return nil
	}
	sorted := make([]string, 0, len(all))
	for p := range all {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	var out []Change
	for _, p := range sorted {
		_, oldHas := oldSet[p]
		_, newHas := newSet[p]
		if oldHas == newHas {
			continue // both set or both unset → no change
		}
		change := Change{Kind: KindMaskChange, Key: p}
		if oldHas {
			change.Old = p
		}
		if newHas {
			change.New = p
		}
		out = append(out, change)
	}
	return out
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

// effectiveDiskGB is the VM disk size a config targets: the explicit
// `disk:` override when set, else the base image default. Comparing
// effective sizes means adding `disk: 32G` (equal to the default) is
// not treated as a change.
//
// schema.ParseDiskSize's error is intentionally ignored — Config.Validate
// at load time rejects malformed strings before this ever runs.
func effectiveDiskGB(c schema.Config) int {
	if c.Disk != nil {
		n, _ := schema.ParseDiskSize(*c.Disk)
		return n
	}
	return schema.DefaultDiskSizeGB
}

func computeDiskChange(old, new schema.Config) []Change {
	o, n := effectiveDiskGB(old), effectiveDiskGB(new)
	if o == n {
		return nil
	}
	return []Change{{
		Kind: KindDiskChange,
		Old:  fmt.Sprintf("%dG", o),
		New:  fmt.Sprintf("%dG", n),
	}}
}

// computeResourceChange diffs Memory and Cpu independently, emitting
// one Change per field that transitioned (nil→non-nil, non-nil→nil,
// or non-nil→different-non-nil).
//
// Display formatting: nil is rendered as "(default)" so a
// nil→non-nil transition reads "(default) → 8G".
func computeResourceChange(old, new schema.Config) []Change {
	var out []Change

	if !memoryEqual(old.Memory, new.Memory) {
		out = append(out, Change{
			Kind: KindMemoryChange,
			Old:  formatMemory(old.Memory),
			New:  formatMemory(new.Memory),
		})
	}

	if !cpuEqual(old.Cpu, new.Cpu) {
		out = append(out, Change{
			Kind: KindCpuChange,
			Old:  formatCpu(old.Cpu),
			New:  formatCpu(new.Cpu),
		})
	}

	return out
}

func memoryEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func cpuEqual(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func formatMemory(p *string) string {
	if p == nil {
		return "(default)"
	}
	return *p
}

func formatCpu(p *int) string {
	if p == nil {
		return "(default)"
	}
	return fmt.Sprintf("%d", *p)
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

// computeSecretChanges diffs a {name: hash} map of resolved secret
// values. Compared against the last-applied snapshot's SecretHashes,
// this catches keychain-value rotation that the reference-syntax
// env-diff misses.
//
// Adds and removes cover the ref-lifecycle too (a new `!secret NAME`
// entry means newHashes has NAME that oldHashes doesn't), so
// KindSecret* is the canonical bucket-carrier for iron-proxy-restart;
// KindEnv* still fires for the env reference itself but is bucketed
// as BucketLive.
func computeSecretChanges(newHashes, oldHashes map[string]string) []Change {
	names := make(map[string]struct{}, len(newHashes)+len(oldHashes))
	for n := range newHashes {
		names[n] = struct{}{}
	}
	for n := range oldHashes {
		names[n] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	var out []Change
	for _, n := range sorted {
		nv, nOk := newHashes[n]
		ov, oOk := oldHashes[n]
		switch {
		case nOk && !oOk:
			out = append(out, Change{Kind: KindSecretAdd, Key: n})
		case !nOk && oOk:
			out = append(out, Change{Kind: KindSecretRemove, Key: n})
		case nOk && oOk && nv != ov:
			out = append(out, Change{Kind: KindSecretChange, Key: n})
		}
	}
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
func ComputeTemplateChanges(new schema.Config, repoRoot, daemonRuntimeDir string, lastApplied map[string]string) ([]Change, error) {
	desired, err := render.RenderTemplates(new, repoRoot, daemonRuntimeDir, new.Project.Name)
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
