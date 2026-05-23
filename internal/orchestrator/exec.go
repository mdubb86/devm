package orchestrator

import (
	"io"
	"os"
	"os/exec"
)

// Spawner abstracts subprocess control so tests can drive the
// orchestrator without invoking real processes. Production code uses
// ExecSpawner; tests use a stub.
type Spawner interface {
	Start(name string, args ...string) (SpawnedCmd, error)
}

// SpawnedCmd is the subset of *exec.Cmd the orchestrator needs.
type SpawnedCmd interface {
	Wait() error
	Kill() error
	Pid() int
}

// ExecSpawner is the production Spawner. If Interactive is true, the
// spawned process inherits the host terminal's stdin/stdout/stderr;
// otherwise stdin is /dev/null and stdout/stderr are routed to
// LogWriter (or discarded if LogWriter is nil).
type ExecSpawner struct {
	Interactive bool
	LogWriter   io.Writer
}

func (s *ExecSpawner) Start(name string, args ...string) (SpawnedCmd, error) {
	c := exec.Command(name, args...)
	if s.Interactive {
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
	} else {
		c.Stdin = nil
		if s.LogWriter != nil {
			c.Stdout = s.LogWriter
			c.Stderr = s.LogWriter
		}
	}
	if err := c.Start(); err != nil {
		return nil, err
	}
	return &execCmd{c: c}, nil
}

type execCmd struct{ c *exec.Cmd }

func (e *execCmd) Wait() error { return e.c.Wait() }
func (e *execCmd) Kill() error {
	if e.c.Process == nil {
		return nil
	}
	return e.c.Process.Kill()
}
func (e *execCmd) Pid() int {
	if e.c.Process == nil {
		return 0
	}
	return e.c.Process.Pid
}
