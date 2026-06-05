package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner is the minimal interface for shelling out — abstracted so tests
// can mock subprocess output.
type Runner interface {
	Output(name string, args ...string) ([]byte, error)
	Run(name string, args ...string) error
	RunStdin(stdin, name string, args ...string) error
}

// DefaultRunner runs commands via os/exec.
type DefaultRunner struct{}

func (DefaultRunner) Output(name string, args ...string) ([]byte, error) {
	out, err := exec.Command(name, args...).Output()
	// exec.Cmd.Output() returns *exec.ExitError whose Error() is just
	// "exit status N" — the real message is on stderr, captured in
	// ExitError.Stderr. Fold it into the returned error so callers
	// (and string-matching on specific failures) see the actual text.
	if ee, ok := err.(*exec.ExitError); ok {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			return out, fmt.Errorf("%w: %s", err, msg)
		}
	}
	return out, err
}

func (DefaultRunner) Run(name string, args ...string) error {
	// Match Output's stderr-folding: exec.Cmd.Run by default discards
	// stderr, so callers get a bare "exit status N" with no clue what
	// failed. Capture stderr explicitly and fold into the error.
	c := exec.Command(name, args...)
	var stderr strings.Builder
	c.Stderr = &stderr
	err := c.Run()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
}

func (DefaultRunner) RunStdin(stdin, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdin = strings.NewReader(stdin)
	var stderr strings.Builder
	c.Stderr = &stderr
	err := c.Run()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
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
