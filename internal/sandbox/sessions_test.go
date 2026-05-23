package sandbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// alwaysRunner returns canned bytes for every Output call. Used to test
// the parsing layer of Sessions without coupling to specific argv shape.
type alwaysRunner struct {
	out []byte
	err error
}

func (a *alwaysRunner) Output(name string, args ...string) ([]byte, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.out, nil
}

func TestSessionsParsesMultiplePts(t *testing.T) {
	probeOutput := "" +
		"27 bash pts/1 agent\n" +
		"47 bash pts/2 agent\n"
	r := &alwaysRunner{out: []byte(probeOutput)}
	sb := &Sandbox{Name: "test"}
	sessions, err := sb.SessionsWithRunner(r)
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	assert.Equal(t, Session{PID: 27, Comm: "bash", TTY: "pts/1", User: "agent"}, sessions[0])
	assert.Equal(t, Session{PID: 47, Comm: "bash", TTY: "pts/2", User: "agent"}, sessions[1])
}

func TestSessionsEmptyWhenNoPty(t *testing.T) {
	r := &alwaysRunner{out: []byte("")}
	sb := &Sandbox{Name: "test"}
	sessions, err := sb.SessionsWithRunner(r)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestSessionsIgnoresMalformedLines(t *testing.T) {
	probeOutput := "" +
		"27 bash pts/1 agent\n" +
		"oops malformed line\n" +
		"47 bash pts/2 agent\n"
	r := &alwaysRunner{out: []byte(probeOutput)}
	sb := &Sandbox{Name: "test"}
	sessions, err := sb.SessionsWithRunner(r)
	require.NoError(t, err)
	require.Len(t, sessions, 2)
}

func TestSessionsSkipsNonIntegerPID(t *testing.T) {
	probeOutput := "" +
		"abc bash pts/1 agent\n" +
		"47 bash pts/2 agent\n"
	r := &alwaysRunner{out: []byte(probeOutput)}
	sb := &Sandbox{Name: "test"}
	sessions, err := sb.SessionsWithRunner(r)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, 47, sessions[0].PID)
}

func TestSessionsBubblesRunnerError(t *testing.T) {
	r := &alwaysRunner{err: assertableError("boom")}
	sb := &Sandbox{Name: "test"}
	_, err := sb.SessionsWithRunner(r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

// assertableError lets us inject a deterministic error from the stub.
type assertableError string

func (e assertableError) Error() string { return string(e) }
