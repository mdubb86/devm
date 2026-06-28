package provision

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeTartOK drops a tart shell script into dir that succeeds for
// every invocation. PATH is prepended.
func writeFakeTartOK(t *testing.T, dir string) {
	t.Helper()
	script := "#!/bin/sh\necho fake-tart-output\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tart"), []byte(script), 0755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// writeFakeTartFailingAt drops a tart shell script that succeeds for
// every invocation EXCEPT those whose argv contains `failureMarker`,
// for which it exits 1.
func writeFakeTartFailingAt(t *testing.T, dir, failureMarker string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    *%s*) echo "step failure marker matched: $arg" >&2; exit 1 ;;
  esac
done
exit 0
`, failureMarker)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tart"), []byte(script), 0755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestProvisioner_RunsAllStepsOnHappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFakeTartOK(t, dir)

	p := &Provisioner{
		Tart:            tart.New(),
		VMName:          "myproj-sbx",
		Cfg:             schema.Config{Project: schema.Project{ID: "myproj", SandboxName: "myproj-sbx"}},
		CARootPEM:       []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
		WorkspaceVMPath: "/Users/test/myproj",
	}
	var buf bytes.Buffer
	err := p.Run(context.Background(), &buf)
	require.NoError(t, err)
	out := buf.String()

	// All expected step headers appear in order.
	expectedSteps := []string{
		"[step: mkdir workspace parents]",
		"[step: install CA root]",
		"[step: write Caddyfile]",
		"[step: write dnsmasq config]",
		"[step: reload base services]",
		"[step: apt-get update]",
		"[step: apt-get install packages]",
		"[step: run install commands]",
		"[step: install service units]",
		"[step: systemctl daemon-reload]",
		"[step: enable + start services]",
		"[step: apply masks]",
	}
	prev := -1
	for _, marker := range expectedSteps {
		idx := strings.Index(out, marker)
		require.GreaterOrEqual(t, idx, 0, "missing step header: %s\nfull output:\n%s", marker, out)
		assert.Greater(t, idx, prev, "step %s out of order", marker)
		prev = idx
	}
}

// Note: failure-isolation testing (verifying that later steps DON'T
// run after an earlier one fails) is harder because the fake tart
// would need to track invocations. Acceptable simplification: verify
// the error propagation behavior only.
func TestProvisioner_FailsFastOnTartError(t *testing.T) {
	dir := t.TempDir()
	// Fail when argv contains "apt-get" — i.e., the apt update step.
	writeFakeTartFailingAt(t, dir, "apt-get")

	p := &Provisioner{
		Tart:   tart.New(),
		VMName: "myproj-sbx",
		Cfg: schema.Config{
			Project:  schema.Project{ID: "myproj", SandboxName: "myproj-sbx"},
			Packages: []string{"jq"}, // forces apt-get update to actually run
		},
		CARootPEM:       []byte("fake-pem\n"),
		WorkspaceVMPath: "/Users/test/myproj",
	}
	var buf bytes.Buffer
	err := p.Run(context.Background(), &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apt-get update")

	out := buf.String()
	// Steps after the failure must NOT appear.
	assert.NotContains(t, out, "[step: install service units]")
	assert.NotContains(t, out, "[step: enable + start services]")
}

func TestProvisioner_RoutingOnlyServiceSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFakeTartOK(t, dir)

	p := &Provisioner{
		Tart:   tart.New(),
		VMName: "myproj-sbx",
		Cfg: schema.Config{
			Project: schema.Project{ID: "myproj", SandboxName: "myproj-sbx"},
			Services: map[string]schema.Service{
				// Routing-only (no Exec, no Systemd).
				"routing-only": {Hostname: "x.test", Port: 8080},
				// Has Exec, should get systemctl enable --now.
				"with-exec": {Exec: []string{"/bin/true"}},
			},
		},
		CARootPEM:       []byte("fake\n"),
		WorkspaceVMPath: "/Users/test/myproj",
	}
	var buf bytes.Buffer
	require.NoError(t, p.Run(context.Background(), &buf))

	out := buf.String()
	assert.Contains(t, out, "(skip routing-only — routing-only declaration)")
}
