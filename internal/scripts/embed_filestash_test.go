package scripts

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFilestashEmbeddedConfigStarts unpacks the embedded config and
// binary into a scratch dir, spawns filestash against them, and
// asserts the server answers HTTP 200 within 10s. Fails on any
// filestash version bump that changes the config schema in a way
// that breaks startup — the fastest signal of an incompatible bump.
// Runs on linux/arm64 only (filestash binary is arm64 Linux); on
// other platforms it skips.
func TestFilestashEmbeddedConfigStarts(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Skip("filestash binary is linux/arm64-only; skipping on this platform")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "filestash")
	require.NoError(t, os.WriteFile(bin, EmbeddedFilestashBinary, 0o755))
	cfgDir := filepath.Join(dir, "data", "state", "config")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.json"), EmbeddedFilestashConfig, 0o600))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PORT=18941")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:18941/")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("embedded filestash config did not produce a serving instance within 10s")
}
