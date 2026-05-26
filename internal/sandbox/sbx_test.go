package sandbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type mockRunner struct {
	output string
	err    error
}

func (m mockRunner) Output(name string, args ...string) ([]byte, error) {
	return []byte(m.output), m.err
}
func (m mockRunner) Run(name string, args ...string) error             { return nil }
func (m mockRunner) RunStdin(stdin, name string, args ...string) error { return nil }

func TestStateRunning(t *testing.T) {
	s := &Sandbox{
		Name:   "test-sbx",
		Runner: mockRunner{output: "NAME           KIT         STATUS\ntest-sbx       test        running    127.0.0.1:80\n"},
	}
	assert.Equal(t, "running", s.State())
}

func TestStateAbsent(t *testing.T) {
	s := &Sandbox{
		Name:   "test-sbx",
		Runner: mockRunner{output: "NAME    KIT     STATUS\n"},
	}
	assert.Empty(t, s.State())
}
