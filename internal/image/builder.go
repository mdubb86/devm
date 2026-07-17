// Package image manages the Tart base-image build pipeline. The
// daemon calls BuildBaseImage during `devm install` / `devm upgrade`
// when NeedsBuild returns true.
package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	baseimage "github.com/mdubb86/devm/image"
	"github.com/mdubb86/devm/internal/schema"
)

// BaseImageName is the Tart VM name we build into.
const BaseImageName = "devm-base"

// defaultTemplate is the Tart image devm-base clones from when the
// TEMPLATE environment variable isn't set.
const defaultTemplate = "ghcr.io/cirruslabs/debian:latest"

// cleanupScript runs inside the VM (via `tart exec … sudo bash -c`)
// once the rename-on-boot one-shot has fired and its effect has been
// verified. It removes the transient rename machinery installed by
// provision-base.sh so the saved image ships clean — no leftover
// unit, script, or state file referencing the pre-rename setup.
const cleanupScript = `
systemctl disable devm-rename-user.service 2>/dev/null || true
rm -f /etc/systemd/system/devm-rename-user.service
rm -f /etc/systemd/system/multi-user.target.wants/devm-rename-user.service
rm -f /usr/local/bin/devm-rename-user
rm -f /var/lib/devm/user-renamed
rmdir /var/lib/devm 2>/dev/null || true
systemctl daemon-reload
`

// definitionVersion bumps DefinitionHash's output independent of the
// embedded script contents. Bump this when the build *procedure*
// itself changes (step order, new tart flags, etc.) so a previously
// built devm-base gets rebuilt even though provision-base.sh and
// cleanupScript are byte-for-byte unchanged.
const definitionVersion = "v2-boot-integrity-gate-floor"

// assetStagingDir is where stageImageAssets writes the embedded
// image/ assets on the guest before ProvisionBaseScript runs — must
// match provision-base.sh's SCRIPT_DIR constant.
const assetStagingDir = "/root/devm-image-assets"

// DefinitionHash returns sha256 over the image definition: the
// embedded provisioning script, the embedded image assets it installs
// (nftables-locked.conf, devm.target), the embedded cleanup fragment,
// and definitionVersion. The definition is baked into the binary via
// //go:embed — devm doesn't depend on the image/ directory existing
// at install time.
func DefinitionHash() (string, error) {
	h := sha256.New()
	io.WriteString(h, baseimage.ProvisionBaseScript)
	h.Write([]byte{0})
	io.WriteString(h, baseimage.NftablesLockedConf)
	h.Write([]byte{0})
	io.WriteString(h, baseimage.DevmTarget)
	h.Write([]byte{0})
	io.WriteString(h, cleanupScript)
	h.Write([]byte{0})
	io.WriteString(h, definitionVersion)
	h.Write([]byte{0})
	io.WriteString(h, strconv.Itoa(schema.DefaultDiskSizeGB))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashStorePath returns the disk location where we cache the hash of
// the most recently built image. Under ~/Library/Caches (not the
// runtime dir under Application Support) so `devm uninstall`'s
// runtime-dir cleanup doesn't wipe the build cache — losing the hash
// would falsely trigger a devm-base rebuild on every subsequent
// install, which takes ~5 min and thrashes the tart image cache.
func HashStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Caches", "devm",
		"base-image.hash"), nil
}

// NeedsBuild returns (true, currentHash, nil) if the base image
// should be (re)built. False with nil err means up-to-date.
//
// Returns true if any of:
//   - The image definition hash has changed since last build
//   - The devm-base Tart VM is absent from local cache
func NeedsBuild() (bool, string, error) {
	cur, err := DefinitionHash()
	if err != nil {
		return false, "", err
	}

	storePath, err := HashStorePath()
	if err != nil {
		return false, "", err
	}
	stored, _ := os.ReadFile(storePath)
	if strings.TrimSpace(string(stored)) != cur {
		return true, cur, nil
	}

	// Hash matches; verify the VM still exists in Tart's cache.
	if !baseImageExists() {
		return true, cur, nil
	}
	return false, cur, nil
}

// baseImageExists is true if `tart list` shows the devm-base VM.
// Returns false on any error reading from Tart (we'd rather rebuild
// than skip a potentially-needed rebuild).
func baseImageExists() bool {
	ok, err := baseImageExistsCtx(context.Background())
	if err != nil {
		return false
	}
	return ok
}

// baseImageExistsCtx runs `tart list --format=json` under a bounded
// context and reports whether devm-base is listed.
func baseImageExistsCtx(ctx context.Context) (bool, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(attemptCtx, "tart", "list", "--format=json")
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	// Cheap substring scan — we just need to know the VM is listed
	// somewhere in the output by name. JSON format on Tart varies a
	// bit across versions; we don't try to fully decode.
	return strings.Contains(string(out), `"`+BaseImageName+`"`), nil
}

// runTart runs `tart <args...>` under ctx, streaming stdout/stderr to
// w. Every tart invocation in this package goes through either this
// helper or one of the more specialized ones below — all of them use
// exec.CommandContext, never a bare exec.Command.
func runTart(ctx context.Context, w io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "tart", args...)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// stageImageAssetsScript returns a shell fragment that (re)creates
// assetStagingDir on the guest and writes the embedded image/ assets
// (nftables-locked.conf, devm.target) into it. ProvisionBaseScript's
// `install -o root -g root -m 0644 "$SCRIPT_DIR/..."` lines expect
// these files to already be on disk, so this runs as a separate
// tartExecStdin call BEFORE ProvisionBaseScript itself — the script
// is piped over stdin (no on-disk image/ directory ships with the
// binary), so there's nothing else to `install` from.
func stageImageAssetsScript() string {
	return fmt.Sprintf(`set -euo pipefail
mkdir -p %s
cat > %s/nftables-locked.conf <<'DEVM_ASSET_NFTABLES_EOF'
%s
DEVM_ASSET_NFTABLES_EOF
cat > %s/devm.target <<'DEVM_ASSET_TARGET_EOF'
%s
DEVM_ASSET_TARGET_EOF
`, assetStagingDir, assetStagingDir, baseimage.NftablesLockedConf, assetStagingDir, baseimage.DevmTarget)
}

// tartExecStdin runs `tart exec -i devm-base sudo bash -s`, piping
// script to the guest bash's stdin, and streams output to w. Used for
// the provisioning step, which is long-running (apt installs) and
// worth surfacing progress for.
func tartExecStdin(ctx context.Context, w io.Writer, script string) error {
	cmd := exec.CommandContext(ctx, "tart", "exec", "-i", BaseImageName, "sudo", "bash", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// tartExecIdentity runs `tart exec devm-base id -un` under a
// per-attempt timeout and returns the trimmed identity, or "unknown"
// if the command didn't complete (guest unreachable, hung
// guest-agent handshake, etc.) — mirroring the shell script's
// `timeout 5 tart exec … id -un 2>/dev/null || echo unknown`.
func tartExecIdentity(ctx context.Context, timeout time.Duration) string {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(attemptCtx, "tart", "exec", BaseImageName, "id", "-un")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// tartExecCleanup runs the cleanup fragment inside the guest under a
// bounded context, streaming output to w.
func tartExecCleanup(ctx context.Context, timeout time.Duration, w io.Writer) error {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(attemptCtx, "tart", "exec", BaseImageName, "sudo", "bash", "-c", cleanupScript)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// waitForIP polls `tart ip devm-base` until it succeeds, sleeping
// interval between attempts, up to attempts tries. Each attempt is
// bounded by perAttemptTimeout so a hung `tart ip` surfaces as a
// failed iteration rather than blocking indefinitely — matching the
// per-attempt bound applied to every other polling loop in this
// package.
func waitForIP(ctx context.Context, attempts int, interval, perAttemptTimeout time.Duration) error {
	for i := 0; i < attempts; i++ {
		attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
		cmd := exec.CommandContext(attemptCtx, "tart", "ip", BaseImageName)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		err := cmd.Run()
		cancel()
		if err == nil {
			return nil
		}
		if i == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("VM did not report an IP within %s", time.Duration(attempts)*interval)
}

// waitForReachable polls `tart exec devm-base true`, each attempt
// bounded by perAttemptTimeout, sleeping interval between attempts,
// up to attempts tries. This is the native-Go replacement for the
// shell script's `timeout 3 tart exec … true` readiness loop.
//
// Unlike the shell script (which falls through silently on exhaustion
// and lets the subsequent identity check produce a slightly
// misleading "identity is 'unknown'" error), this returns a distinct,
// clearer error when the VM never becomes reachable.
func waitForReachable(ctx context.Context, w io.Writer, attempts int, interval, perAttemptTimeout time.Duration) error {
	for i := 1; i <= attempts; i++ {
		attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
		cmd := exec.CommandContext(attemptCtx, "tart", "exec", BaseImageName, "true")
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		err := cmd.Run()
		cancel()
		if err == nil {
			fmt.Fprintf(w, ">>> VM reachable after %ds\n", i)
			return nil
		}
		if i == attempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("VM not reachable via tart exec within %s", time.Duration(attempts)*interval)
}

// tartRunner wraps a background `tart run --no-graphics devm-base`
// process. The clean way to know the VM has stopped is to wait for
// this process to exit — tart run's lifecycle is tied to the VM being
// up, so cmd.Wait() returning IS the "VM is down" signal. We never
// poll `tart list` for a running/stopped, and we only kill the
// process as an explicit, logged escalation when that clean wait
// hangs.
type tartRunner struct {
	cmd    *exec.Cmd
	done   chan error
	exited bool
}

// startTartRun launches `tart run --no-graphics devm-base` in the
// background. Output is discarded: tart run's stdout/stderr is a
// console mirror, not useful build-progress output, and streaming it
// to w would just add noise on top of the milestone lines we print
// ourselves.
func startTartRun(ctx context.Context) (*tartRunner, error) {
	cmd := exec.CommandContext(ctx, "tart", "run", "--no-graphics", BaseImageName)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return &tartRunner{cmd: cmd, done: done}, nil
}

// killIfRunning force-stops the VM process if it hasn't already
// exited. Used as a defer'd safety net so a mid-build failure never
// leaves a zombie `tart run` behind.
func (r *tartRunner) killIfRunning() {
	if r.exited {
		return
	}
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	<-r.done
	r.exited = true
}

// powerOffAndWait sends a clean guest poweroff via `tart exec … sudo
// systemctl poweroff` (best-effort — the guest-agent exec channel
// often closes mid-shutdown, which surfaces as a non-nil error here
// even on a fully successful poweroff) and then waits for the
// background tart run process to exit on its own. That exit is the
// actual "VM stopped" signal.
//
// If tart run doesn't exit within 60s of the poweroff, that's a
// genuine hang, not a slow shutdown — we escalate to killing the
// process and return an error so the failure is visible and
// diagnosable, rather than silently working around it.
func (r *tartRunner) powerOffAndWait(ctx context.Context, w io.Writer) error {
	if err := runTart(ctx, w, "exec", BaseImageName, "sudo", "systemctl", "poweroff"); err != nil {
		fmt.Fprintf(w, "note: tart exec poweroff returned %v (expected if the guest-agent channel closes mid-shutdown)\n", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	select {
	case err := <-r.done:
		r.exited = true
		if err != nil {
			fmt.Fprintf(w, "tart run exited after poweroff (%v) — normal for a guest-initiated shutdown\n", err)
		}
		return nil
	case <-waitCtx.Done():
		if r.cmd.Process != nil {
			_ = r.cmd.Process.Kill()
		}
		<-r.done
		r.exited = true
		return fmt.Errorf("tart run did not exit within 60s of guest poweroff; killed the process as a fallback: %w", waitCtx.Err())
	}
}

// BuildBaseImage builds the devm-base Tart VM natively in Go,
// reproducing the steps the (now-deleted) image/build.sh used to
// perform via tart(1):
//
//  1. tart pull the template.
//  2. Delete any pre-existing devm-base (NeedsBuild returning true
//     is authorization to blow away the stale image).
//  3. tart clone the template into devm-base.
//  4. Boot headless, wait for the guest-agent IP.
//  5. Stage the embedded image/ assets (nftables-locked.conf,
//     devm.target) onto the guest, then provision via `tart exec -i …
//     sudo bash -s` fed provision-base.sh.
//  6. Poweroff + fresh boot to fire the rename-on-boot one-shot
//     (in-place `systemctl reboot` doesn't reliably re-establish the
//     guest-agent handshake; a clean poweroff + new `tart run` does).
//  7. Verify the identity is "devm".
//  8. Run the cleanup fragment to remove the rename machinery.
//  9. Final poweroff to save a clean image.
//
// Streams progress to w. On success, records the current definition
// hash so a subsequent NeedsBuild call reports up-to-date.
func BuildBaseImage(ctx context.Context, w io.Writer) error {
	hash, err := DefinitionHash()
	if err != nil {
		return err
	}

	template := os.Getenv("TEMPLATE")
	if template == "" {
		template = defaultTemplate
	}

	fmt.Fprintf(w, ">>> Pulling template %s...\n", template)
	if err := runTart(ctx, w, "pull", template); err != nil {
		return fmt.Errorf("tart pull %s: %w", template, err)
	}

	// Delete any existing devm-base before rebuilding. The old shell
	// script aborted here as a defensive check when a human ran
	// build.sh manually; called programmatically from Go, the builder
	// owns the lifecycle — NeedsBuild returning true is enough
	// authorization to blow away the stale image. Without this, every
	// `devm upgrade` and every install-triggered rebuild-on-hash-change
	// would fail because devm-base is present but out of date.
	exists, err := baseImageExistsCtx(ctx)
	if err != nil {
		return fmt.Errorf("check for existing %s: %w", BaseImageName, err)
	}
	if exists {
		fmt.Fprintf(w, ">>> Removing stale %s before rebuild...\n", BaseImageName)
		if err := runTart(ctx, w, "delete", BaseImageName); err != nil {
			return fmt.Errorf("delete stale %s: %w", BaseImageName, err)
		}
	}

	fmt.Fprintf(w, ">>> Cloning %s -> %s...\n", template, BaseImageName)
	if err := runTart(ctx, w, "clone", template, BaseImageName); err != nil {
		return fmt.Errorf("tart clone %s %s: %w", template, BaseImageName, err)
	}

	fmt.Fprintf(w, ">>> Resizing %s disk to %dGB...\n", BaseImageName, schema.DefaultDiskSizeGB)
	if err := runTart(ctx, w, "set", BaseImageName, "--disk-size", strconv.Itoa(schema.DefaultDiskSizeGB)); err != nil {
		return fmt.Errorf("tart set --disk-size %s: %w", BaseImageName, err)
	}

	runner, err := startTartRun(ctx)
	if err != nil {
		return fmt.Errorf("start tart run: %w", err)
	}
	defer runner.killIfRunning()

	fmt.Fprintln(w, ">>> Waiting for VM boot...")
	if err := waitForIP(ctx, 60, 2*time.Second, 5*time.Second); err != nil {
		return fmt.Errorf("VM did not report an IP: %w", err)
	}

	fmt.Fprintln(w, ">>> Staging image assets...")
	if err := tartExecStdin(ctx, w, stageImageAssetsScript()); err != nil {
		return fmt.Errorf("stage image assets: %w", err)
	}

	fmt.Fprintln(w, ">>> Provisioning base layer...")
	if err := tartExecStdin(ctx, w, baseimage.ProvisionBaseScript); err != nil {
		return fmt.Errorf("provision base layer: %w", err)
	}

	fmt.Fprintln(w, ">>> Powering off VM to release guest-agent state...")
	if err := runner.powerOffAndWait(ctx, w); err != nil {
		return fmt.Errorf("poweroff after provisioning: %w", err)
	}

	fmt.Fprintln(w, ">>> Booting fresh to fire rename one-shot...")
	runner, err = startTartRun(ctx)
	if err != nil {
		return fmt.Errorf("start tart run (fresh boot): %w", err)
	}
	defer runner.killIfRunning()

	if err := waitForReachable(ctx, w, 30, time.Second, 3*time.Second); err != nil {
		return fmt.Errorf("VM not reachable after fresh boot: %w", err)
	}

	identity := tartExecIdentity(ctx, 5*time.Second)
	if identity != "devm" {
		return fmt.Errorf("rename one-shot did not fire — tart exec identity is '%s', expected 'devm'", identity)
	}
	fmt.Fprintln(w, ">>> Rename verified: tart exec runs as devm.")

	fmt.Fprintln(w, ">>> Cleaning up rename bootstrap unit...")
	if err := tartExecCleanup(ctx, 30*time.Second, w); err != nil {
		return fmt.Errorf("cleanup rename bootstrap: %w", err)
	}

	fmt.Fprintln(w, ">>> Shutting down VM...")
	if err := runner.powerOffAndWait(ctx, w); err != nil {
		return fmt.Errorf("final poweroff: %w", err)
	}

	fmt.Fprintf(w, ">>> devm-base built (cloned from %s).\n", template)

	storePath, err := HashStorePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(storePath), 0700); err != nil {
		return fmt.Errorf("create hash cache dir: %w", err)
	}
	if err := os.WriteFile(storePath, []byte(hash), 0644); err != nil {
		return fmt.Errorf("write hash: %w", err)
	}
	return nil
}
