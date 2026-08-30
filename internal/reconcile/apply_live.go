package reconcile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mdubb86/devm/internal/caenv"
	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/guestbin"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
)

// ApplyLive runs every BucketLive change through the corresponding
// operation. Non-LIVE changes in the slice are skipped silently (caller
// is expected to handle them via the recreate path).
//
// Template changes are coalesced — any number of KindTemplateChange
// entries trigger a SINGLE invocation of the in-sandbox dispatcher,
// which re-runs every installer (cheap; identical content is an
// idempotent atomic rewrite). Any env, path, commands, or template
// change re-builds the devmbundle from cfg + repoRoot and pipes it into
// the guest at /opt/devm/ before the dispatcher runs, so the sandbox
// always executes the latest rendered content — nothing is written to
// the host workspace. A commands change rides this same rebuild because
// the commands manifest that backs the guest's `run <name>` dispatcher
// is written into every bundle rebuild alongside env/templates. Path
// changes ride the same rebuild as env changes because
// render.RenderEtcEnvironment folds cfg.Path into /etc/environment's PATH= line
// (there's no separate path-only artifact to pipe). A KindRepoChange
// mutation ALSO rides this rebuild — a label rename or primary toggle
// changes the guestPath commands.json records and/or the $WORKSPACE
// /etc/environment folds in, so without it a rename leaves the guest's
// `run <name>` dispatcher and $WORKSPACE pointed at the old path.
// KindStartupChange is NOT live-applied — it's BucketRestartVM, not
// BucketLive, so the caller routes it through the recreate path (VM
// stop + cold start; see internal/provision's setupBootEnforcement /
// runStartupCommands, which pick up the freshly-rendered
// /opt/devm/startup.sh on that next boot). For each changed template,
// this function logs a "consuming services may need restart" line to
// stderr.
//
// KindRepoChange/KindVolumeChange entries ALSO route through
// applyMutagenSessionChange, which needs a live mutagen daemon
// (mutagenCLI), the daemon's identity (identCfg, for runtime-dir-scoped
// mirror paths and TLD), and the project's current iron-proxy CONNECT
// URL (ironProxyURL, needed only for a repo cold-start clone; pass ""
// when no repo add/volume-toggle-on is pending).
//
// Returns the first error encountered; later changes are not attempted
// after a failure so the snapshot stays coherent on retry.
func ApplyLive(tr *tart.Tart, vmName string, changes []Change, cfg schema.Config, repoRoot, daemonRuntimeDir string, caPEM, sshAuthPub, sshHostPriv, sshHostPub []byte, mutagenCLI *mutagen.CLI, identCfg identity.Config, ironProxyURL string) error {
	var templateChanges []Change
	var mutagenChanges []Change
	var bundleRebuildNeeded bool
	for _, c := range changes {
		if c.Bucket() != BucketLive {
			continue
		}
		switch c.Kind {
		case KindPortAdd, KindPortRemove, KindPortChange:
			// No guest-side action needed: host<->guest port publishing is
			// softnet's ingress listeners, reconciled off the merged config
			// snapshot elsewhere — not applied here.
		case KindTemplateChange:
			templateChanges = append(templateChanges, c)
		case KindEnvAdd, KindEnvRemove, KindEnvChange, KindPathChange, KindCommandsChange:
			bundleRebuildNeeded = true
		case KindServiceDirectChange:
			// Ingress for direct services is pushed to softnet's
			// declarative expose map by the daemon, not applied in-guest.
		case KindRepoChange:
			// A repo mutation (label rename, primary toggle, ...) changes
			// the guestPath commands.json records and/or $WORKSPACE in
			// /etc/environment, so it needs the same bundle rebuild as an
			// env/path/commands change, ON TOP OF its mutagen session
			// update below.
			bundleRebuildNeeded = true
			mutagenChanges = append(mutagenChanges, c)
		case KindVolumeChange:
			mutagenChanges = append(mutagenChanges, c)
		}
	}

	if bundleRebuildNeeded || len(templateChanges) > 0 {
		// Rebuild the bundle and pipe it into the guest at /opt/devm/ —
		// same mechanism the provisioner uses at cold-start. Nothing is
		// written to the host workspace; with-devm-env sources the new
		// /etc/environment on every subsequent exec, and (for template changes) the
		// dispatcher below reads the freshly-piped installers. Running
		// shells keep their old env until they re-exec — hence BucketLive.
		commandsManifest, err := render.RenderCommandsManifest(cfg, repoRoot)
		if err != nil {
			return fmt.Errorf("render commands manifest: %w", err)
		}
		mutagenAgent, err := mutagen.LinuxArm64Agent()
		if err != nil {
			return fmt.Errorf("extract mutagen agent: %w", err)
		}
		in := devmbundle.BuildInput{
			Cfg:                    cfg,
			RepoRoot:               repoRoot,
			DaemonRuntimeDir:       daemonRuntimeDir,
			CARootPEM:              caPEM,
			SSHAuthorizedPubkey:    sshAuthPub,
			SSHHostPriv:            sshHostPriv,
			SSHHostPub:             sshHostPub,
			CommandsManifest:       commandsManifest,
			Run:                    guestbin.Run(),
			MutagenAgentLinuxArm64: mutagenAgent,
			MutagenVersion:         strings.TrimPrefix(mutagen.EmbeddedVersion(), "v"),
		}
		if cfg.Docker {
			in.DockerRuncShim = docker.Shim()
			in.DockerCLIShim = docker.DockerShim()
		}
		tar, err := devmbundle.Build(in)
		if err != nil {
			return fmt.Errorf("apply_live: build bundle: %w", err)
		}
		r := tr.ExecStdin(context.Background(), vmName,
			bytes.NewReader(tar),
			[]string{"bash", "-e", "-o", "pipefail", "-c", devmbundle.GuestInstallScript},
		)
		if r.ExitCode != 0 {
			return fmt.Errorf("apply_live: pipe bundle: exit %d (stderr: %s)", r.ExitCode, r.Stderr)
		}
	}

	if len(templateChanges) > 0 {
		// Single dispatcher invocation re-runs all installers already piped
		// in above. Wrapper sources /etc/environment (sets $WORKSPACE etc.)
		// and cd's into the workspace before exec'ing the dispatcher, which
		// itself reads the fixed /opt/devm/templates path.
		wrapperPath := devmbundle.GuestWrapper
		dispatcherPath := devmbundle.GuestDispatcher
		r := tr.ExecWithRetry(context.Background(), vmName, []string{wrapperPath, "bash", dispatcherPath})
		if r.ExitCode != 0 {
			return fmt.Errorf("apply_live: install-templates: exit %d (stderr: %s)", r.ExitCode, r.Stderr)
		}
		// User-facing "you might need to restart your service" hint.
		for _, c := range templateChanges {
			// Structural invariants (same as the rest of the Change contract):
			//   add    -> Old == "" && New != ""
			//   change -> Old != "" && New != ""
			//   remove -> Old != "" && New == ""
			if c.New == "" {
				// removed: the on-disk artifact in the sandbox persists.
				fmt.Fprintf(os.Stderr,
					"template %s removed from config; sandbox file persists until recreate.\n",
					c.Detail)
				continue
			}
			action := "updated"
			if c.Old == "" {
				action = "installed"
			}
			fmt.Fprintf(os.Stderr,
				"template %s (service %s) %s; restart consuming services in the shell if needed.\n",
				c.Detail, c.Service, action)
		}
	}

	if len(mutagenChanges) > 0 {
		exec := func(script string) (string, string, int, error) {
			r := tr.ExecStdin(context.Background(), vmName, strings.NewReader(script),
				[]string{"bash", "-e", "-o", "pipefail"})
			return r.Stdout, r.Stderr, r.ExitCode, nil
		}
		for _, c := range mutagenChanges {
			if err := applyMutagenSessionChange(context.Background(), exec, mutagenCLI, identCfg, vmName, ironProxyURL, c); err != nil {
				return err
			}
		}
	}

	return nil
}

// GuestExec runs script inside the guest and returns its stdout,
// stderr, exit code, and any transport error — the same shape as
// serviceapi.GuestExec. reconcile can't import serviceapi (serviceapi
// already imports reconcile for Change/ApplyLive, so the reverse import
// would cycle), so applyMutagenSessionChange gets its own copy of this
// seam; ApplyLive wires it from *tart.Tart the same way
// serviceapi.tartGuestExec wires the real one from tart exec.
type GuestExec func(script string) (stdout, stderr string, exitCode int, err error)

// guestHomeDir is the guest-side parent for every repo clone — mirrors
// serviceapi.guestHomeDir; repo session paths are always
// /home/devm/<label>, independent of the project.
const guestHomeDir = "/home/devm"

// mirrorDirFn is the test-injection seam for the Mac-side mirror
// directory ensure step every session setup needs. Production mirrors
// serviceapi.ensureMirrorDir's mkdir-mode-0700 semantics exactly;
// duplicated here (rather than imported) for the same import-cycle
// reason as GuestExec above.
var mirrorDirFn = defaultMirrorDir

// resolveMirrorPath resolves label's current Mac mirror path through
// the same mirrorDirFn seam setupSingleSession uses (ensuring the dir
// exists as a side effect), so a test's fake mirror-dir factory
// governs every mirror-path computation in this file, not just the
// session-setup one.
func resolveMirrorPath(cfg identity.Config, projectID, label string) (string, error) {
	path, _, err := mirrorDirFn(cfg, projectID, label)
	return path, err
}

func defaultMirrorDir(cfg identity.Config, projectID, label string) (path string, wasEmpty bool, err error) {
	path = filepath.Join(cfg.RuntimeDir(), projectID, label)
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", false, fmt.Errorf("mkdir mirror dir %s: %w", path, err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", false, fmt.Errorf("read mirror dir %s: %w", path, err)
	}
	return path, len(entries) == 0, nil
}

// mutagenSessionsDir is where per-project generated sync config yaml
// files live — mirrors serviceapi.mutagenSessionsDir.
func mutagenSessionsDir(cfg identity.Config) string {
	return filepath.Join(cfg.RuntimeDir(), "mutagen", "sessions")
}

// mutagenSessionName returns the mutagen sync session name for one
// entity of projectID — mirrors serviceapi.SessionName. The two MUST
// stay byte-for-byte identical: this is how apply-live finds a session
// serviceapi.SetupVolumesPhase created at cold-start (and vice versa).
func mutagenSessionName(projectID, label string) string {
	return fmt.Sprintf("devm-%s-%s", projectID, label)
}

// guestSSHTarget returns the mutagen beta-endpoint host for projectID
// — the ALIAS ("devm-<vmName>") from devm's managed ssh_config, not the
// bare "<vmName>.<TLD>" hostname. mutagen shells out to system ssh
// which resolves per-project HostKeyAlias / UserKnownHostsFile /
// IdentityFile from the alias's Host block; passing the FQDN matches
// no Host stanza, ssh falls back to default known_hosts, and mutagen
// sync create fails with "Host key verification failed". Mirrors the
// guestSSHTarget construction in serviceapi's /vm/volume-sync handler.
func guestSSHTarget(cfg identity.Config, projectID string) string {
	_ = cfg.TLD
	return "devm-" + projectID
}

// guestGitCACertPath returns the guest-side CA bundle path git trusts
// for a proxied clone — mirrors serviceapi.guestGitCACertPath.
// caenv.Vars is the single source of truth for this value; this just
// indexes into it.
func guestGitCACertPath() string {
	for _, v := range caenv.Vars {
		if v.Key == "GIT_SSL_CAINFO" {
			return v.Value
		}
	}
	return ""
}

// resolveVolumeLabel resolves one volumes.<name> entry's mutagen sync
// label — mirrors serviceapi.resolveVolumeLabel.
func resolveVolumeLabel(v schema.Volume) string {
	if v.Label != nil {
		return *v.Label
	}
	return filepath.Base(v.Path)
}

// resolveRepoLabelForApply resolves one repos.<name> entry's mutagen
// sync label for a live-apply change. Unlike
// serviceapi.resolveRepoLabel, it never falls back to a Mac-cwd
// basename: apply-live only ever touches secondary repos (the primary
// repo is established at cold-start and never appears as a live
// KindRepoChange add/remove), and schema validation requires an
// explicit URL on every secondary — so the URL-derived name always
// resolves.
func resolveRepoLabelForApply(r schema.RepoConfig) string {
	if r.Label != nil {
		return *r.Label
	}
	if r.URL != nil {
		return schema.BareCloneName(*r.URL)
	}
	return ""
}

// repoVolumeEnabled reports whether r's `volume:` flag is on, applying
// the same secondary-repo default (nil == false, opt-in) BuildEntities
// uses for non-primary entries.
func repoVolumeEnabled(r schema.RepoConfig) bool {
	return r.Volume != nil && *r.Volume
}

// derefOrEmpty returns *s, or "" when s is nil.
func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---------- in-sync guard (scan + compare) ----------
//
// Duplicates serviceapi's ScanMac/ScanGuest/GuardCheck fast-path
// algorithm byte-for-byte (same guest scan script, same sha256-over-
// sorted-top-100-paths hash) for the same import-cycle reason as
// GuestExec above. Any change to the guard's shape needs to land in
// both copies.

const guardTopSampleLimit = 100

type scanSide struct {
	Count   int64
	Size    int64
	TopHash string
}

// scanMacSide walks rootPath and computes its scanSide. An empty or
// missing directory returns the zero scanSide.
func scanMacSide(rootPath string) (scanSide, error) {
	var count, size int64
	var relPaths []string

	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == rootPath {
			return nil
		}
		rel, relErr := filepath.Rel(rootPath, path)
		if relErr != nil {
			return relErr
		}
		count++
		relPaths = append(relPaths, rel)
		if !d.IsDir() {
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return scanSide{}, nil
		}
		return scanSide{}, fmt.Errorf("apply_live: scan mac %s: %w", rootPath, err)
	}
	if count == 0 {
		return scanSide{}, nil
	}

	sort.Strings(relPaths)
	top := relPaths
	if len(top) > guardTopSampleLimit {
		top = top[:guardTopSampleLimit]
	}
	return scanSide{Count: count, Size: size, TopHash: hashTopSample(top)}, nil
}

// hashTopSample sha256-hashes the newline-joined sample, matching the
// guest script's `sort | head -100 | shasum -a 256` pipeline byte for
// byte.
func hashTopSample(sample []string) string {
	if len(sample) == 0 {
		return ""
	}
	h := sha256.New()
	for _, p := range sample {
		h.Write([]byte(p))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

const guestScanScriptBody = `set -e
cd "$1" 2>/dev/null || { echo count=0 size=0 hash=- top=-; exit 0; }
count=$(find . -mindepth 1 | wc -l | tr -d ' ')
size=$(du -sb . 2>/dev/null | awk '{print $1}')
top=$(find . -mindepth 1 -printf '%P\n' 2>/dev/null | sort | head -100 | shasum -a 256 | awk '{print $1}')
echo "count=$count size=$size hash=$top"
`

// scanGuestSide runs the fixed guest-side scan script against
// guestPath via exec and parses its "count=N size=B hash=H" stdout.
func scanGuestSide(exec GuestExec, guestPath string) (scanSide, error) {
	script := fmt.Sprintf("set -- %s\n%s", shellSingleQuoted(guestPath), guestScanScriptBody)
	stdout, stderr, exitCode, err := exec(script)
	if err != nil {
		return scanSide{}, fmt.Errorf("apply_live: scan guest %s: %w", guestPath, err)
	}
	if exitCode != 0 {
		return scanSide{}, fmt.Errorf("apply_live: scan guest %s: exit %d: %s", guestPath, exitCode, strings.TrimSpace(stderr))
	}
	var side scanSide
	for _, f := range strings.Fields(stdout) {
		key, val, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		switch key {
		case "count":
			n, perr := strconv.ParseInt(val, 10, 64)
			if perr != nil {
				return scanSide{}, fmt.Errorf("apply_live: parse count %q: %w", val, perr)
			}
			side.Count = n
		case "size":
			n, perr := strconv.ParseInt(val, 10, 64)
			if perr != nil {
				return scanSide{}, fmt.Errorf("apply_live: parse size %q: %w", val, perr)
			}
			side.Size = n
		case "hash":
			if val != "-" {
				side.TopHash = val
			}
		}
	}
	return side, nil
}

// guardOK implements the in-sync guard's fast-path decision — mirrors
// serviceapi.GuardCheck. An empty side never conflicts; two populated
// sides pass only when Count, Size, and TopHash all agree.
func guardOK(mac, guest scanSide) (ok bool, reason string) {
	if mac.Count == 0 || guest.Count == 0 {
		return true, ""
	}
	if mac.Count != guest.Count {
		return false, fmt.Sprintf("mac and guest entry counts differ (mac=%d, guest=%d)", mac.Count, guest.Count)
	}
	if mac.Size != guest.Size {
		return false, fmt.Sprintf("mac and guest total sizes differ (mac=%d bytes, guest=%d bytes)", mac.Size, guest.Size)
	}
	if mac.TopHash != guest.TopHash {
		return false, fmt.Sprintf("mac and guest content shape differs (top-%d path hash mismatch)", guardTopSampleLimit)
	}
	return true, ""
}

// ---------- guest-side scripts ----------

// ensureGuestDir guarantees guestPath exists and is owned by devm —
// mirrors serviceapi.ensureGuestDirScript.
func ensureGuestDir(exec GuestExec, guestPath string) error {
	script := fmt.Sprintf(
		"sudo install -d -o devm -g devm %s\nsudo -u devm mkdir -p %s\n",
		shellSingleQuoted(filepath.Dir(guestPath)),
		shellSingleQuoted(guestPath),
	)
	_, stderr, exitCode, err := exec(script)
	if err != nil {
		return fmt.Errorf("ensure guest dir %s: %w", guestPath, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("ensure guest dir %s: exit %d: %s", guestPath, exitCode, strings.TrimSpace(stderr))
	}
	return nil
}

// moveGuestDir runs `sudo -u devm mv oldPath newPath` in the guest,
// creating newPath's parent first (root, so the mv can land outside
// devm's existing tree).
func moveGuestDir(exec GuestExec, oldPath, newPath string) error {
	script := fmt.Sprintf(
		"sudo install -d -o devm -g devm %s\nsudo -u devm mv %s %s\n",
		shellSingleQuoted(filepath.Dir(newPath)),
		shellSingleQuoted(oldPath),
		shellSingleQuoted(newPath),
	)
	_, stderr, exitCode, err := exec(script)
	if err != nil {
		return fmt.Errorf("mv guest dir %s -> %s: %w", oldPath, newPath, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("mv guest dir %s -> %s: exit %d: %s", oldPath, newPath, exitCode, strings.TrimSpace(stderr))
	}
	return nil
}

// tokenPlaceholderFor returns the __DEVM_SECRET_<name>__ placeholder
// iron-proxy's secrets transform substitutes on the wire — mirrors
// serviceapi.tokenPlaceholderFor.
func tokenPlaceholderFor(secretName string) string {
	return "__DEVM_SECRET_" + secretName + "__"
}

// cloneRepoInGuest runs `git clone` inside the guest as the devm user.
// Mirrors serviceapi.CloneRepoInGuest exactly — same shape, same
// constraints — but lives here because internal/reconcile can't import
// internal/serviceapi without a cycle. Keep the two in lockstep.
//
// Guest egress is transparently routed through iron-proxy by softnet
// (:80 → ft.HTTP, :443 → ft.HTTPS under PolicyEnforced) so git just
// clones normally; NO HTTP_PROXY. When a secret is configured,
// iron-proxy sees the http.extraheader with the placeholder token and
// substitutes the resolved secret on the wire. GIT_SSL_CAINFO points
// at iron-proxy's MITM root cert so git trusts the intercepted TLS.
//
// When secretName is empty (public repo), the extraheader is omitted
// entirely — sending a well-formed Basic auth header with a bogus
// token makes hosts like github.com reject the clone even for public
// reads. ironProxyURL is retained for API compatibility with the
// SessionEntity plumbing but is not used on the guest side.
func cloneRepoInGuest(exec GuestExec, url, secretName, guestTargetPath, ironProxyURL, guestCACertPath string) error {
	_ = ironProxyURL

	var configOpts string
	if secretName != "" {
		authRaw := "x-access-token:" + tokenPlaceholderFor(secretName)
		authB64 := base64.StdEncoding.EncodeToString([]byte(authRaw))
		extraHeader := "Authorization: Basic " + authB64
		configOpts = "-c " + shellSingleQuoted("http.extraheader="+extraHeader) + " "
	}
	script := fmt.Sprintf(`sudo -u devm bash -c %s`, shellSingleQuoted(fmt.Sprintf(`set -e
export GIT_SSL_CAINFO=%s
git clone --quiet %s%s %s
`,
		guestCACertPath,
		configOpts,
		shellSingleQuoted(url),
		shellSingleQuoted(guestTargetPath),
	)))

	stdout, stderr, exitCode, err := exec(script)
	if err != nil {
		return fmt.Errorf("git clone %s: %w", url, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("git clone %s: exit %d: %s", url, exitCode, strings.TrimSpace(stderr+stdout))
	}
	return nil
}

// ---------- session setup / teardown ----------

// repoCloneSpec carries the git provenance a cold-start clone needs.
// nil on a sessionSpec means the entity is a pure volume.
type repoCloneSpec struct {
	URL             string
	Secret          string
	IronProxyURL    string
	GuestCACertPath string
}

// sessionSpec is one mirrored path getting its own mutagen sync
// session — the apply-live analogue of serviceapi.SessionEntity.
type sessionSpec struct {
	Label     string
	GuestPath string
	Ignore    []string
	Repo      *repoCloneSpec
}

// findExactSession returns the session named exactly name, or nil if
// none exists. cli.SyncList filters by prefix internally, so this
// re-filters for an exact match (a label like "foo" would otherwise
// spuriously prefix-match a session named "foo-bar").
func findExactSession(cli *mutagen.CLI, name string) (*mutagen.SyncSession, error) {
	sessions, err := cli.SyncList(name)
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		if sessions[i].Name == name {
			return &sessions[i], nil
		}
	}
	return nil, nil
}

// terminateSessionIfExists flushes and terminates the session named
// devm-<projectID>-<label>, if one exists. A no-op when no such
// session exists (e.g. removing a repo that was never volume:true).
func terminateSessionIfExists(cli *mutagen.CLI, projectID, label string) error {
	name := mutagenSessionName(projectID, label)
	existing, err := findExactSession(cli, name)
	if err != nil {
		return fmt.Errorf("list sessions for %s: %w", label, err)
	}
	if existing == nil {
		return nil
	}
	if err := cli.SyncFlush(existing.ID); err != nil {
		return fmt.Errorf("flush session %s: %w", label, err)
	}
	if err := cli.SyncTerminate(existing.ID); err != nil {
		return fmt.Errorf("terminate session %s: %w", label, err)
	}
	return nil
}

// removeMirrorIfEmpty removes label's Mac mirror dir when a
// removed repo/volume's mirror holds nothing — pure housekeeping, not
// required for correctness (a stale empty dir is harmless). A
// non-empty mirror is left alone: it may hold content the mutagen
// session never finished syncing back to the guest before the entry
// was removed, and this isn't the place to silently discard it — the
// daemon's project-purge path is what unconditionally clears mirrors.
func removeMirrorIfEmpty(cfg identity.Config, projectID, label string) error {
	mirror, err := resolveMirrorPath(cfg, projectID, label)
	if err != nil {
		return fmt.Errorf("resolve mac mirror for %s: %w", label, err)
	}
	entries, err := os.ReadDir(mirror)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read mac mirror %s: %w", mirror, err)
	}
	if len(entries) != 0 {
		return nil
	}
	if err := os.Remove(mirror); err != nil {
		return fmt.Errorf("rm mac mirror %s: %w", mirror, err)
	}
	return nil
}

// cloneOneRepoIfEmpty is apply_live's analogue of
// serviceapi.cloneOneRepoIfEmpty — same shape, same rules: a repo
// entity is git-cloned iff both the Mac mirror and the guest side are
// currently empty. A pure volume (spec.Repo == nil) is a no-op. Called
// after setupSingleSession for every entity under the live-reconcile
// path.
func cloneOneRepoIfEmpty(exec GuestExec, cfg identity.Config, projectID string, spec sessionSpec) error {
	if spec.Repo == nil {
		return nil
	}
	macMirror, _, err := mirrorDirFn(cfg, projectID, spec.Label)
	if err != nil {
		return fmt.Errorf("mutagen setup %s: ensure mac mirror: %w", spec.Label, err)
	}
	macSide, err := scanMacSide(macMirror)
	if err != nil {
		return fmt.Errorf("mutagen setup %s: %w", spec.Label, err)
	}
	guestSide, err := scanGuestSide(exec, spec.GuestPath)
	if err != nil {
		return fmt.Errorf("mutagen setup %s: %w", spec.Label, err)
	}
	if macSide.Count > 0 || guestSide.Count > 0 {
		return nil
	}
	return cloneRepoInGuest(exec, spec.Repo.URL, spec.Repo.Secret, spec.GuestPath, spec.Repo.IronProxyURL, spec.Repo.GuestCACertPath)
}

// setupSingleSession brings spec's mutagen sync session up to date —
// the apply-live analogue of serviceapi.SetupVolumesPhase for exactly
// one entity: ensures both sides' mirror dirs exist, verifies the
// in-sync guard before touching an existing target, then creates a
// fresh session or resumes a paused one. Clone-if-empty for a repo
// entity is handled separately by cloneOneRepoIfEmpty, called by every
// caller right after this returns nil.
func setupSingleSession(exec GuestExec, cli *mutagen.CLI, cfg identity.Config, projectID string, spec sessionSpec) error {
	macMirror, _, err := mirrorDirFn(cfg, projectID, spec.Label)
	if err != nil {
		return fmt.Errorf("mutagen setup %s: ensure mac mirror: %w", spec.Label, err)
	}

	if err := ensureGuestDir(exec, spec.GuestPath); err != nil {
		return fmt.Errorf("mutagen setup %s: %w", spec.Label, err)
	}

	macSide, err := scanMacSide(macMirror)
	if err != nil {
		return fmt.Errorf("mutagen setup %s: %w", spec.Label, err)
	}
	guestSide, err := scanGuestSide(exec, spec.GuestPath)
	if err != nil {
		return fmt.Errorf("mutagen setup %s: %w", spec.Label, err)
	}

	if ok, reason := guardOK(macSide, guestSide); !ok {
		daemonlog.Errorf("mutagen: guard rejected %s: %s", spec.Label, reason)
		return fmt.Errorf("in-sync guard failed for %s: %s", spec.Label, reason)
	}

	name := mutagenSessionName(projectID, spec.Label)
	existing, err := findExactSession(cli, name)
	if err != nil {
		return fmt.Errorf("mutagen setup %s: list sessions: %w", spec.Label, err)
	}
	if existing != nil {
		if existing.Status == "paused" {
			if err := cli.SyncResume(existing.ID); err != nil {
				return fmt.Errorf("mutagen setup %s: resume session: %w", spec.Label, err)
			}
		}
		return nil
	}

	sessionCfg := mutagen.ComposeConfig(spec.Ignore)
	configPath := mutagen.ConfigFilePath(mutagenSessionsDir(cfg), projectID, spec.Label)
	if err := mutagen.WriteConfigFile(configPath, sessionCfg); err != nil {
		return fmt.Errorf("mutagen setup %s: write session config: %w", spec.Label, err)
	}

	beta := "devm@" + guestSSHTarget(cfg, projectID) + ":" + spec.GuestPath
	if _, err := cli.SyncCreate(name, macMirror, beta, configPath, nil); err != nil {
		return fmt.Errorf("mutagen setup %s: create session: %w", spec.Label, err)
	}
	return nil
}

// recreateSessionWithIgnore terminates label's existing session (if
// any) and re-creates it with a freshly composed ignore list — used
// by both the volume and repo Field="ignore"/"Ignore" mutation
// branches. repo is nil for a volume.
func recreateSessionWithIgnore(exec GuestExec, cli *mutagen.CLI, cfg identity.Config, projectID, label, guestPath string, ignore []string, repo *repoCloneSpec) error {
	if err := terminateSessionIfExists(cli, projectID, label); err != nil {
		return err
	}
	spec := sessionSpec{
		Label: label, GuestPath: guestPath, Ignore: ignore, Repo: repo,
	}
	if err := setupSingleSession(exec, cli, cfg, projectID, spec); err != nil {
		return err
	}
	return cloneOneRepoIfEmpty(exec, cfg, projectID, spec)
}

// ensureRepoClonedInGuest is the Volume:false repo flow: no mirror, no
// mutagen session — just guarantee the guest has a clone.
func ensureRepoClonedInGuest(exec GuestExec, repo schema.RepoConfig, guestPath, ironProxyURL, guestCACertPath string) error {
	if err := ensureGuestDir(exec, guestPath); err != nil {
		return fmt.Errorf("cold-start clone %s: %w", guestPath, err)
	}
	guestSide, err := scanGuestSide(exec, guestPath)
	if err != nil {
		return fmt.Errorf("cold-start clone %s: %w", guestPath, err)
	}
	if guestSide.Count != 0 {
		return nil
	}
	if err := cloneRepoInGuest(exec, derefOrEmpty(repo.URL), repo.Secret, guestPath, ironProxyURL, guestCACertPath); err != nil {
		return fmt.Errorf("cold-start clone %s: %w", guestPath, err)
	}
	return nil
}

// ---------- volume change dispatch ----------

func applyVolumeChange(exec GuestExec, cli *mutagen.CLI, cfg identity.Config, projectID string, change Change) error {
	switch change.Op {
	case OpAdd:
		vol, ok := change.NewValue.(schema.Volume)
		if !ok {
			return fmt.Errorf("apply_live: volume add %q: missing new value", change.Key)
		}
		spec := sessionSpec{
			Label: resolveVolumeLabel(vol), GuestPath: vol.Path, Ignore: vol.Ignore,
		}
		if err := setupSingleSession(exec, cli, cfg, projectID, spec); err != nil {
			return err
		}
		return cloneOneRepoIfEmpty(exec, cfg, projectID, spec)

	case OpRemove:
		vol, ok := change.OldValue.(schema.Volume)
		if !ok {
			return fmt.Errorf("apply_live: volume remove %q: missing old value", change.Key)
		}
		label := resolveVolumeLabel(vol)
		if err := terminateSessionIfExists(cli, projectID, label); err != nil {
			return err
		}
		return removeMirrorIfEmpty(cfg, projectID, label)

	case OpMutate:
		before, after := change.VolumeBefore, change.VolumeAfter
		if before == nil || after == nil {
			return fmt.Errorf("apply_live: volume mutate %q.%s: missing before/after snapshot", change.Key, change.Field)
		}
		switch change.Field {
		case "path":
			return moveVolumeGuestPath(exec, cli, cfg, projectID, *before, *after)
		case "label":
			return renameVolumeLabel(exec, cli, cfg, projectID, *before, *after)
		case "ignore":
			label := resolveVolumeLabel(*after)
			return recreateSessionWithIgnore(exec, cli, cfg, projectID, label, after.Path, after.Ignore, nil)
		}
	}
	return nil
}

// moveVolumeGuestPath handles Field="path": terminate the old session,
// mv the guest dir to its new location, then re-run the setup flow at
// the (usually unchanged) resolved label. The Mac mirror dir is keyed
// by label, not path, so it never needs to move here.
func moveVolumeGuestPath(exec GuestExec, cli *mutagen.CLI, cfg identity.Config, projectID string, before, after schema.Volume) error {
	oldLabel := resolveVolumeLabel(before)
	if err := terminateSessionIfExists(cli, projectID, oldLabel); err != nil {
		return err
	}
	if err := moveGuestDir(exec, before.Path, after.Path); err != nil {
		return fmt.Errorf("apply_live: volume path change %q: %w", oldLabel, err)
	}
	newLabel := resolveVolumeLabel(after)
	spec := sessionSpec{
		Label: newLabel, GuestPath: after.Path, Ignore: after.Ignore,
	}
	if err := setupSingleSession(exec, cli, cfg, projectID, spec); err != nil {
		return err
	}
	return cloneOneRepoIfEmpty(exec, cfg, projectID, spec)
}

// renameVolumeLabel handles Field="label": terminate the old session,
// mv the Mac mirror dir to the new label's slot (the guest dir is
// untouched — a volume's guest Path is independent of its label), then
// re-run the setup flow at the new label.
func renameVolumeLabel(exec GuestExec, cli *mutagen.CLI, cfg identity.Config, projectID string, before, after schema.Volume) error {
	oldLabel := resolveVolumeLabel(before)
	newLabel := resolveVolumeLabel(after)
	if oldLabel == newLabel {
		return nil
	}
	if err := terminateSessionIfExists(cli, projectID, oldLabel); err != nil {
		return err
	}
	oldMirror, err := resolveMirrorPath(cfg, projectID, oldLabel)
	if err != nil {
		return fmt.Errorf("apply_live: resolve mac mirror for %s: %w", oldLabel, err)
	}
	newMirror := filepath.Join(filepath.Dir(oldMirror), newLabel)
	if err := os.Rename(oldMirror, newMirror); err != nil {
		return fmt.Errorf("apply_live: mv mac mirror %s -> %s: %w", oldMirror, newMirror, err)
	}
	spec := sessionSpec{
		Label: newLabel, GuestPath: after.Path, Ignore: after.Ignore,
	}
	if err := setupSingleSession(exec, cli, cfg, projectID, spec); err != nil {
		return err
	}
	return cloneOneRepoIfEmpty(exec, cfg, projectID, spec)
}

// ---------- repo change dispatch ----------

func applyRepoChange(exec GuestExec, cli *mutagen.CLI, cfg identity.Config, projectID, ironProxyURL, guestCACertPath string, change Change) error {
	switch change.Op {
	case OpAdd:
		repo, ok := change.NewValue.(schema.RepoConfig)
		if !ok {
			return fmt.Errorf("apply_live: repo add %q: missing new value", change.Key)
		}
		label := resolveRepoLabelForApply(repo)
		guestPath := filepath.Join(guestHomeDir, label)
		if !repoVolumeEnabled(repo) {
			return ensureRepoClonedInGuest(exec, repo, guestPath, ironProxyURL, guestCACertPath)
		}
		spec := sessionSpec{
			Label: label, GuestPath: guestPath, Ignore: repo.Ignore,
			Repo: &repoCloneSpec{
				URL: derefOrEmpty(repo.URL), Secret: repo.Secret,
				IronProxyURL: ironProxyURL, GuestCACertPath: guestCACertPath,
			},
		}
		if err := setupSingleSession(exec, cli, cfg, projectID, spec); err != nil {
			return err
		}
		return cloneOneRepoIfEmpty(exec, cfg, projectID, spec)

	case OpRemove:
		repo, ok := change.OldValue.(schema.RepoConfig)
		if !ok {
			return fmt.Errorf("apply_live: repo remove %q: missing old value", change.Key)
		}
		if !repoVolumeEnabled(repo) {
			return nil
		}
		label := resolveRepoLabelForApply(repo)
		if err := terminateSessionIfExists(cli, projectID, label); err != nil {
			return err
		}
		return removeMirrorIfEmpty(cfg, projectID, label)

	case OpMutate:
		before, after := change.RepoBefore, change.RepoAfter
		if before == nil || after == nil {
			return fmt.Errorf("apply_live: repo mutate %q.%s: missing before/after snapshot", change.Key, change.Field)
		}
		switch change.Field {
		case "Label":
			return renameRepoLabel(exec, cli, cfg, projectID, ironProxyURL, guestCACertPath, *before, *after)
		case "Volume":
			return toggleRepoVolume(exec, cli, cfg, projectID, ironProxyURL, guestCACertPath, *before, *after)
		case "Ignore":
			if !repoVolumeEnabled(*after) {
				return nil
			}
			label := resolveRepoLabelForApply(*after)
			return recreateSessionWithIgnore(exec, cli, cfg, projectID, label, filepath.Join(guestHomeDir, label), after.Ignore,
				&repoCloneSpec{
					URL: derefOrEmpty(after.URL), Secret: after.Secret,
					IronProxyURL: ironProxyURL, GuestCACertPath: guestCACertPath,
				})
		case "Primary":
			// Primary election is a validation-time concern (schema
			// picks/rejects a primary before this ever runs) — no guest
			// or mutagen-session action follows from it alone.
			return nil
		}
	}
	return nil
}

// renameRepoLabel handles Field="Label": terminate the old session (if
// volume:true), mv the guest clone dir (a repo's guest path IS
// label-derived, unlike a volume's), mv the Mac mirror dir if one
// exists, then re-run the setup flow at the new label.
func renameRepoLabel(exec GuestExec, cli *mutagen.CLI, cfg identity.Config, projectID, ironProxyURL, guestCACertPath string, before, after schema.RepoConfig) error {
	oldLabel := resolveRepoLabelForApply(before)
	newLabel := resolveRepoLabelForApply(after)
	if oldLabel == newLabel {
		return nil
	}

	volumeAfter := repoVolumeEnabled(after)
	if repoVolumeEnabled(before) {
		if err := terminateSessionIfExists(cli, projectID, oldLabel); err != nil {
			return err
		}
	}

	oldGuestPath := filepath.Join(guestHomeDir, oldLabel)
	newGuestPath := filepath.Join(guestHomeDir, newLabel)
	if err := moveGuestDir(exec, oldGuestPath, newGuestPath); err != nil {
		return fmt.Errorf("apply_live: repo label rename %q -> %q: %w", oldLabel, newLabel, err)
	}

	if !volumeAfter {
		return nil
	}

	oldMirror, err := resolveMirrorPath(cfg, projectID, oldLabel)
	if err != nil {
		return fmt.Errorf("apply_live: resolve mac mirror for %s: %w", oldLabel, err)
	}
	newMirror := filepath.Join(filepath.Dir(oldMirror), newLabel)
	if err := os.Rename(oldMirror, newMirror); err != nil {
		return fmt.Errorf("apply_live: mv mac mirror %s -> %s: %w", oldMirror, newMirror, err)
	}

	spec := sessionSpec{
		Label: newLabel, GuestPath: newGuestPath, Ignore: after.Ignore,
		Repo: &repoCloneSpec{
			URL: derefOrEmpty(after.URL), Secret: after.Secret,
			IronProxyURL: ironProxyURL, GuestCACertPath: guestCACertPath,
		},
	}
	if err := setupSingleSession(exec, cli, cfg, projectID, spec); err != nil {
		return err
	}
	return cloneOneRepoIfEmpty(exec, cfg, projectID, spec)
}

// toggleRepoVolume handles Field="Volume": true->false tears the
// mutagen session and its Mac mirror down (the guest clone is
// untouched — it keeps working as a plain, unmirrored checkout);
// false->true runs the same setup flow as a repo OpAdd, against the
// repo's already-existing guest clone.
func toggleRepoVolume(exec GuestExec, cli *mutagen.CLI, cfg identity.Config, projectID, ironProxyURL, guestCACertPath string, before, after schema.RepoConfig) error {
	wasOn := repoVolumeEnabled(before)
	isOn := repoVolumeEnabled(after)
	if wasOn == isOn {
		return nil
	}
	label := resolveRepoLabelForApply(after)

	if wasOn && !isOn {
		if err := terminateSessionIfExists(cli, projectID, label); err != nil {
			return err
		}
		mirror, err := resolveMirrorPath(cfg, projectID, label)
		if err != nil {
			return fmt.Errorf("apply_live: resolve mac mirror for %s: %w", label, err)
		}
		if err := os.RemoveAll(mirror); err != nil {
			return fmt.Errorf("apply_live: rm mac mirror %s: %w", mirror, err)
		}
		return nil
	}

	guestPath := filepath.Join(guestHomeDir, label)
	spec := sessionSpec{
		Label: label, GuestPath: guestPath, Ignore: after.Ignore,
		Repo: &repoCloneSpec{
			URL: derefOrEmpty(after.URL), Secret: after.Secret,
			IronProxyURL: ironProxyURL, GuestCACertPath: guestCACertPath,
		},
	}
	if err := setupSingleSession(exec, cli, cfg, projectID, spec); err != nil {
		return err
	}
	return cloneOneRepoIfEmpty(exec, cfg, projectID, spec)
}

// applyMutagenSessionChange dispatches one BucketLive
// KindRepoChange/KindVolumeChange onto the mutagen session lifecycle:
// setting up, tearing down, or recreating exactly the one session (or,
// for a Volume:false repo, the plain guest clone) the change affects.
// ironProxyURL is the project's current iron-proxy CONNECT URL, needed
// only when the change implies a cold-start clone (repo add, or a repo
// volume:false->true toggle); pass "" when neither applies.
func applyMutagenSessionChange(ctx context.Context, exec GuestExec, cli *mutagen.CLI, cfg identity.Config, projectID string, ironProxyURL string, change Change) error {
	_ = ctx // reserved for future guest-transport cancellation, unused today
	guestCACertPath := guestGitCACertPath()

	switch change.Kind {
	case KindVolumeChange:
		return applyVolumeChange(exec, cli, cfg, projectID, change)
	case KindRepoChange:
		return applyRepoChange(exec, cli, cfg, projectID, ironProxyURL, guestCACertPath, change)
	}
	return nil
}

// shellSingleQuoted wraps s in single quotes for use as a bash
// literal, so a path or URL containing spaces or shell metacharacters
// can't break out of a generated script.
func shellSingleQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
