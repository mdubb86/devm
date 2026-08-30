package serviceapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
)

// PackagesApplier is the daemon-internal contract for converging apt
// packages on a running VM. Tests fake it in the reconcile handler.
type PackagesApplier interface {
	ApplyPackages(ctx context.Context, projectID string, snapCfg schema.Config, macCwd string, adds, removes []string) error
}

// realPackagesApplier is the production impl, wired by runner.go.
type realPackagesApplier struct {
	tr *tart.Tart

	// execScript is the test-injection seam for running the apt
	// converge script inside the guest. nil defaults to the
	// tart-backed implementation (tartExecScript). Same seam style as
	// spawnIronProxyFn.
	execScript func(ctx context.Context, vmName, script string) (int, string)
}

var _ PackagesApplier = (*realPackagesApplier)(nil)

// aptEgressHintSignature is the substring apt-get's own stderr carries
// when a mirror fetch is blocked by devm's egress policy — apt reports
// the self-describing 403 devm's PolicyAuthority hands back verbatim
// (see policyauthority.go). Checking for it lets the hint attach only
// when it's actually the likely cause, without devm inspecting the
// script's traffic itself.
const aptEgressHintSignature = "403"

// aptEgressHint is appended to a converge failure whose stderr carries
// aptEgressHintSignature, naming both fixes.
const aptEgressHint = "apt egress may be blocked by network.allow — add deb.debian.org and security.debian.org, or open a `devm passthrough` window and re-run (see devm denials)"

// ApplyPackages converges the guest's apt package set by execing the
// converge script under the project's CURRENT egress policy — no
// allowlist widening, no restore. Egress policy is exactly what the
// user wrote in network.allow; if the Debian mirrors aren't in it, apt
// fails with devm's self-describing 403s and this call fails loud with
// a hint naming both fixes (`devm denials` shows the blocked mirrors).
//
// Caller contract: ApplyPackages does not acquire the project lock
// itself. The caller (the /vm/reconcile handler, via
// locks.Lock(projectID)) must hold it for the entire call.
func (a *realPackagesApplier) ApplyPackages(ctx context.Context, projectID string, snapCfg schema.Config, macCwd string, adds, removes []string) error {
	script := render.AptConvergeScript(adds, removes)
	if script == "" {
		return nil
	}

	exitCode, stderr := a.exec()(ctx, projectID, script)
	if exitCode != 0 {
		err := fmt.Errorf("apply packages: apt exit %d: %s", exitCode, stderr)
		if strings.Contains(stderr, aptEgressHintSignature) {
			err = fmt.Errorf("%w (%s)", err, aptEgressHint)
		}
		return err
	}
	return nil
}

// exec returns execScript, defaulting to the tart-backed implementation.
func (a *realPackagesApplier) exec() func(ctx context.Context, vmName, script string) (int, string) {
	if a.execScript != nil {
		return a.execScript
	}
	return a.tartExecScript
}

// tartExecScript is the production execScript: pipes script over `tart
// exec -i` as a bash script, capturing exit code and stderr.
func (a *realPackagesApplier) tartExecScript(ctx context.Context, vmName, script string) (int, string) {
	r := a.tr.ExecStdin(ctx, vmName, strings.NewReader(script), []string{"bash", "-e", "-o", "pipefail"})
	return r.ExitCode, r.Stderr
}
