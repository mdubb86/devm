package sandbox

import (
	"os"
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

// Create creates a new sandbox from kitDir for repoRoot. Caller has already
// rendered .devm/spec.yaml etc.
func (s *Sandbox) Create(kitDir, repoRoot string) error {
	cmd := exec.Command("sbx", "create", "--quiet",
		"--kit", kitDir,
		s.Name,
		repoRoot,
		"--name", s.Name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (s *Sandbox) Stop() error {
	return exec.Command("sbx", "stop", s.Name).Run()
}

func (s *Sandbox) Remove() error {
	return exec.Command("sbx", "rm", s.Name).Run()
}
