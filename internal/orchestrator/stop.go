package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mtwaage/devm/internal/lock"
	"github.com/mtwaage/devm/internal/sandbox"
)

// Destructiveness selects between preserving the VM (sbx stop) and
// destroying it (sbx rm).
type Destructiveness int

const (
	// StopPreserve runs `sbx stop`: brings the sandbox to stopped
	// state, but keeps VM filesystem + installed packages.
	StopPreserve Destructiveness = iota
	// StopDestroy runs `sbx rm`: removes the sandbox entirely
	// including its VM.
	StopDestroy
)

// StopDeps wires collaborators for RunStop. In and Out drive the
// confirmation prompt; tests inject strings.NewReader / bytes.Buffer.
// When In is nil, os.Stdin is used; when Out is nil, os.Stderr.
type StopDeps struct {
	Runner   sandbox.Runner
	LockPath string
	In       io.Reader
	Out      io.Writer
}

// RunStop implements both `devm stop` (mode=StopPreserve) and
// `devm teardown` (mode=StopDestroy). autoApprove skips the
// interactive prompt. Return code: 0 on success or already-stopped
// no-op; 1 on user refusal.
func RunStop(ctx context.Context, d StopDeps, sandboxName string, mode Destructiveness, autoApprove bool) (int, error) {
	if d.In == nil {
		d.In = os.Stdin
	}
	if d.Out == nil {
		d.Out = os.Stderr
	}

	lk, err := lock.Acquire(d.LockPath)
	if err != nil {
		return -1, fmt.Errorf("acquire lock: %w", err)
	}
	defer lk.Release()

	sb := &sandbox.Sandbox{Name: sandboxName, Runner: d.Runner}

	running := sb.IsRunning()
	if !running && mode == StopPreserve {
		fmt.Fprintln(d.Out, "sandbox is already stopped")
		return 0, nil
	}

	var sessions []sandbox.Session
	if running {
		sessions, err = sb.Sessions()
		if err != nil {
			return -1, fmt.Errorf("discover sessions: %w", err)
		}
	}

	if !autoApprove {
		approved, err := promptConfirm(d.In, d.Out, sandboxName, mode, sessions)
		if err != nil {
			return -1, err
		}
		if !approved {
			fmt.Fprintln(d.Out, "aborted")
			return 1, nil
		}
	}

	verb := "stop"
	if mode == StopDestroy {
		verb = "rm"
	}
	if _, err := d.Runner.Output("sbx", verb, sandboxName); err != nil {
		return -1, fmt.Errorf("sbx %s: %w", verb, err)
	}
	return 0, nil
}

// promptConfirm prints the session list (if any) and asks for [y/N].
// Returns true on "y"/"yes" (case-insensitive); false otherwise.
func promptConfirm(in io.Reader, out io.Writer, name string, mode Destructiveness, sessions []sandbox.Session) (bool, error) {
	if len(sessions) > 0 {
		fmt.Fprintf(out, "Active sessions in %s:\n", name)
		for _, s := range sessions {
			fmt.Fprintf(out, "  %s: %s (PID %d, owner %s)\n", s.TTY, s.Comm, s.PID, s.User)
		}
		fmt.Fprintln(out)
	}
	action := "Stop"
	if mode == StopDestroy {
		action = "Tear down"
	}
	if len(sessions) > 0 {
		fmt.Fprintf(out, "%s sandbox %s? This will hang up %d session(s). [y/N]: ", action, name, len(sessions))
	} else {
		fmt.Fprintf(out, "%s sandbox %s? [y/N]: ", action, name)
	}
	br := bufio.NewReader(in)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	resp := strings.ToLower(strings.TrimSpace(line))
	return resp == "y" || resp == "yes", nil
}
