package serviceapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeTartForRunner drops a fake `tart` shell script into dir and
// returns the path. The fake records argv and stdin so the test can
// prove the exec shape is right.
func writeFakeTartForRunner(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "tart")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/bash\n"+body+"\n"), 0o755))
	return path
}

func TestTartHookRunner_RunBackup_StreamsStdoutAndExit(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "argv")
	fake := writeFakeTartForRunner(t, dir,
		`printf '%s\n' "$@" > `+argsFile+`
printf '\x00\x01\x02binary'
exit 0`)

	r := &tartHookRunner{tr: &tart.Tart{Path: fake}, vmName: "proj"}
	var out bytes.Buffer
	code, err := r.RunBackup(context.Background(), "pg", &out)
	require.NoError(t, err)
	require.Equal(t, 0, code)
	require.Equal(t, []byte{0, 1, 2, 'b', 'i', 'n', 'a', 'r', 'y'}, out.Bytes())

	argv, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	got := string(argv)
	// `tart exec -i proj /bin/bash -c "...; backup_pg"`
	require.Contains(t, got, "exec")
	require.Contains(t, got, "-i")
	require.Contains(t, got, "proj")
	require.Contains(t, got, "/bin/bash")
	require.Contains(t, got, "-c")
	require.Contains(t, got, "backup_pg")
	require.Contains(t, got, "source /opt/devm/devm.sh")
	require.Contains(t, got, "/opt/devm/devm.me.sh")
}

func TestTartHookRunner_RunRestore_PipesStdinAndExit(t *testing.T) {
	dir := t.TempDir()
	stdinFile := filepath.Join(dir, "stdin")
	argsFile := filepath.Join(dir, "argv")
	fake := writeFakeTartForRunner(t, dir,
		`printf '%s\n' "$@" > `+argsFile+`
cat > `+stdinFile+`
exit 0`)

	r := &tartHookRunner{tr: &tart.Tart{Path: fake}, vmName: "proj"}
	payload := []byte{0x7f, 'r', 'e', 's', 't', 0x00, 'o', 'r', 'e'}
	code, err := r.RunRestore(context.Background(), "pg",
		io.NopCloser(bytes.NewReader(payload)))
	require.NoError(t, err)
	require.Equal(t, 0, code)

	gotStdin, err := os.ReadFile(stdinFile)
	require.NoError(t, err)
	require.Equal(t, payload, gotStdin)

	argv, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	require.Contains(t, string(argv), "restore_pg")
}

func TestTartHookRunner_RunBackup_PropagatesNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	fake := writeFakeTartForRunner(t, dir, `exit 42`)
	r := &tartHookRunner{tr: &tart.Tart{Path: fake}, vmName: "proj"}
	code, err := r.RunBackup(context.Background(), "pg", io.Discard)
	require.NoError(t, err)
	require.Equal(t, 42, code)
}

// fakeTartRunner wires a programmable `tart` shell script to a
// *tart.Tart so pre/post hook tests can assert on both the invoked
// script (lastExecCmd) and the resulting stdout / stderr / exit.
// stubExec registers per-hook output keyed by the final command in the
// -c script — which is either a hook function name (e.g. pre_backup_foo)
// or the first word of the final `;`-separated segment (e.g. declare,
// for HookExists).
type fakeTartRunner struct {
	t        *testing.T
	dir      string
	stubsDir string
	argvFile string
	tart     *tart.Tart
}

func newFakeTartRunner(t *testing.T) *fakeTartRunner {
	t.Helper()
	dir := t.TempDir()
	stubsDir := filepath.Join(dir, "stubs")
	require.NoError(t, os.MkdirAll(stubsDir, 0o755))
	argvFile := filepath.Join(dir, "argv.last")
	// The fake records argv, then dispatches on the "first word of the
	// final `;`-separated segment" of the -c script. That segment is
	// the hook invocation (e.g. `pre_backup_foo`) or the introspection
	// call (`declare -F ...`).
	body := `printf '%s\n' "$@" > ` + argvFile + `
script="${!#}"
hook=$(printf '%s' "$script" | awk -F';' '{print $NF}' | awk '{print $1}')
if [ -f "` + stubsDir + `/$hook.stdout" ]; then
  cat "` + stubsDir + `/$hook.stdout"
fi
if [ -f "` + stubsDir + `/$hook.stderr" ]; then
  cat "` + stubsDir + `/$hook.stderr" >&2
fi
if [ -f "` + stubsDir + `/$hook.exit" ]; then
  exit $(cat "` + stubsDir + `/$hook.exit")
fi
exit 0`
	binPath := writeFakeTartForRunner(t, dir, body)
	return &fakeTartRunner{
		t:        t,
		dir:      dir,
		stubsDir: stubsDir,
		argvFile: argvFile,
		tart:     &tart.Tart{Path: binPath},
	}
}

// stubExec registers what the fake will emit when the final command in
// a tart-exec script matches `hook`. `hook` is the function name
// (pre_backup_foo, post_backup_foo, ...) or `declare` for HookExists.
func (f *fakeTartRunner) stubExec(hook, stdout, stderrOut string, exitCode int) {
	f.t.Helper()
	require.NoError(f.t, os.WriteFile(filepath.Join(f.stubsDir, hook+".stdout"), []byte(stdout), 0o644))
	require.NoError(f.t, os.WriteFile(filepath.Join(f.stubsDir, hook+".stderr"), []byte(stderrOut), 0o644))
	require.NoError(f.t, os.WriteFile(filepath.Join(f.stubsDir, hook+".exit"), []byte(fmt.Sprintf("%d", exitCode)), 0o644))
}

// lastExecCmd returns the argv of the most recent tart invocation as
// newline-separated words. Tests use assert.Contains against it.
func (f *fakeTartRunner) lastExecCmd() string {
	f.t.Helper()
	data, err := os.ReadFile(f.argvFile)
	require.NoError(f.t, err)
	return string(data)
}

func TestTartHookRunner_RunPreBackup_CapturesStdoutStrict(t *testing.T) {
	r := newFakeTartRunner(t)
	r.stubExec("pre_backup_foo", "file:///tmp/staging\n", "", 0)
	runner := &tartHookRunner{tr: r.tart, vmName: "vm"}
	line, code, err := runner.RunPreBackup(context.Background(), "foo", map[string]string{
		"DEVM_SNAPSHOT_NAME": "foo",
		"DEVM_SNAPSHOT_PATH": "/home/devm/x",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "file:///tmp/staging", line)
	// env surfaces into the invoked command, single-quoted
	assert.Contains(t, r.lastExecCmd(), "DEVM_SNAPSHOT_NAME='foo'")
	assert.Contains(t, r.lastExecCmd(), "DEVM_SNAPSHOT_PATH='/home/devm/x'")
}

func TestTartHookRunner_RunPostBackup_PassesPreExitCode(t *testing.T) {
	r := newFakeTartRunner(t)
	r.stubExec("post_backup_foo", "", "", 0)
	runner := &tartHookRunner{tr: r.tart, vmName: "vm"}
	code, err := runner.RunPostBackup(context.Background(), "foo", map[string]string{
		"DEVM_PRE_EXIT_CODE": "0",
		"DEVM_CAPTURED_PATH": "/home/devm/x",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Contains(t, r.lastExecCmd(), "DEVM_PRE_EXIT_CODE='0'")
	assert.Contains(t, r.lastExecCmd(), "DEVM_CAPTURED_PATH='/home/devm/x'")
}

func TestTartHookRunner_StderrRoutedToDaemonLog(t *testing.T) {
	r := newFakeTartRunner(t)
	r.stubExec("pre_backup_foo", "", "stopping buildkitd\n", 0)
	var logged bytes.Buffer
	runner := &tartHookRunner{tr: r.tart, vmName: "vm", logW: &logged}
	_, _, err := runner.RunPreBackup(context.Background(), "foo", nil)
	require.NoError(t, err)
	assert.Contains(t, logged.String(), "snapshot foo pre_backup: stopping buildkitd")
}

func TestTartHookRunner_RunPreRestoreAndRunPostRestore(t *testing.T) {
	r := newFakeTartRunner(t)
	r.stubExec("pre_restore_foo", "", "pre r\n", 7)
	r.stubExec("post_restore_foo", "", "post r\n", 0)
	var logged bytes.Buffer
	runner := &tartHookRunner{tr: r.tart, vmName: "vm", logW: &logged}

	code, err := runner.RunPreRestore(context.Background(), "foo", nil)
	require.NoError(t, err)
	assert.Equal(t, 7, code)
	assert.Contains(t, logged.String(), "snapshot foo pre_restore: pre r")

	code, err = runner.RunPostRestore(context.Background(), "foo", map[string]string{"DEVM_PRE_EXIT_CODE": "7"})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Contains(t, logged.String(), "snapshot foo post_restore: post r")
	assert.Contains(t, r.lastExecCmd(), "DEVM_PRE_EXIT_CODE='7'")
}

func TestTartHookRunner_HookExists_ParsesDeclareOutput(t *testing.T) {
	r := newFakeTartRunner(t)
	// Guest has backup_foo and pre_backup_foo defined, nothing else.
	// `declare -F <names>` with named args prints one bare function
	// name per line on stdout for each present name; absent names
	// surface on stderr (and bash exits non-zero, which HookExists
	// must ignore).
	r.stubExec("declare",
		"backup_foo\npre_backup_foo\n",
		"bash: declare: restore_foo: not found\nbash: declare: post_backup_foo: not found\n",
		1)
	runner := &tartHookRunner{tr: r.tart, vmName: "vm"}
	got, err := runner.HookExists(context.Background(), "foo")
	require.NoError(t, err)
	assert.Equal(t, true, got.Backup)
	assert.Equal(t, true, got.PreBackup)
	assert.Equal(t, false, got.Restore)
	assert.Equal(t, false, got.PostBackup)
	assert.Equal(t, false, got.PreRestore)
	assert.Equal(t, false, got.PostRestore)
	assert.Contains(t, r.lastExecCmd(), "declare -F backup_foo")
	assert.Contains(t, r.lastExecCmd(), "post_restore_foo")
}

// TestTartHookRunner_HookExists_StderrRoutedToDaemonLog pins M-2:
// HookExists must route stderr through the logW line-prefixer so a
// devm.sh sourcing failure (or any other noise) surfaces in the daemon
// log instead of vanishing. Before the fix, stderr went to io.Discard
// and operators had no signal when the sourcing step failed silently.
func TestTartHookRunner_HookExists_StderrRoutedToDaemonLog(t *testing.T) {
	r := newFakeTartRunner(t)
	r.stubExec("declare",
		"backup_foo\n",
		"bash: /opt/devm/devm.sh: syntax error near unexpected token\n",
		1)
	var logged bytes.Buffer
	runner := &tartHookRunner{tr: r.tart, vmName: "vm", logW: &logged}
	_, err := runner.HookExists(context.Background(), "foo")
	require.NoError(t, err)
	assert.Contains(t, logged.String(),
		"snapshot foo hook_exists: bash: /opt/devm/devm.sh: syntax error near unexpected token")
}

// TestTartHookRunner_HookExists_RealBashOutputShape pins the exact
// shape real bash emits for `declare -F name1 name2 …`: one bare
// function name per present name on stdout, nothing else. Mixed in
// here are a blank line and an unrelated stray line of noise; neither
// may flip a presence bit, and only the matching names do.
func TestTartHookRunner_HookExists_RealBashOutputShape(t *testing.T) {
	r := newFakeTartRunner(t)
	r.stubExec("declare",
		"backup_foo\n\nunexpected text\npre_restore_foo\npost_restore_foo\n",
		"",
		1)
	runner := &tartHookRunner{tr: r.tart, vmName: "vm"}
	got, err := runner.HookExists(context.Background(), "foo")
	require.NoError(t, err)
	assert.Equal(t, true, got.Backup)
	assert.Equal(t, true, got.PreRestore)
	assert.Equal(t, true, got.PostRestore)
	assert.Equal(t, false, got.Restore)
	assert.Equal(t, false, got.PreBackup)
	assert.Equal(t, false, got.PostBackup)
}

func TestHookShellWithEnv_QuotesSingleQuotes(t *testing.T) {
	argv := hookShellWithEnv("pre_backup_foo", map[string]string{
		"K": "val with ' quote",
	})
	require.Len(t, argv, 3)
	// The single-quote inside the value must be rendered as '\''
	assert.Contains(t, argv[2], `K='val with '\'' quote'`)
	assert.Contains(t, argv[2], "source /opt/devm/devm.sh")
	assert.Contains(t, argv[2], "pre_backup_foo")
}

func TestHookShellWithEnv_NoEnvOmitsExport(t *testing.T) {
	argv := hookShellWithEnv("post_backup_foo", nil)
	require.Len(t, argv, 3)
	// No leading "export " or empty-statement ";".
	assert.False(t, bytesStartsWith(argv[2], "export "))
	assert.False(t, bytesStartsWith(argv[2], "; "))
	assert.Contains(t, argv[2], "source /opt/devm/devm.sh")
	assert.Contains(t, argv[2], "post_backup_foo")
}

// bytesStartsWith is a tiny helper so the "no leading export" check
// reads cleanly at the call site without pulling in another import.
func bytesStartsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestTartTarRunner_BackupDir_CdsAndStreams(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "argv")
	fake := writeFakeTartForRunner(t, dir,
		`printf '%s\n' "$@" > `+argsFile+`
printf 'TARBYTES'
exit 0`)
	r := &tartTarRunner{tr: &tart.Tart{Path: fake}, vmName: "vm"}

	var out bytes.Buffer
	code, err := r.RunBackupTar(context.Background(), "pg", "/var/lib/postgresql", "postgresql", true, &out)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "TARBYTES", out.String())

	argv, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	got := string(argv)
	assert.Contains(t, got, "exec")
	assert.Contains(t, got, "-i")
	assert.Contains(t, got, "vm")
	assert.Contains(t, got, "/bin/bash")
	assert.Contains(t, got, "-c")
	// Dir branch: cd into source, then `sudo tar -cf - .` — sudo so tar
	// can read root-owned entries.
	assert.Contains(t, got, "cd '/var/lib/postgresql'")
	assert.Contains(t, got, "sudo tar -cf - .")
}

func TestTartTarRunner_BackupFile_TransformsBasename(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "argv")
	fake := writeFakeTartForRunner(t, dir,
		`printf '%s\n' "$@" > `+argsFile+`
exit 0`)
	r := &tartTarRunner{tr: &tart.Tart{Path: fake}, vmName: "vm"}

	// Source is a staged file `/tmp/staging/db.sql`, but the declared
	// basename is `data.sql` — the transform renames it on the fly.
	_, err := r.RunBackupTar(context.Background(), "pg", "/tmp/staging/db.sql", "data.sql", false, io.Discard)
	require.NoError(t, err)

	argv, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	got := string(argv)
	// File branch: cd into parent, then `sudo tar --transform=... -cf - <src>`.
	assert.Contains(t, got, "cd '/tmp/staging'")
	assert.Contains(t, got, "sudo tar --transform='s|^db.sql$|data.sql|'")
	assert.Contains(t, got, "-cf - 'db.sql'")
}

// TestTartTarRunner_RestoreDir_MkdirsParent pins the carry-forward
// invariant from Task 8: a path-mode restore must run `mkdir -p` on
// the declared path before `tar -xf`, so a fresh guest filesystem or
// one where an operator renamed the declared path extracts cleanly
// instead of erroring at `tar -C`. The restore pipeline above relies
// on this and does not touch the guest FS on its own.
func TestTartTarRunner_RestoreDir_MkdirsParent(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "argv")
	stdinFile := filepath.Join(dir, "stdin")
	fake := writeFakeTartForRunner(t, dir,
		`printf '%s\n' "$@" > `+argsFile+`
cat > `+stdinFile+`
exit 0`)
	r := &tartTarRunner{tr: &tart.Tart{Path: fake}, vmName: "vm"}

	payload := []byte("RESTOREBYTES")
	code, err := r.RunRestoreTar(context.Background(), "pg", "/var/lib/postgresql", true, bytes.NewReader(payload))
	require.NoError(t, err)
	assert.Equal(t, 0, code)

	gotStdin, err := os.ReadFile(stdinFile)
	require.NoError(t, err)
	assert.Equal(t, payload, gotStdin)

	argv, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	got := string(argv)
	// mkdir -p the declared path first (it IS the extract target for a dir);
	// both mkdir and tar run under sudo so root-owned entries extract with
	// their archived uid/gid and a non-devm-writable parent isn't a blocker.
	assert.Contains(t, got, "sudo mkdir -p '/var/lib/postgresql'")
	assert.Contains(t, got, "sudo tar -xf - --same-owner --same-permissions -C '/var/lib/postgresql'")
}

// TestTartTarRunner_RestoreFile_MkdirsParent is the file-branch mirror:
// `mkdir -p` is run against the parent of declaredPath, because a
// single-file archive is extracted into the parent dir.
func TestTartTarRunner_RestoreFile_MkdirsParent(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "argv")
	fake := writeFakeTartForRunner(t, dir,
		`printf '%s\n' "$@" > `+argsFile+`
cat > /dev/null
exit 0`)
	r := &tartTarRunner{tr: &tart.Tart{Path: fake}, vmName: "vm"}

	_, err := r.RunRestoreTar(context.Background(), "pg", "/home/devm/data.sql", false, bytes.NewReader([]byte("x")))
	require.NoError(t, err)

	argv, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	got := string(argv)
	assert.Contains(t, got, "sudo mkdir -p '/home/devm'")
	assert.Contains(t, got, "sudo tar -xf - --same-owner --same-permissions -C '/home/devm'")
}

func TestTartTarRunner_NonZeroExitPropagates(t *testing.T) {
	dir := t.TempDir()
	fake := writeFakeTartForRunner(t, dir, `exit 7`)
	r := &tartTarRunner{tr: &tart.Tart{Path: fake}, vmName: "vm"}
	code, err := r.RunBackupTar(context.Background(), "pg", "/tmp/x", "x", true, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, 7, code)
}

func TestTartTarRunner_StderrRoutedToDaemonLog(t *testing.T) {
	dir := t.TempDir()
	fake := writeFakeTartForRunner(t, dir, `echo "tar: Removing leading /" >&2
exit 0`)
	var logged bytes.Buffer
	r := &tartTarRunner{tr: &tart.Tart{Path: fake}, vmName: "vm", logW: &logged}
	_, err := r.RunBackupTar(context.Background(), "pg", "/tmp/x", "x", true, io.Discard)
	require.NoError(t, err)
	assert.Contains(t, logged.String(), "snapshot pg backup_tar: tar: Removing leading /")
}

// TestTartTarRunner_StatPath_ExistingDir pins the happy-path parse of
// the StatPath wrapper's single-line protocol: a dir hit resolves back
// through E=1 D=1 P=<path>.
func TestTartTarRunner_StatPath_ExistingDir(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "argv")
	fake := writeFakeTartForRunner(t, dir,
		`printf '%s\n' "$@" > `+argsFile+`
printf 'E=1 D=1 P=/home/devm/.claude\n'
exit 0`)
	r := &tartTarRunner{tr: &tart.Tart{Path: fake}, vmName: "vm"}
	exists, isDir, resolved, err := r.StatPath(context.Background(), "claude", "/home/devm/.claude")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.True(t, isDir)
	assert.Equal(t, "/home/devm/.claude", resolved)

	argv, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	got := string(argv)
	assert.Contains(t, got, "P='/home/devm/.claude'")
	assert.Contains(t, got, "readlink -f --")
}

// TestTartTarRunner_StatPath_Missing pins the absent-path signal:
// exists=false, err=nil so capture surfaces the specific
// "does not exist" error upstream.
func TestTartTarRunner_StatPath_Missing(t *testing.T) {
	dir := t.TempDir()
	fake := writeFakeTartForRunner(t, dir,
		`printf 'E=0 D=0 P=\n'
exit 0`)
	r := &tartTarRunner{tr: &tart.Tart{Path: fake}, vmName: "vm"}
	exists, isDir, resolved, err := r.StatPath(context.Background(), "claude", "/does/not/exist")
	require.NoError(t, err)
	assert.False(t, exists)
	assert.False(t, isDir)
	assert.Equal(t, "", resolved)
}

// TestTartTarRunner_StatPath_TransportError pins the "tart/bash
// unreachable" signal: a non-zero shell exit surfaces as err so callers
// can't mistake it for a benign "path absent".
func TestTartTarRunner_StatPath_TransportError(t *testing.T) {
	dir := t.TempDir()
	fake := writeFakeTartForRunner(t, dir, `exit 125`)
	r := &tartTarRunner{tr: &tart.Tart{Path: fake}, vmName: "vm"}
	_, _, _, err := r.StatPath(context.Background(), "claude", "/home/devm/.claude")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stat_path")
}

// TestTartTarRunner_QuotesPathsSafely pins the single-quote escaping
// for a path with embedded single-quote (`'\''`): a malformed path
// would break the shell command.
func TestTartTarRunner_QuotesPathsSafely(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "argv")
	fake := writeFakeTartForRunner(t, dir,
		`printf '%s\n' "$@" > `+argsFile+`
exit 0`)
	r := &tartTarRunner{tr: &tart.Tart{Path: fake}, vmName: "vm"}

	_, err := r.RunBackupTar(context.Background(), "pg", "/tmp/o'quote", "o'quote", true, io.Discard)
	require.NoError(t, err)

	argv, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	got := string(argv)
	// The bash command contains `cd '/tmp/o'\''quote'` after escaping.
	assert.Contains(t, got, `cd '/tmp/o'\''quote'`)
}
