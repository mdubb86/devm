package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/repohelpers"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/spf13/cobra"
)

// cpArg is one parsed positional argument of `devm cp`. Exactly one
// of the two sides of a devm cp invocation is Remote=true — direction
// is inferred from which side wears the colon.
type cpArg struct {
	Remote  bool   // colon-prefixed (either `:/p` or `proj:/p`)
	Project string // explicit "proj" from "proj:/p"; empty for ":/p"
	Path    string // absolute path (guest side) or local path (host side)
}

// parseCpArg splits one positional argv token into its structured form.
// Recognised shapes:
//
//	":/absolute/path"            → Remote=true, Project="",   Path="/absolute/path"
//	"project:/absolute/path"     → Remote=true, Project="project", Path="/absolute/path"
//	"-"                          → Remote=false, Project="", Path="-" (stdin/stdout sentinel)
//	anything else                → Remote=false, Project="", Path=<as-is>
//
// A local path that happens to contain a colon must be disambiguated
// with a leading "./" (matches scp): "./foo:bar" is local, "foo:bar"
// would be misread as project "foo".
func parseCpArg(raw string) (cpArg, error) {
	if raw == "" {
		return cpArg{}, errors.New("empty path argument")
	}
	if raw == "-" {
		return cpArg{Path: "-"}, nil
	}
	if strings.HasPrefix(raw, ":") {
		p := raw[1:]
		if p == "" {
			return cpArg{}, errors.New(`":" prefix requires a path (got ":")`)
		}
		if !strings.HasPrefix(p, "/") {
			return cpArg{}, fmt.Errorf("guest path must be absolute (got %q)", raw)
		}
		return cpArg{Remote: true, Path: p}, nil
	}
	// A local path containing "/" before any ":" is unambiguously local
	// (matches scp). "./foo:bar" is local; "foo:bar" is project-scoped.
	if slash := strings.Index(raw, "/"); slash >= 0 {
		if colon := strings.Index(raw, ":"); colon < 0 || slash < colon {
			return cpArg{Path: raw}, nil
		}
	}
	if colon := strings.Index(raw, ":"); colon > 0 {
		project := raw[:colon]
		p := raw[colon+1:]
		if p == "" {
			return cpArg{}, fmt.Errorf(`"%s" is missing a path after ":"`, project)
		}
		if !strings.HasPrefix(p, "/") {
			return cpArg{}, fmt.Errorf("guest path must be absolute (got %q)", raw)
		}
		return cpArg{Remote: true, Project: project, Path: p}, nil
	}
	// No colon anywhere: pure local path.
	return cpArg{Path: raw}, nil
}

// resolveDirection classifies a src/dst pair into upload / download /
// error. Both remote or both local is a usage error — devm cp isn't
// guest↔guest (cross-project) and isn't a host↔host cp replacement.
type direction int

const (
	directionUpload   direction = iota + 1 // host → guest
	directionDownload                      // guest → host
)

func resolveDirection(src, dst cpArg) (direction, error) {
	switch {
	case src.Remote && dst.Remote:
		return 0, errors.New("both src and dst are remote — devm cp does not support guest-to-guest copies")
	case !src.Remote && !dst.Remote:
		return 0, errors.New("neither src nor dst is remote — use plain `cp` for local copies")
	case dst.Remote:
		return directionUpload, nil
	default:
		return directionDownload, nil
	}
}

// mountPassthrough returns the host-side path that mirrors the given
// guest path, if the guest path lives under a mount that shares the
// filesystem view. Two cases:
//
//   - The primary workspace (repoRoot, when `repo:` is configured) is
//     virtiofs-shared from the primary volume's Mac-side storage
//     (<RuntimeDir>/volumes/<project>/<primaryVolumeName>/). A guest
//     path under repoRoot translates to the same relative path under
//     that storage dir.
//   - User mounts[] entries — these still mirror the host path at the
//     same absolute path inside the guest.
//
// Named volumes (other than the primary) aren't checked here — go via
// pipe. Returns ("", false) when no mirror is known; caller falls back
// to pipe.
func mountPassthrough(guestPath, repoRoot string, pcfg schema.Config, projectName string) (string, bool) {
	if pcfg.Repo != nil && inside(guestPath, repoRoot) {
		if rel, err := filepath.Rel(repoRoot, guestPath); err == nil {
			primary := repohelpers.PrimaryVolumeName(repoRoot)
			storageRoot := filepath.Join(cfg.RuntimeDir(), "volumes", projectName, primary)
			return filepath.Clean(filepath.Join(storageRoot, rel)), true
		}
	}
	for _, entry := range pcfg.Mounts {
		host, _ := strings.CutSuffix(entry, ":ro")
		host = expandHome(host)
		if !filepath.IsAbs(host) {
			host = filepath.Join(repoRoot, host)
		}
		host = filepath.Clean(host)
		// Same-path mirror: mounts[] pins host path === guest path.
		if inside(guestPath, host) {
			return guestPath, true
		}
	}
	return "", false
}

// inside reports whether target lives under root (or is root itself).
// Uses lexical prefix comparison after Clean; guest paths and host
// paths use POSIX separators either way (macOS + Linux both `/`).
func inside(target, root string) bool {
	target = filepath.Clean(target)
	root = filepath.Clean(root)
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(filepath.Separator))
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

var cpCmd = &cobra.Command{
	Use:   "cp <src> <dst>",
	Short: "Copy a file between the Mac and the sandbox",
	Long: `Copy a file between the Mac and the sandbox VM. Direction is
inferred from which arg wears the colon:

  devm cp foo.txt :/etc/foo.conf              # host → guest
  devm cp :/var/log/x.log ./x.log             # guest → host
  devm cp buzztrack:/root/dump.sql ./         # explicit project

":/path" alone infers the project from the devm.yaml in the current
working directory; "project:/path" is always explicit. "-" is stdin
(as src) or stdout (as dst) for streaming from/to pipes.

Transport is auto-selected: if the guest path lives under a shared
mount (workspace or a mounts[] entry), the copy is a plain host-side
cp into the shared filesystem with no network involved. Otherwise
it's streamed through the daemon exec channel; writes to root-owned
paths (/etc, /var, /root) retry via sudo (the guest devm user has
NOPASSWD sudo).`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		src, err := parseCpArg(args[0])
		if err != nil {
			return fmt.Errorf("src: %w", err)
		}
		dst, err := parseCpArg(args[1])
		if err != nil {
			return fmt.Errorf("dst: %w", err)
		}
		dir, err := resolveDirection(src, dst)
		if err != nil {
			return err
		}
		// Resolve which project we're copying to/from. When the remote
		// side carries no explicit project name, fall back to the CWD's
		// devm.yaml.
		var remote cpArg
		if dir == directionUpload {
			remote = dst
		} else {
			remote = src
		}
		projectName, repoRoot, cfg, err := resolveProject(remote.Project)
		if err != nil {
			return err
		}
		return runCp(cmd.Context(), projectName, repoRoot, cfg, dir, src, dst)
	},
}

// resolveProject picks the project name and loads its schema. When
// explicit is non-empty it's the project from `project:/path` and no
// CWD walk is needed (in which case repoRoot is empty and cfg is the
// zero value — mount detection still returns "not mounted" for every
// path, forcing pipe transport, which is the right conservative
// default when we don't know the project's mount table). When empty,
// walk up from CWD to find the project root and load it.
func resolveProject(explicit string) (name, repoRoot string, cfg schema.Config, err error) {
	if explicit != "" {
		// Explicit project name — no local devm.yaml required.
		return explicit, "", schema.Config{}, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", schema.Config{}, fmt.Errorf("get cwd: %w", err)
	}
	repoRoot, err = repohelpers.FindDevmYAML(cwd)
	if err != nil {
		return "", "", schema.Config{}, fmt.Errorf("locate devm.yaml: %w (run `devm cp` from a project root or use `project:/path` syntax)", err)
	}
	loaded, err := config.Load(repoRoot)
	if err != nil {
		return "", "", schema.Config{}, fmt.Errorf("locate devm.yaml: %w (run `devm cp` from a project root or use `project:/path` syntax)", err)
	}
	return loaded.Project.Name, repoRoot, loaded, nil
}

// runCp is the transport-dispatcher after arg parsing + project
// resolution. Split from the RunE closure so tests can exercise it
// with a fake tartRunner + local temp directories.
func runCp(ctx context.Context, projectName, repoRoot string, cfg schema.Config, dir direction, src, dst cpArg) error {
	tr := tart.New()
	switch dir {
	case directionUpload:
		return upload(ctx, tr, projectName, repoRoot, cfg, src.Path, dst.Path)
	case directionDownload:
		return download(ctx, tr, projectName, repoRoot, cfg, src.Path, dst.Path)
	}
	return fmt.Errorf("internal: unclassified direction")
}

// upload copies hostPath into the guest at guestPath.
func upload(ctx context.Context, tr *tart.Tart, projectName, repoRoot string, cfg schema.Config, hostPath, guestPath string) error {
	if hostMirror, ok := mountPassthrough(guestPath, repoRoot, cfg, projectName); ok {
		// Zero-network path: write into the shared filesystem the guest
		// already sees at guestPath === hostMirror.
		if err := copyFileHostSide(hostPath, hostMirror); err != nil {
			return fmt.Errorf("mount-passthrough copy: %w", err)
		}
		fmt.Fprintf(os.Stderr, "devm cp: %s → %s (mount)\n", hostPath, guestPath)
		return nil
	}
	// Pipe transport. `tee` truncates on write and errors on EACCES,
	// which is what the sudo-retry branch keys on. `sh -c` gives us
	// argv-safe path quoting via %q + escaping.
	remoteCmd := fmt.Sprintf("tee %s > /dev/null", shellQuote(guestPath))
	if err := pipeUpload(ctx, tr, projectName, hostPath, remoteCmd); err != nil {
		// Retry via sudo on permission-denied. tee's exit code is
		// non-zero when it can't open the target; the error surfaces
		// here regardless of exit code detail because tart exec propagates.
		if isPermissionDenied(err) {
			remoteCmd = fmt.Sprintf("sudo tee %s > /dev/null", shellQuote(guestPath))
			if err2 := pipeUpload(ctx, tr, projectName, hostPath, remoteCmd); err2 != nil {
				return fmt.Errorf("pipe upload (with sudo): %w", err2)
			}
			fmt.Fprintf(os.Stderr, "devm cp: %s → %s (pipe, sudo)\n", hostPath, guestPath)
			return nil
		}
		return fmt.Errorf("pipe upload: %w", err)
	}
	fmt.Fprintf(os.Stderr, "devm cp: %s → %s (pipe)\n", hostPath, guestPath)
	return nil
}

// download copies guestPath out to hostPath.
func download(ctx context.Context, tr *tart.Tart, projectName, repoRoot string, cfg schema.Config, guestPath, hostPath string) error {
	if hostMirror, ok := mountPassthrough(guestPath, repoRoot, cfg, projectName); ok {
		if err := copyFileHostSide(hostMirror, hostPath); err != nil {
			return fmt.Errorf("mount-passthrough copy: %w", err)
		}
		fmt.Fprintf(os.Stderr, "devm cp: %s → %s (mount)\n", guestPath, hostPath)
		return nil
	}
	// Pipe transport. `cat` on the guest streams the file to our stdout.
	remoteCmd := fmt.Sprintf("cat %s", shellQuote(guestPath))
	if err := pipeDownload(ctx, tr, projectName, remoteCmd, hostPath); err != nil {
		if isPermissionDenied(err) {
			remoteCmd = fmt.Sprintf("sudo cat %s", shellQuote(guestPath))
			if err2 := pipeDownload(ctx, tr, projectName, remoteCmd, hostPath); err2 != nil {
				return fmt.Errorf("pipe download (with sudo): %w", err2)
			}
			fmt.Fprintf(os.Stderr, "devm cp: %s → %s (pipe, sudo)\n", guestPath, hostPath)
			return nil
		}
		return fmt.Errorf("pipe download: %w", err)
	}
	fmt.Fprintf(os.Stderr, "devm cp: %s → %s (pipe)\n", guestPath, hostPath)
	return nil
}

// pipeUpload streams hostPath (or stdin if "-") through `tart exec
// <vm> sh -c <remoteCmd>` as the guest process's stdin.
func pipeUpload(ctx context.Context, tr *tart.Tart, vm, hostPath, remoteCmd string) error {
	var src io.Reader
	if hostPath == "-" {
		src = os.Stdin
	} else {
		f, err := os.Open(hostPath)
		if err != nil {
			return err
		}
		defer f.Close()
		src = f
	}
	// `-i` is required — `tart exec` drops stdin unless -i is passed, so a
	// pipeUpload without it silently produces an empty file on the guest
	// (proven by test_149's first run: sudo tee succeeded but wrote 0
	// bytes). Harmless for pipeDownload too (nothing to feed via stdin),
	// so we use the same argv shape for both.
	cmd := exec.CommandContext(ctx, tr.Path, "exec", "-i", vm, "sh", "-c", remoteCmd)
	cmd.Stdin = src
	cmd.Stdout = os.Stderr // tart exec sometimes prints its own noise; keep it off the pipe
	var stderr strings.Builder
	cmd.Stderr = &teeWriter{w: os.Stderr, buf: &stderr}
	if err := cmd.Run(); err != nil {
		return &pipeError{stderr: stderr.String(), cause: err}
	}
	return nil
}

// pipeDownload runs `tart exec <vm> sh -c <remoteCmd>` and streams its
// stdout to hostPath (or os.Stdout if "-").
func pipeDownload(ctx context.Context, tr *tart.Tart, vm, remoteCmd, hostPath string) error {
	var dst io.Writer
	if hostPath == "-" {
		dst = os.Stdout
	} else {
		f, err := os.Create(hostPath)
		if err != nil {
			return err
		}
		defer f.Close()
		dst = f
	}
	// `-i` matches pipeUpload — harmless here (nothing to feed via stdin).
	cmd := exec.CommandContext(ctx, tr.Path, "exec", "-i", vm, "sh", "-c", remoteCmd)
	cmd.Stdout = dst
	var stderr strings.Builder
	cmd.Stderr = &teeWriter{w: os.Stderr, buf: &stderr}
	if err := cmd.Run(); err != nil {
		return &pipeError{stderr: stderr.String(), cause: err}
	}
	return nil
}

// pipeError carries the guest-side stderr alongside the exec failure
// so isPermissionDenied can look for "permission denied" text — tart
// exec forwards the child's exit code but flattens it into a generic
// *exec.ExitError with no path context.
type pipeError struct {
	stderr string
	cause  error
}

func (e *pipeError) Error() string {
	if e.stderr != "" {
		return fmt.Sprintf("%v: %s", e.cause, strings.TrimSpace(e.stderr))
	}
	return e.cause.Error()
}
func (e *pipeError) Unwrap() error { return e.cause }

// isPermissionDenied heuristically detects EACCES from a pipe stderr.
// cat/tee both write "Permission denied" (case-varies-slightly across
// coreutils versions) to stderr on EACCES; the exit code alone is
// ambiguous (1 for a broad range of tee/cat failures).
func isPermissionDenied(err error) bool {
	var pe *pipeError
	if !errors.As(err, &pe) {
		return false
	}
	s := strings.ToLower(pe.stderr)
	return strings.Contains(s, "permission denied") ||
		strings.Contains(s, "operation not permitted")
}

// copyFileHostSide is the mount-passthrough workhorse: plain
// io.Copy from src to dst on the Mac side. Both paths are Mac paths;
// the guest sees the destination via the shared mount.
func copyFileHostSide(srcPath, dstPath string) error {
	var src io.Reader
	if srcPath == "-" {
		src = os.Stdin
	} else {
		f, err := os.Open(srcPath)
		if err != nil {
			return err
		}
		defer f.Close()
		src = f
	}
	var dst io.Writer
	if dstPath == "-" {
		dst = os.Stdout
	} else {
		f, err := os.Create(dstPath)
		if err != nil {
			return err
		}
		defer f.Close()
		dst = f
	}
	_, err := io.Copy(dst, src)
	return err
}

// (shellQuote — defined in service.go; POSIX single-quote wrapping.)

// teeWriter forwards writes to two sinks. Used to send guest-side
// stderr to the user's terminal in real time AND capture a copy for
// isPermissionDenied inspection.
type teeWriter struct {
	w   io.Writer
	buf *strings.Builder
}

func (t *teeWriter) Write(p []byte) (int, error) {
	t.buf.Write(p)
	return t.w.Write(p)
}

func init() {
	rootCmd.AddCommand(cpCmd)
}
