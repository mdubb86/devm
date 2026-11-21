package daemonlog

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWarnf_WritesToStderrWithPrefix(t *testing.T) {
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = orig })

	Warnf("category: something happened: %s", "detail")

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	out := buf.String()
	assert.True(t, strings.HasPrefix(out, "[WARN] "), "expected [WARN] prefix; got %q", out)
	assert.Contains(t, out, "category: something happened: detail")
	assert.True(t, strings.HasSuffix(out, "\n"), "expected trailing newline; got %q", out)
	assert.NotContains(t, out, "goroutine ", "Warnf must not append a stack trace")
}
