package sandbox

import (
	"os/exec"
	"strings"
)

// Runner is the minimal interface for shelling out — abstracted so tests
// can mock subprocess output.
type Runner interface {
	Output(name string, args ...string) ([]byte, error)
}

// DefaultRunner runs commands via os/exec.
type DefaultRunner struct{}

func (DefaultRunner) Output(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// Sandbox is a thin wrapper around the `sbx` CLI scoped to one sandbox name.
type Sandbox struct {
	Name   string
	Runner Runner
}

// State returns the STATUS column from `sbx ls` for our sandbox, or "" if absent.
func (s *Sandbox) State() string {
	out, err := s.Runner.Output("sbx", "ls")
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if i == 0 {
			continue // header
		}
		parts := strings.Fields(line)
		if len(parts) > 0 && parts[0] == s.Name {
			if len(parts) > 2 {
				return parts[2]
			}
			return ""
		}
	}
	return ""
}

func (s *Sandbox) Exists() bool    { return s.State() != "" }
func (s *Sandbox) IsRunning() bool { return s.State() == "running" }
