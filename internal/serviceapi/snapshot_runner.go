// snapshot_runner adapts internal/snapshot.HookRunner to the guest
// over `tart exec -i`. RunBackup streams the guest hook's stdout into
// the Mac-side writer unmodified (archive bytes); RunRestore pipes the
// Mac-side reader into the guest hook's stdin. Stderr for backup/
// restore is discarded — stream-mode hooks emit noisy progress
// (pg_basebackup, verbose tar) that isn't worth preserving alongside
// the archive. Pre/post lifecycle hooks instead route stderr to the
// daemon log, line-prefixed with the entry and hook name, since their
// stderr is the operator's signal.
package serviceapi

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/snapshot"
)

// tartHookRunner bridges snapshot.HookRunner to the project's tart VM.
type tartHookRunner struct {
	tr     *tart.Tart
	vmName string
	// logW receives stderr lines from pre/post hooks, prefixed with
	// "snapshot <entry> <hookname>: ". nil ⇒ default to the stdlib log
	// package's writer so production lines land in the daemon's
	// com.devm.service.out.log alongside other info-level output.
	logW io.Writer
}

// hookShell wraps a devm.sh function reference in the same preamble
// `devm shell` uses so backup_<name> / restore_<name> see the same
// $WORKSPACE, $PATH, and helper functions a user shell sees. Sourced
// from /opt/devm/ (bundle-local) rather than /home/devm/ (the mutagen-
// synced copy used by scriptfile/`gdevm run`) because on_stop/
// on_teardown hooks run while mutagen may already be torn down.
func hookShell(fn string) []string {
	return hookShellWithEnv(fn, nil)
}

// hookShellWithEnv is hookShell with an additional env-var export
// prefix. Values are single-quoted, with any embedded single quote
// replaced by `'\''`. When env is empty the export clause is omitted
// entirely so the leading `;` can't produce an empty statement.
func hookShellWithEnv(fn string, env map[string]string) []string {
	var b strings.Builder
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("export ")
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(k)
			b.WriteString("='")
			b.WriteString(strings.ReplaceAll(env[k], "'", `'\''`))
			b.WriteByte('\'')
		}
		b.WriteString("; ")
	}
	b.WriteString("source /opt/devm/devm.sh && " +
		"[ -r /opt/devm/devm.me.sh ] && source /opt/devm/devm.me.sh; ")
	b.WriteString(fn)
	return []string{"/bin/bash", "-c", b.String()}
}

// RunBackup invokes backup_<entryName> inside the VM and streams its
// stdout to w. Stderr is discarded.
func (r *tartHookRunner) RunBackup(ctx context.Context, entryName string, w io.Writer) (int, error) {
	return r.tr.ExecBinary(ctx, r.vmName, nil, w, nil, hookShell("backup_"+entryName))
}

// RunRestore invokes restore_<entryName> inside the VM, piping stdin
// as its stdin. Stderr is discarded.
func (r *tartHookRunner) RunRestore(ctx context.Context, entryName string, stdin io.Reader) (int, error) {
	return r.tr.ExecBinary(ctx, r.vmName, stdin, nil, nil, hookShell("restore_"+entryName))
}

// RunPreBackup invokes pre_backup_<entryName> and returns its first
// stdout line trimmed (empty when nothing was printed). Path-mode
// uses this to receive a `file:///abs/path` directive. Stderr is
// routed to the daemon log line-prefixed with "snapshot <entry>
// pre_backup: ".
func (r *tartHookRunner) RunPreBackup(ctx context.Context, entryName string, env map[string]string) (string, int, error) {
	var out bytes.Buffer
	code, err := r.runLifecycle(ctx, entryName, "pre_backup", env, &out)
	return strings.TrimRight(out.String(), "\n"), code, err
}

// RunPostBackup invokes post_backup_<entryName>. Stdout is discarded;
// stderr is routed to the daemon log.
func (r *tartHookRunner) RunPostBackup(ctx context.Context, entryName string, env map[string]string) (int, error) {
	return r.runLifecycle(ctx, entryName, "post_backup", env, nil)
}

// RunPreRestore invokes pre_restore_<entryName>. Stdout is discarded;
// stderr is routed to the daemon log.
func (r *tartHookRunner) RunPreRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	return r.runLifecycle(ctx, entryName, "pre_restore", env, nil)
}

// RunPostRestore invokes post_restore_<entryName>. Stdout is discarded;
// stderr is routed to the daemon log.
func (r *tartHookRunner) RunPostRestore(ctx context.Context, entryName string, env map[string]string) (int, error) {
	return r.runLifecycle(ctx, entryName, "post_restore", env, nil)
}

// runLifecycle is the shared body of the four pre/post methods: build
// the guest command with env, pipe stderr through a line-prefixing
// scanner that writes to r.logW, wait for the scanner to drain, and
// return the exit code.
func (r *tartHookRunner) runLifecycle(ctx context.Context, entryName, hookName string, env map[string]string, stdout io.Writer) (int, error) {
	logW := r.logW
	if logW == nil {
		logW = log.Writer()
	}
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		// Match ExecStream's buffer: 1 MiB per line, truncate rather
		// than drop beyond that.
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		prefix := fmt.Sprintf("snapshot %s %s: ", entryName, hookName)
		for sc.Scan() {
			fmt.Fprintf(logW, "%s%s\n", prefix, sc.Text())
		}
	}()

	argv := hookShellWithEnv(hookName+"_"+entryName, env)
	code, err := r.tr.ExecBinary(ctx, r.vmName, nil, stdout, pw, argv)
	// Close the write end so the scanner sees EOF; wait for drain so
	// the caller sees all lines flushed before we return.
	_ = pw.Close()
	<-done
	_ = pr.Close()
	return code, err
}

// HookExists introspects the sourced /opt/devm/devm.sh for the six
// per-entry hook functions in one shell call. `declare -F name...`
// with named arguments prints one bare function name per line on
// stdout for each name that resolves to a shell function, and emits
// an error on stderr for the rest — a mix exits non-zero, which is
// expected and not a transport failure, so we ignore the exit code
// and parse stdout instead. Stderr is line-prefixed into the daemon
// log so a devm.sh sourcing failure surfaces instead of vanishing.
func (r *tartHookRunner) HookExists(ctx context.Context, entryName string) (snapshot.HookPresence, error) {
	names := []string{
		"backup_" + entryName,
		"restore_" + entryName,
		"pre_backup_" + entryName,
		"post_backup_" + entryName,
		"pre_restore_" + entryName,
		"post_restore_" + entryName,
	}
	expected := map[string]bool{}
	for _, n := range names {
		expected[n] = true
	}
	fn := "declare -F " + strings.Join(names, " ")
	logW := r.logW
	if logW == nil {
		logW = log.Writer()
	}
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		prefix := fmt.Sprintf("snapshot %s hook_exists: ", entryName)
		for sc.Scan() {
			fmt.Fprintf(logW, "%s%s\n", prefix, sc.Text())
		}
	}()
	var out bytes.Buffer
	_, err := r.tr.ExecBinary(ctx, r.vmName, nil, &out, pw, hookShell(fn))
	_ = pw.Close()
	<-done
	_ = pr.Close()
	if err != nil {
		return snapshot.HookPresence{}, err
	}
	// Each stdout line is a bare function name (e.g. "backup_foo"); any
	// line that doesn't match one of the queried names is ignored.
	present := map[string]bool{}
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if expected[line] {
			present[line] = true
		}
	}
	return snapshot.HookPresence{
		Backup:      present["backup_"+entryName],
		Restore:     present["restore_"+entryName],
		PreBackup:   present["pre_backup_"+entryName],
		PostBackup:  present["post_backup_"+entryName],
		PreRestore:  present["pre_restore_"+entryName],
		PostRestore: present["post_restore_"+entryName],
	}, nil
}

// newHookRunnerFn is the test seam the handlers go through to build a
// HookRunner. Tests override it with a lambda that returns an
// in-process fake so they never shell out to tart. Production returns
// a tartHookRunner bound to the daemon's one *tart.Tart + the
// project's VM name, with logW defaulted to the stdlib log writer so
// hook stderr lands in the daemon's stdout log.
var newHookRunnerFn = func(tr *tart.Tart, vmName string) snapshot.HookRunner {
	return &tartHookRunner{tr: tr, vmName: vmName, logW: log.Writer()}
}

// tartTarRunner bridges snapshot.TarRunner to the project's tart VM.
// The daemon runs `tart exec -i <vm> /bin/bash -c <script>` for the
// four tar branches (backup-dir, backup-file, restore-dir,
// restore-file); the shell wrapper handles the `cd` and (for restore)
// the parent-dir `mkdir -p` so the Mac side never has to inspect the
// guest filesystem. Stderr from tar is routed to logW line-prefixed
// with "snapshot <entry> <backup_tar|restore_tar>: " so a tar failure
// surfaces in the daemon log alongside the error the caller sees.
type tartTarRunner struct {
	tr     *tart.Tart
	vmName string
	// logW receives stderr lines, prefixed. nil ⇒ default to the
	// stdlib log writer so production lines land in the daemon's
	// com.devm.service.out.log.
	logW io.Writer
}

// bashShell wraps a shell snippet in the `/bin/bash -c` argv tar needs,
// matching hookShell's shape so both runners emit the same `tart exec`
// invocation modulo the inner script.
func bashShell(script string) []string {
	return []string{"/bin/bash", "-c", script}
}

// shellQuote single-quotes s for interpolation inside `/bin/bash -c`,
// escaping any embedded single quote as `'\''`. All path / basename
// interpolations into the tar scripts go through this so a path with
// a space, `$`, or `'` cannot break the shell invocation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RunBackupTar streams `tar -cf -` output from the guest to w. The
// directory branch tars the source's contents (`-C <source> .`) so the
// archive restores as "<declaredPath>/<contents>" regardless of the
// resolved source name. The file branch tars the source basename and
// applies `--transform` to rename it to declaredBasename on the fly,
// so a capture-time `--filename` override (via pre_backup) still
// re-materializes under the declared name on restore. Stderr lines
// are routed to the daemon log.
//
// tar runs under `sudo` so it can read root-owned source files; on a
// user-owned source the sudo'd read produces an identical archive (tar
// records the file's own uid/gid regardless of who ran it).
func (r *tartTarRunner) RunBackupTar(ctx context.Context, entryName, source, declaredBasename string, isDir bool, stdout io.Writer) (int, error) {
	var script string
	if isDir {
		script = "cd " + shellQuote(source) + " && sudo tar -cf - ."
	} else {
		parent := filepath.Dir(source)
		srcBase := filepath.Base(source)
		// The substitution assumes POSIX basenames do not contain the
		// sed delimiter `|`, a backslash, or `$`/`^` anchors — true for
		// every path we have observed in practice. shellQuote still
		// escapes the whole expression for the outer bash; this comment
		// pins the narrower assumption for the inner sed program.
		transform := "s|^" + srcBase + "$|" + declaredBasename + "|"
		script = "cd " + shellQuote(parent) + " && sudo tar --transform=" + shellQuote(transform) + " -cf - " + shellQuote(srcBase)
	}
	return r.runTar(ctx, entryName, "backup_tar", nil, stdout, script)
}

// RunRestoreTar pipes stdin into `tar -xf -` inside the guest. Both
// branches `mkdir -p` the extract target's parent first so a fresh
// guest filesystem (or one where an operator renamed the declared
// path) doesn't fail at `tar -C` with "no such directory". For a
// directory entry that parent is the declaredPath itself; for a file
// entry it is dirname(declaredPath).
//
// Both `mkdir -p` and `tar -xf` run under `sudo` so the extract can
// restore root-owned entries with their original uid/gid (required by
// `--same-owner --same-permissions`) and can write under a parent the
// devm user does not own. For user-owned data the entry's archived
// uid/gid is still the devm user, so the extract lands as the devm
// user — sudo does not change ownership, only authority.
//
// Tests pin the mkdir-p behavior at this layer (see
// TestTartTarRunner_RestoreDir_MkdirsParent and
// TestTartTarRunner_RestoreFile_MkdirsParent); the restore code above
// relies on the parent existing and does not touch the guest FS.
func (r *tartTarRunner) RunRestoreTar(ctx context.Context, entryName, declaredPath string, isDir bool, stdin io.Reader) (int, error) {
	var script string
	if isDir {
		script = "sudo mkdir -p " + shellQuote(declaredPath) +
			" && sudo tar -xf - --same-owner --same-permissions -C " + shellQuote(declaredPath)
	} else {
		parent := filepath.Dir(declaredPath)
		script = "sudo mkdir -p " + shellQuote(parent) +
			" && sudo tar -xf - --same-owner --same-permissions -C " + shellQuote(parent)
	}
	return r.runTar(ctx, entryName, "restore_tar", stdin, nil, script)
}

// StatPath shells out to the guest to query a path: whether it exists,
// whether it's a directory, and its symlink-resolved absolute path. The
// wrapper always exits 0 and emits a single line on stdout with E=/D=/P=
// tokens — a non-zero exit from the shell means `tart` or `bash` could
// not be reached and is surfaced as a transport error, not as "path
// absent". Stderr lines (should be none in the happy path) are routed to
// the daemon log like the tar branches.
func (r *tartTarRunner) StatPath(ctx context.Context, entryName, guestPath string) (bool, bool, string, error) {
	logW := r.logW
	if logW == nil {
		logW = log.Writer()
	}
	script := "P=" + shellQuote(guestPath) + `; ` +
		`R=$(readlink -f -- "$P" 2>/dev/null) || R=""; ` +
		`if [ -n "$R" ] && [ -e "$R" ]; then ` +
		`  if [ -d "$R" ]; then printf 'E=1 D=1 P=%s\n' "$R"; ` +
		`  else printf 'E=1 D=0 P=%s\n' "$R"; fi; ` +
		`else printf 'E=0 D=0 P=\n'; fi`

	var out bytes.Buffer
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		prefix := fmt.Sprintf("snapshot %s stat_path: ", entryName)
		for sc.Scan() {
			fmt.Fprintf(logW, "%s%s\n", prefix, sc.Text())
		}
	}()

	code, err := r.tr.ExecBinary(ctx, r.vmName, nil, &out, pw, bashShell(script))
	_ = pw.Close()
	<-done
	_ = pr.Close()
	if err != nil {
		return false, false, "", fmt.Errorf("snapshot %s stat_path: %w", entryName, err)
	}
	if code != 0 {
		return false, false, "", fmt.Errorf("snapshot %s stat_path: shell exit %d", entryName, code)
	}

	line := strings.TrimRight(out.String(), "\n")
	exists, isDir, resolved, perr := parseStatPathLine(line)
	if perr != nil {
		return false, false, "", fmt.Errorf("snapshot %s stat_path: %w", entryName, perr)
	}
	return exists, isDir, resolved, nil
}

// parseStatPathLine parses the single-line `E=X D=Y P=<path>` protocol
// emitted by the StatPath shell wrapper. Returns an error only when the
// shape is malformed (which would mean the wrapper itself is broken or
// something injected noise into stdout); well-formed lines carry no
// error even when the path is absent.
func parseStatPathLine(line string) (bool, bool, string, error) {
	if !strings.HasPrefix(line, "E=") {
		return false, false, "", fmt.Errorf("unexpected stdout: %q", line)
	}
	dIdx := strings.Index(line, " D=")
	pIdx := strings.Index(line, " P=")
	if dIdx < 0 || pIdx < 0 || pIdx <= dIdx {
		return false, false, "", fmt.Errorf("unexpected stdout: %q", line)
	}
	eTok := line[2:dIdx]
	dTok := line[dIdx+3 : pIdx]
	pTok := line[pIdx+3:]
	if eTok != "0" && eTok != "1" {
		return false, false, "", fmt.Errorf("unexpected E token: %q", eTok)
	}
	if dTok != "0" && dTok != "1" {
		return false, false, "", fmt.Errorf("unexpected D token: %q", dTok)
	}
	return eTok == "1", dTok == "1", pTok, nil
}

// runTar is the shared body of the two methods: pipe stderr through a
// line-prefixing scanner, invoke `tart exec -i <vm> /bin/bash -c
// <script>`, and return the tar process's exit code.
func (r *tartTarRunner) runTar(ctx context.Context, entryName, phase string, stdin io.Reader, stdout io.Writer, script string) (int, error) {
	logW := r.logW
	if logW == nil {
		logW = log.Writer()
	}
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		prefix := fmt.Sprintf("snapshot %s %s: ", entryName, phase)
		for sc.Scan() {
			fmt.Fprintf(logW, "%s%s\n", prefix, sc.Text())
		}
	}()

	code, err := r.tr.ExecBinary(ctx, r.vmName, stdin, stdout, pw, bashShell(script))
	_ = pw.Close()
	<-done
	_ = pr.Close()
	return code, err
}

// newTarRunnerFn is the test seam the handlers go through to build a
// TarRunner. Production returns a tartTarRunner bound to the daemon's
// one *tart.Tart + the project's VM name.
var newTarRunnerFn = func(tr *tart.Tart, vmName string) snapshot.TarRunner {
	return &tartTarRunner{tr: tr, vmName: vmName, logW: log.Writer()}
}
