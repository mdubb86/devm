package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/serviceapi/sshkeys"
)

// Destructiveness selects between preserving the VM (stop) and
// destroying it (teardown).
type Destructiveness int

const (
	// StopPreserve stops the VM via the daemon supervisor but keeps
	// the VM disk intact.
	StopPreserve Destructiveness = iota
	// StopDestroy stops the VM and deletes its disk image entirely.
	StopDestroy
)

// StopVMClient is the subset of *serviceapi.Client this orchestrator
// uses. Defined here to allow test fakes; the real client satisfies it.
type StopVMClient interface {
	StopVM(ctx context.Context, name string, destroy bool) error
}

// StopDeps wires collaborators for RunStop. Out sinks the human-
// readable progress lines RunStop emits; tests inject bytes.Buffer.
// When Out is nil, os.Stderr is used.
type StopDeps struct {
	Tart             *tart.Tart
	ServiceAPIClient StopVMClient
	Out              io.Writer
	// Ident is the daemon identity (prod vs. e2e) this stop/teardown
	// operates under — threaded into the state-snapshot/sshkeys/
	// ssh_config calls RunStop makes, instead of a hardcoded
	// identity.Prod.
	Ident identity.Config
}

// RunStop implements both `devm stop` (mode=StopPreserve) and
// `devm teardown` (mode=StopDestroy). Return code: 0 on success.
//
// name is both the daemon admin StopVM key and the Tart VM name used
// for disk deletion on teardown.
//
// The caller is responsible for any interactive confirmation before
// invoking RunStop; this function always proceeds unconditionally.
func RunStop(ctx context.Context, d StopDeps, name string, mode Destructiveness) (int, error) {
	if d.Out == nil {
		d.Out = os.Stderr
	}

	// Ask the daemon supervisor to stop the VM. Best-effort: continue
	// silently on failure so teardown can still delete the disk.
	// Common case: daemon is down, or the VM was never supervised by
	// THIS daemon process. Either way, the user's intent ("stop and
	// destroy") is still achievable via tart.Delete below.
	//
	// name is forwarded so the daemon can `tart stop <name>` first for a
	// graceful guest shutdown before SIGTERM'ing the tart-run process —
	// otherwise in-flight guest writes aren't flushed and files from just
	// before stop are lost (Bug J).
	//
	// destroy mirrors mode: StopDestroy tells the daemon to purge this
	// project's egress policy state and denial counts along with the
	// VM, so a future project reusing the name starts clean; StopPreserve
	// keeps them for the next start.
	_ = d.ServiceAPIClient.StopVM(ctx, name, mode == StopDestroy)

	if err := EmitSSHConfig(ctx, d.Ident, d.Tart); err != nil {
		log.Printf("ssh_config emit failed after stop: %v", err)
	}

	if mode == StopDestroy {
		// Terminate this project's mutagen sessions before the VM disk
		// is deleted. Termination is a mutagen-daemon-local operation
		// (no guest sshd needed), but runs before Delete for ordering
		// consistency with the flush+pause /vm/stop just issued.
		// Best-effort: a failure leaves the sessions dangling in the
		// mutagen daemon's state, cleaned up on its next restart.
		if err := mutagenTeardownFn(d, name); err != nil {
			log.Printf("mutagen teardown phase for %s: %v (continuing)", name, err)
		}

		if err := d.Tart.Delete(ctx, name); err != nil {
			// "VM does not exist" is the desired end state; treat
			// as success. tart's stderr for absent VMs is stable:
			// "the specified VM \"<name>\" does not exist".
			if strings.Contains(err.Error(), "does not exist") {
				fmt.Fprintf(d.Out, "VM %s already absent.\n", name)
			} else {
				return -1, fmt.Errorf("tart delete %s: %w", name, err)
			}
		} else {
			fmt.Fprintf(d.Out, "Deleted VM %s.\n", name)
		}

		// Remove the daemon's last-applied-cfg snapshot now that the VM
		// is gone. Without this, a recreated project with the same
		// name inherits a stale baseline and reconcile diffs
		// against the OLD vm's config instead of treating everything as
		// new. Best-effort: log but don't fail the teardown over it —
		// a stray snapshot only affects the first reconcile after
		// recreation (degrades to the same "full diff" fallback used
		// when no snapshot exists at all).
		if err := serviceapi.RemoveStateCfg(d.Ident, name); err != nil {
			fmt.Fprintf(d.Out, "warning: remove state snapshot for %s: %v\n", name, err)
		}

		// Remove the per-project SSH key material. Without this, a
		// recreated project with the same name inherits stale
		// SSH keys. Best-effort: log but continue — leaked SSH material
		// is inert without a matching authorized_keys in a fresh VM.
		if err := sshkeys.Remove(d.Ident, name); err != nil {
			log.Printf("sshkeys.Remove(%s): %v", name, err)
		}

		// Reap orchestration strays that outlive tart's own delete.
		// Runs on both the "Deleted" and "already absent" branches:
		// belt-and-suspenders when tart cleaned up cleanly, actual fix
		// when tart's registry and disk diverged (a v0.10.0-created
		// VM whose config newer tart can't parse — `tart delete`
		// reports "does not exist" while ~/.tart/vms/<name>/ still
		// holds the disk).
		reapPerProjectArtifacts(d.Out, d.Ident, name)
	} else {
		fmt.Fprintf(d.Out, "Stopped VM %s. Disk preserved.\n", name)
	}

	return 0, nil
}

// mutagenTeardownFn is the test-injection seam for the mutagen
// session-termination step RunStop runs (on StopDestroy) before the
// VM disk delete. Production always terminates the project's real
// mutagen sessions; tests substitute a fake to verify sequencing
// without a live mutagen daemon.
var mutagenTeardownFn = func(d StopDeps, name string) error {
	mutagenBin, err := mutagen.Ensure(d.Ident.RuntimeDir())
	if err != nil {
		return fmt.Errorf("mutagen: extract binary: %w", err)
	}
	return serviceapi.TeardownPhase(serviceapi.NewMutagenCLI(d.Ident, mutagenBin, mutagen.OSExec), name)
}

// reapPerProjectArtifacts removes the three orchestration files
// devm owns per-project that outlive tart's own delete: the tart VM
// disk dir (in case tart's registry disagreed with its filesystem),
// the iron-proxy config, and the softnet control socket. Each is
// best-effort — a missing file is not an error; a permission-denied
// (or anything else) is logged as a warning line on stdout but does
// not fail the teardown, which has already accomplished the user's
// main intent of getting the VM out of the way.
//
// Never touches user data: volumes, secrets, workspace files, and
// log files stay in place. Volume preservation across teardown is a
// documented feature; `devm purge` reaps abandoned volume dirs on a
// separate cadence.
func reapPerProjectArtifacts(out io.Writer, cfg identity.Config, name string) {
	for _, art := range perProjectArtifactPaths(cfg, name) {
		removeArtifact(out, art)
	}
}

// perProjectArtifactPaths lists the on-disk paths reapPerProjectArtifacts
// tries to remove. Kept factored so tests can pin the exact set without
// re-implementing the path derivation.
func perProjectArtifactPaths(cfg identity.Config, name string) []string {
	paths := []string{
		filepath.Join(cfg.RuntimeDir(), "iron-proxy", name+".yaml"),
		serviceapi.SoftnetControlSock(cfg, name),
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".tart", "vms", name))
	}
	return paths
}

// removeArtifact removes one path. RemoveAll handles both files and
// directories, and returns nil for a missing path — the common no-op
// case when tart cleaned up itself.
func removeArtifact(out io.Writer, path string) {
	if err := os.RemoveAll(path); err != nil {
		fmt.Fprintf(out, "warning: remove %s: %v\n", path, err)
	}
}

