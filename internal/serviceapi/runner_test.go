package serviceapi

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitForHelperReady_ReturnsQuicklyWhenHelperUp(t *testing.T) {
	dir, err := os.MkdirTemp("", "hlp-rdy")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "helper.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })

	start := time.Now()
	waitForHelperReady(context.Background(), sock, 2*time.Second)
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 500*time.Millisecond,
		"gate should return quickly when helper is already up; took %s", elapsed)
}

func TestWaitForHelperReady_TimesOutWhenHelperMissing(t *testing.T) {
	start := time.Now()
	waitForHelperReady(context.Background(), "/tmp/does-not-exist-devm-helper.sock", 300*time.Millisecond)
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 300*time.Millisecond,
		"gate should wait for the full timeout when helper never appears")
	assert.Less(t, elapsed, 600*time.Millisecond,
		"gate should not wait beyond the timeout")
}
