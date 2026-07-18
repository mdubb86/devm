package tart

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeTart drops a `tart` shell script into dir with the given
// body. PATH is prepended with dir for the test's lifetime.
func writeFakeTart(t *testing.T, dir, body string) {
	t.Helper()
	script := "#!/bin/sh\n" + body + "\n"
	path := filepath.Join(dir, "tart")
	require.NoError(t, os.WriteFile(path, []byte(script), 0755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestTart_IP_ParsesOutput(t *testing.T) {
	dir := t.TempDir()
	writeFakeTart(t, dir, `echo "192.168.64.5"`)

	tr := New()
	ip, err := tr.IP(context.Background(), "myproj-sbx")
	require.NoError(t, err)
	assert.Equal(t, "192.168.64.5", ip)
}

func TestTart_Stop_PassesArgvCorrectly(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	writeFakeTart(t, dir, `echo "$@" > `+argsFile+`; exit 0`)
	tr := New()
	require.NoError(t, tr.Stop(context.Background(), "foo-sbx"))
	got, _ := os.ReadFile(argsFile)
	assert.Equal(t, "stop foo-sbx\n", string(got))
}

func TestTart_Exec_CapturesOutputAndExit(t *testing.T) {
	dir := t.TempDir()
	writeFakeTart(t, dir,
		`echo "hello from fake"; >&2 echo "warning"; exit 3`)
	tr := New()
	r := tr.Exec(context.Background(), "foo", []string{"echo", "x"})
	assert.Equal(t, "hello from fake\n", r.Stdout)
	assert.Equal(t, "warning\n", r.Stderr)
	assert.Equal(t, 3, r.ExitCode)
}

func TestTart_Stop_PropagatesError(t *testing.T) {
	dir := t.TempDir()
	writeFakeTart(t, dir, `>&2 echo "vm not found"; exit 1`)
	tr := New()
	err := tr.Stop(context.Background(), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vm not found")
}

func TestTart_List_ParsesJSON(t *testing.T) {
	dir := t.TempDir()
	body := `cat <<'EOF'
[
  {"Name": "devm-base", "State": "stopped"},
  {"Name": "acme-sbx", "State": "running"}
]
EOF`
	writeFakeTart(t, dir, body)
	tr := New()
	vms, err := tr.List(context.Background())
	require.NoError(t, err)
	require.Len(t, vms, 2)
	assert.Equal(t, "devm-base", vms[0].Name)
	assert.False(t, vms[0].Running)
	assert.Equal(t, "acme-sbx", vms[1].Name)
	assert.True(t, vms[1].Running)
}

func TestTart_Run_ConstructsCorrectArgv(t *testing.T) {
	tr := New()
	cmd, err := tr.Run(context.Background(), "myvm", RunOpts{
		NoGraphics: true,
		DirMounts: []DirMount{
			{Name: "workspace", HostPath: "/Users/me/proj", Tag: "ws"},
			{Name: "secrets", HostPath: "/Users/me/.config", ReadOnly: true},
		},
	})
	require.NoError(t, err)
	// We don't actually run the cmd here — just check the args.
	args := cmd.Args[1:] // [0] is the binary path
	assert.Contains(t, args, "run")
	assert.Contains(t, args, "--no-graphics")
	assert.Contains(t, args, "--dir=workspace:/Users/me/proj:tag=ws")
	assert.Contains(t, args, "--dir=secrets:/Users/me/.config:ro")
	assert.Contains(t, args, "myvm")
}

func TestTart_Run_DoesNotSetSetsid(t *testing.T) {
	// SysProcAttr.Setsid is the supervisor's responsibility (different
	// supervisors may want different posture). The wrapper must leave it
	// alone.
	tr := New()
	cmd, err := tr.Run(context.Background(), "myvm", RunOpts{NoGraphics: true})
	require.NoError(t, err)
	if cmd.SysProcAttr != nil {
		// Cannot directly check Setsid without importing syscall;
		// the contract is "we don't touch it", so verifying nil is
		// sufficient for the unit test. If a later supervisor sets it,
		// the SysProcAttr will be non-nil.
		t.Fatal("Run() must not touch SysProcAttr; supervisor decides")
	}
}

func TestRunEmitsNetSoftnet(t *testing.T) {
	tr := New()
	cmd, err := tr.Run(context.Background(), "vm", RunOpts{NoGraphics: true, NetSoftnet: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "--net-softnet") {
		t.Fatalf("expected --net-softnet, got %v", cmd.Args)
	}
	if cmd.Args[len(cmd.Args)-1] != "vm" {
		t.Fatalf("name must be last positional arg, got %v", cmd.Args)
	}
}

func TestExecStream_StreamsLinesAndExit(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tart")
	require.NoError(t, os.WriteFile(fake,
		[]byte("#!/bin/bash\necho out1\necho err1 >&2\necho out2\nexit 3\n"), 0o755))
	tr := &Tart{Path: fake}
	var mu sync.Mutex
	var got []string
	code, err := tr.ExecStream(context.Background(), "vm", nil,
		[]string{"true"}, func(stream, line string) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, stream+":"+line)
		})
	require.NoError(t, err)
	require.Equal(t, 3, code)
	require.Contains(t, got, "stdout:out1")
	require.Contains(t, got, "stderr:err1")
	require.Contains(t, got, "stdout:out2")
}
