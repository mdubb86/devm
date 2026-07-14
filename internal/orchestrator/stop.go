package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/serviceapi/sshconfig"
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
	StopVM(ctx context.Context, projectID, vmName string) error
}

// StopDeps wires collaborators for RunStop. In and Out drive the
// confirmation prompt; tests inject strings.NewReader / bytes.Buffer.
// When In is nil, os.Stdin is used; when Out is nil, os.Stderr.
type StopDeps struct {
	Tart             *tart.Tart
	ServiceAPIClient StopVMClient
	In               io.Reader
	Out              io.Writer
}

// RunStop implements both `devm stop` (mode=StopPreserve) and
// `devm teardown` (mode=StopDestroy). autoApprove skips the
// interactive prompt. Return code: 0 on success; 1 on user refusal.
//
// projectID is passed to the daemon admin StopVM call.
// sandboxName is the Tart VM name used for disk deletion on teardown.
//
// The ctx parameter is currently advisory — it is accepted for
// signature consistency with RunShell, but the interactive prompt
// will block on stdin indefinitely; users cancel by ctrl-c at the
// terminal, which kills the devm process.
func RunStop(ctx context.Context, d StopDeps, projectID, sandboxName string, mode Destructiveness, autoApprove bool) (int, error) {
	if d.In == nil {
		d.In = os.Stdin
	}
	if d.Out == nil {
		d.Out = os.Stderr
	}

	if !autoApprove {
		approved, err := promptStopConfirm(d.In, d.Out, sandboxName, mode)
		if err != nil {
			return -1, err
		}
		if !approved {
			fmt.Fprintln(d.Out, "aborted")
			return 1, nil
		}
	}

	// Ask the daemon supervisor to stop the VM. Best-effort: continue
	// silently on failure so teardown can still delete the disk.
	// Common case: daemon is down, or the VM was never supervised by
	// THIS daemon process. Either way, the user's intent ("stop and
	// destroy") is still achievable via tart.Delete below.
	//
	// vmName is forwarded so the daemon can `tart stop <vmName>` first
	// for a graceful guest shutdown before SIGTERM'ing the tart-run
	// process — otherwise in-flight guest writes aren't flushed and
	// files from just before stop are lost (Bug J).
	_ = d.ServiceAPIClient.StopVM(ctx, projectID, sandboxName)

	if err := sshconfig.EmitCurrent(ctx, d.Tart,
		func(id string) (any, error) { return serviceapi.ReadStateSnapshot(id) },
		serviceapi.StateDir); err != nil {
		log.Printf("ssh_config emit failed after stop: %v", err)
	}

	if mode == StopDestroy {
		if err := d.Tart.Delete(ctx, sandboxName); err != nil {
			// "VM does not exist" is the desired end state; treat
			// as success. tart's stderr for absent VMs is stable:
			// "the specified VM \"<name>\" does not exist".
			if strings.Contains(err.Error(), "does not exist") {
				fmt.Fprintf(d.Out, "VM %s already absent.\n", sandboxName)
			} else {
				return -1, fmt.Errorf("tart delete %s: %w", sandboxName, err)
			}
		} else {
			fmt.Fprintf(d.Out, "Deleted VM %s.\n", sandboxName)
		}

		// Remove the daemon's last-applied-cfg snapshot now that the VM
		// is gone. Without this, a recreated project with the same
		// projectID inherits a stale baseline and reconcile diffs
		// against the OLD vm's config instead of treating everything as
		// new. Best-effort: log but don't fail the teardown over it —
		// a stray snapshot only affects the first reconcile after
		// recreation (degrades to the same "full diff" fallback used
		// when no snapshot exists at all).
		if err := serviceapi.RemoveStateCfg(projectID); err != nil {
			fmt.Fprintf(d.Out, "warning: remove state snapshot for %s: %v\n", projectID, err)
		}
	} else {
		fmt.Fprintf(d.Out, "Stopped VM %s. Disk preserved.\n", sandboxName)
	}

	return 0, nil
}

// promptStopConfirm prints the action description and asks for [y/N].
// Returns true on "y"/"yes" (case-insensitive); false otherwise.
func promptStopConfirm(in io.Reader, out io.Writer, name string, mode Destructiveness) (bool, error) {
	action := "Stop"
	if mode == StopDestroy {
		action = "Tear down"
	}
	fmt.Fprintf(out, "%s VM %s? [y/N]: ", action, name)
	br := bufio.NewReader(in)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	resp := strings.ToLower(strings.TrimSpace(line))
	return resp == "y" || resp == "yes", nil
}
