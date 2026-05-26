package orchestrator

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
	KindEnvAdd:         BucketStopShell,
	KindEnvRemove:      BucketStopShell,
	KindEnvChange:      BucketStopShell,
	KindStartupChange:  BucketStopShell,
	KindInstallChange:  BucketTeardownShell,
	KindMaskChange:     BucketTeardownShell,
	KindImageChange:    BucketTeardownShell,
	KindIdentityChange: BucketTeardownShell,
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
