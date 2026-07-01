package orchestrator

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeSnapTart writes a fake tart binary:
//   - `exec <vm> bash -c "cat ..."` (ReadSnapshot) → exits catCode; emits catOut to stdout, catErr to stderr
//   - `exec <vm> bash -c "mkdir ..."` (WriteSnapshot) → exits 0, logs the full argv
func makeSnapTart(t *testing.T, catOut, catErr string, catCode int) (*tart.Tart, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-tart")
	callLog := filepath.Join(dir, "calls.txt")
	catOutFile := filepath.Join(dir, "cat-out.txt")
	catErrFile := filepath.Join(dir, "cat-err.txt")

	require.NoError(t, os.WriteFile(catOutFile, []byte(catOut), 0o644))
	require.NoError(t, os.WriteFile(catErrFile, []byte(catErr), 0o644))

	exitLine := ""
	if catCode != 0 {
		exitLine = fmt.Sprintf("cat '%s' >&2; exit %d\n", catErrFile, catCode)
	}

	// Both ReadSnapshot and WriteSnapshot now call `bash -c <script>`.
	// Distinguish them by whether $5 (the script body) starts with "cat"
	// (ReadSnapshot) or anything else (WriteSnapshot).
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> '" + callLog + "'\n" +
		"case \"$3\" in\n" +
		"  bash)\n" +
		"    case \"$5\" in\n" +
		"      cat*)\n" +
		"        " + exitLine +
		"        cat '" + catOutFile + "'\n" +
		"        ;;\n" +
		"      *)\n" +
		"        exit 0\n" +
		"        ;;\n" +
		"    esac\n" +
		"    ;;\n" +
		"  *)\n" +
		"    exit 0\n" +
		"    ;;\n" +
		"esac\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr, callLog
}

func TestReadSnapshot_Success(t *testing.T) {
	tr, _ := makeSnapTart(t, "name: hello\n", "", 0)
	out, err := ReadSnapshot(tr, "x")
	assert.NoError(t, err)
	assert.Equal(t, "name: hello\n", out)
}

func TestReadSnapshot_NotFoundIsEmpty(t *testing.T) {
	// bash exits non-zero with "No such file" on stderr → clean miss.
	tr, _ := makeSnapTart(t, "", "bash: line 1: cat: $HOME/.devm/applied.yaml: No such file or directory", 1)
	out, err := ReadSnapshot(tr, "x")
	assert.NoError(t, err, "missing snapshot is not an error")
	assert.Equal(t, "", out)
}

func TestReadSnapshot_OtherErrorBubbles(t *testing.T) {
	// Exit non-zero without "No such file" in stderr → hard error.
	tr, _ := makeSnapTart(t, "", "permission denied", 2)
	_, err := ReadSnapshot(tr, "x")
	assert.Error(t, err)
}

func TestWriteSnapshot(t *testing.T) {
	tr, callLog := makeSnapTart(t, "", "", 0)
	err := WriteSnapshot(tr, "x", "rendered: yes\n")
	assert.NoError(t, err)

	// The call log should contain a bash -c invocation with base64-encoded content.
	logged, logErr := os.ReadFile(callLog)
	require.NoError(t, logErr)
	calls := string(logged)
	assert.Contains(t, calls, "bash", "WriteSnapshot must use bash -c")
	assert.Contains(t, calls, "applied.yaml.tmp", "must write via tmp file")
	assert.Contains(t, calls, "base64 -d", "content must be passed base64-encoded inline")
	// Verify the actual encoded content is present.
	encoded := base64.StdEncoding.EncodeToString([]byte("rendered: yes\n"))
	assert.Contains(t, calls, encoded, "encoded content must appear in argv")
}
