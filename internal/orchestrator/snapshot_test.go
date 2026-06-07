package orchestrator

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/stretchr/testify/assert"
)

// stubRunner satisfies sandbox.Runner with scriptable outputs and
// records all invocations across Output/Run/RunStdin.
type stubRunner struct {
	outputOut    string
	outputErr    error
	runErr       error
	runStdinSeen string
	lastArgs     [][]string
}

func (s *stubRunner) Output(name string, args ...string) ([]byte, error) {
	s.lastArgs = append(s.lastArgs, append([]string{name}, args...))
	return []byte(s.outputOut), s.outputErr
}
func (s *stubRunner) Run(name string, args ...string) error {
	s.lastArgs = append(s.lastArgs, append([]string{name}, args...))
	return s.runErr
}
func (s *stubRunner) RunStdin(stdin, name string, args ...string) error {
	s.runStdinSeen = stdin
	s.lastArgs = append(s.lastArgs, append([]string{name}, args...))
	return s.runErr
}

func TestReadSnapshot_Success(t *testing.T) {
	r := &stubRunner{outputOut: "name: hello\n"}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	out, err := ReadSnapshot(sb)
	assert.NoError(t, err)
	assert.Equal(t, "name: hello\n", out)
	assert.Equal(t, []string{"sbx", "exec", "x", "cat", "/home/agent/.devm/applied.yaml"}, r.lastArgs[0])
}

func TestReadSnapshot_NotFoundIsEmpty(t *testing.T) {
	r := &stubRunner{outputErr: errors.New("exit status 1: cat: /home/agent/.devm/applied.yaml: No such file or directory")}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	out, err := ReadSnapshot(sb)
	assert.NoError(t, err, "missing snapshot is not an error")
	assert.Equal(t, "", out)
}

func TestReadSnapshot_OtherErrorBubbles(t *testing.T) {
	r := &stubRunner{outputErr: errors.New("sandbox not running")}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	_, err := ReadSnapshot(sb)
	assert.Error(t, err)
}

func TestWriteSnapshot(t *testing.T) {
	r := &stubRunner{}
	sb := &sandbox.Sandbox{Name: "x", Runner: r}
	err := WriteSnapshot(sb, "rendered: yes\n")
	assert.NoError(t, err)
	cmd := strings.Join(r.lastArgs[0], " ")
	assert.Contains(t, cmd, "sbx exec x bash -c")
	assert.Contains(t, cmd, "applied.yaml.tmp")
	assert.Contains(t, cmd, "mv ")
	// Content is base64-encoded on the command line (no stdin pipe).
	assert.Contains(t, cmd, "base64 -d", "content must be passed base64-encoded inline")
	encoded := base64.StdEncoding.EncodeToString([]byte("rendered: yes\n"))
	assert.Contains(t, cmd, encoded, "encoded content must appear in argv")
	assert.Empty(t, r.runStdinSeen, "no stdin should be piped (avoids Go exec hang)")
}
