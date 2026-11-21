package mutagen

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type execCall struct {
	bin  string
	args []string
	env  []string
}

// fakeExec is an ExecFn double keyed by "<bin> <args joined by space>" so
// each test can script canned stdout/exit/err per invocation while still
// recording every call for argv assertions.
type fakeExec struct {
	calls   []execCall
	stdout  map[string]string
	exit    map[string]int
	failErr map[string]error
}

func newFakeExec() *fakeExec {
	return &fakeExec{stdout: map[string]string{}, exit: map[string]int{}, failErr: map[string]error{}}
}

func (f *fakeExec) Run(bin string, args []string, env []string) (string, string, int, error) {
	f.calls = append(f.calls, execCall{bin: bin, args: append([]string(nil), args...), env: append([]string(nil), env...)})
	key := bin + " " + strings.Join(args, " ")
	if err, ok := f.failErr[key]; ok {
		return "", "", 0, err
	}
	return f.stdout[key], "", f.exit[key], nil
}

func (f *fakeExec) lastCall() execCall {
	return f.calls[len(f.calls)-1]
}

func TestSyncCreate_BuildsArgvAndReturnsID(t *testing.T) {
	fake := newFakeExec()
	fake.stdout["/bin/mutagen sync create --name devm-p-l --configuration-file /cfg.yml -i /key -o StrictHostKeyChecking=yes /mac/mirror devm@10.0.0.5:/home/devm/repo"] =
		"Created session sync_abc123\n"

	cli := &CLI{Binary: "/bin/mutagen", Exec: fake.Run}
	id, err := cli.SyncCreate("devm-p-l",
		"/mac/mirror", "devm@10.0.0.5:/home/devm/repo",
		"/cfg.yml",
		[]string{"-i", "/key", "-o", "StrictHostKeyChecking=yes"})
	require.NoError(t, err)
	assert.Equal(t, "sync_abc123", id)

	assert.Equal(t, []string{
		"sync", "create",
		"--name", "devm-p-l",
		"--configuration-file", "/cfg.yml",
		"-i", "/key", "-o", "StrictHostKeyChecking=yes",
		"/mac/mirror", "devm@10.0.0.5:/home/devm/repo",
	}, fake.lastCall().args)
}

func TestSyncCreate_ParsesRealMutagenCreatedSessionLine(t *testing.T) {
	// Real v0.18.1 output interleaves a \r-terminated progress line
	// ("Creating session...") before the final "Created session <id>"
	// line, with no trailing newline separating them from the spinner.
	fake := newFakeExec()
	fake.stdout["/bin/mutagen sync create --name n --configuration-file /c.yml a b"] =
		"\rCreating session...                    \r  \rCreated session sync_P2RCcdZEjjwil6xBG02\n"

	cli := &CLI{Binary: "/bin/mutagen", Exec: fake.Run}
	id, err := cli.SyncCreate("n", "a", "b", "/c.yml", nil)
	require.NoError(t, err)
	assert.Equal(t, "sync_P2RCcdZEjjwil6xBG02", id)
}

func TestSyncCreate_NonZeroExitReturnsError(t *testing.T) {
	fake := newFakeExec()
	fake.stdout["/bin/mutagen sync create --name n --configuration-file /c.yml a b"] = ""
	fake.exit["/bin/mutagen sync create --name n --configuration-file /c.yml a b"] = 1

	cli := &CLI{Binary: "/bin/mutagen", Exec: fake.Run}
	_, err := cli.SyncCreate("n", "a", "b", "/c.yml", nil)
	require.Error(t, err)
}

func TestSyncCreate_ExecErrorPropagates(t *testing.T) {
	fake := newFakeExec()
	fake.failErr["/bin/mutagen sync create --name n --configuration-file /c.yml a b"] = errors.New("boom")

	cli := &CLI{Binary: "/bin/mutagen", Exec: fake.Run}
	_, err := cli.SyncCreate("n", "a", "b", "/c.yml", nil)
	require.Error(t, err)
}

func TestSyncList_ParsesAndFiltersByPrefix(t *testing.T) {
	fake := newFakeExec()
	fake.stdout["/bin/mutagen sync list --template {{json .}}"] = `[` +
		`{"identifier":"sync_a","name":"devm-p-l","status":"watching"},` +
		`{"identifier":"sync_b","name":"other-project","status":"watching"}` +
		`]`

	cli := &CLI{Binary: "/bin/mutagen", Exec: fake.Run}
	sessions, err := cli.SyncList("devm-p-")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, SyncSession{ID: "sync_a", Name: "devm-p-l", Status: "watching"}, sessions[0])
}

func TestSyncList_EmptyPrefixReturnsAll(t *testing.T) {
	fake := newFakeExec()
	fake.stdout["/bin/mutagen sync list --template {{json .}}"] = `[]`

	cli := &CLI{Binary: "/bin/mutagen", Exec: fake.Run}
	sessions, err := cli.SyncList("")
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestSyncControlCommands_BuildCorrectArgv(t *testing.T) {
	fake := newFakeExec()
	cli := &CLI{Binary: "/bin/mutagen", Exec: fake.Run}

	require.NoError(t, cli.SyncFlush("sync_a"))
	assert.Equal(t, []string{"sync", "flush", "sync_a"}, fake.lastCall().args)

	require.NoError(t, cli.SyncPause("sync_a"))
	assert.Equal(t, []string{"sync", "pause", "sync_a"}, fake.lastCall().args)

	require.NoError(t, cli.SyncResume("sync_a"))
	assert.Equal(t, []string{"sync", "resume", "sync_a"}, fake.lastCall().args)

	require.NoError(t, cli.SyncTerminate("sync_a"))
	assert.Equal(t, []string{"sync", "terminate", "sync_a"}, fake.lastCall().args)
}

func TestDaemonStart_SetsDataDirAndParsesPIDViaLsof(t *testing.T) {
	fake := newFakeExec()
	fake.stdout["/bin/mutagen daemon start"] = ""
	fake.stdout["lsof -t /data/daemon/daemon.lock"] = "54321\n"

	cli := &CLI{Binary: "/bin/mutagen", DataDir: "/data", Exec: fake.Run}
	pid, err := cli.DaemonStart()
	require.NoError(t, err)
	assert.Equal(t, 54321, pid)

	require.Len(t, fake.calls, 2)
	startCall := fake.calls[0]
	assert.Equal(t, "/bin/mutagen", startCall.bin)
	assert.Equal(t, []string{"daemon", "start"}, startCall.args)
	assert.Contains(t, startCall.env, "MUTAGEN_DATA_DIRECTORY=/data")

	lsofCall := fake.calls[1]
	assert.Equal(t, "lsof", lsofCall.bin)
	assert.Equal(t, []string{"-t", "/data/daemon/daemon.lock"}, lsofCall.args)
}

func TestDaemonStart_RequiresDataDir(t *testing.T) {
	fake := newFakeExec()
	fake.stdout["/bin/mutagen daemon start"] = ""

	cli := &CLI{Binary: "/bin/mutagen", Exec: fake.Run}
	_, err := cli.DaemonStart()
	require.Error(t, err)
}

func TestDaemonStop_InvokesDaemonStop(t *testing.T) {
	fake := newFakeExec()
	fake.stdout["/bin/mutagen daemon stop"] = ""

	cli := &CLI{Binary: "/bin/mutagen", Exec: fake.Run}
	require.NoError(t, cli.DaemonStop())
	assert.Equal(t, []string{"daemon", "stop"}, fake.lastCall().args)
}

func TestCLI_ExtraEnvAppendedToEveryCall(t *testing.T) {
	fake := newFakeExec()
	fake.stdout["/bin/mutagen sync flush sync_a"] = ""

	cli := &CLI{Binary: "/bin/mutagen", ExtraEnv: []string{"FOO=bar"}, Exec: fake.Run}
	require.NoError(t, cli.SyncFlush("sync_a"))
	assert.Contains(t, fake.lastCall().env, "FOO=bar")
}

func TestOSExec_RunsRealCommand(t *testing.T) {
	stdout, _, code, err := OSExec("/bin/echo", []string{"hello"}, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "hello\n", stdout)
}

func TestOSExec_NonZeroExitNoError(t *testing.T) {
	_, _, code, err := OSExec("/usr/bin/false", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, code)
}
