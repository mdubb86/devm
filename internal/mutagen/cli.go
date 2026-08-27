package mutagen

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ExecFn runs bin with args and env, returning captured stdout/stderr,
// the process exit code, and any error launching or waiting on it. A
// non-zero exitCode is not itself an error (mirrors os/exec's
// ExitError split); err is reserved for failures to start the
// process at all.
type ExecFn func(bin string, args []string, env []string) (stdout, stderr string, exitCode int, err error)

// CLI wraps the mutagen binary's sync/daemon subcommands via os/exec
// (or an injected ExecFn for tests).
type CLI struct {
	Binary   string
	DataDir  string   // sets MUTAGEN_DATA_DIRECTORY for every invocation
	ExtraEnv []string // additional key=value pairs appended to the exec env
	Exec     ExecFn
}

// SyncSession is one row from `mutagen sync list`.
type SyncSession struct {
	ID     string
	Name   string
	Status string
}

// OSExec is the default ExecFn: a real os/exec invocation.
func OSExec(bin string, args []string, env []string) (stdout, stderr string, exitCode int, err error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			err = runErr
		}
	}
	return outBuf.String(), errBuf.String(), exitCode, err
}

func (c *CLI) execFn() ExecFn {
	if c.Exec != nil {
		return c.Exec
	}
	return OSExec
}

func (c *CLI) env() []string {
	env := os.Environ()
	if c.DataDir != "" {
		env = append(env, "MUTAGEN_DATA_DIRECTORY="+c.DataDir)
	}
	return append(env, c.ExtraEnv...)
}

// run invokes the mutagen binary with args and returns stdout, or an
// error describing a launch failure or non-zero exit.
func (c *CLI) run(args ...string) (string, error) {
	stdout, stderr, code, err := c.execFn()(c.Binary, args, c.env())
	if err != nil {
		return "", fmt.Errorf("mutagen %s: %w", strings.Join(args, " "), err)
	}
	if code != 0 {
		return "", fmt.Errorf("mutagen %s: exit %d: %s", strings.Join(args, " "), code, strings.TrimSpace(stderr))
	}
	return stdout, nil
}

// SyncCreate creates a new sync session named name between alpha and
// beta, loading configFile for the session's ignore/mode defaults,
// and returns the session identifier mutagen assigns.
//
// Deviation from the original brief: mutagen v0.18.1's `sync create`
// has no --ssh-flag (or any other per-invocation SSH flag — verified
// against the real --help output, which lists no ssh-related flags
// at all). mutagen shells out to the system ssh client for remote
// endpoints, so identity file / host-key-checking behavior is
// controlled entirely by ~/.ssh/config for the target host, which the
// caller must have prepared before calling SyncCreate. sshFlags is
// still accepted and passed through verbatim as extra argv tokens
// ahead of alpha/beta, so a caller can forward any *real* mutagen
// flag (e.g. --watch-mode-beta=force-poll) without this wrapper
// needing to know about it.
func (c *CLI) SyncCreate(name, alpha, beta, configFile string, sshFlags []string) (string, error) {
	args := []string{"sync", "create", "--name", name, "--configuration-file", configFile}
	args = append(args, sshFlags...)
	args = append(args, alpha, beta)
	stdout, err := c.run(args...)
	if err != nil {
		return "", err
	}
	return parseCreateID(stdout)
}

// parseCreateID extracts the session identifier from `sync create`
// output. mutagen prints a \r-updated "Creating session..." progress
// line followed by "Created session <id>" — split on both \r and \n
// since the progress line has no trailing newline of its own.
func parseCreateID(stdout string) (string, error) {
	normalized := strings.ReplaceAll(stdout, "\r", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Created session") {
			continue
		}
		fields := strings.Fields(line)
		if id := fields[len(fields)-1]; id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("mutagen: could not parse session id from output: %q", stdout)
}

type syncSessionJSON struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
	Status     string `json:"status"`
}

// SyncList returns sessions from `mutagen sync list`, filtered to
// those whose name starts with namePrefix (all sessions if empty).
func (c *CLI) SyncList(namePrefix string) ([]SyncSession, error) {
	stdout, err := c.run("sync", "list", "--template", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var rows []syncSessionJSON
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		return nil, fmt.Errorf("mutagen sync list: parse json: %w", err)
	}
	sessions := make([]SyncSession, 0, len(rows))
	for _, r := range rows {
		if namePrefix != "" && !strings.HasPrefix(r.Name, namePrefix) {
			continue
		}
		sessions = append(sessions, SyncSession{ID: r.Identifier, Name: r.Name, Status: r.Status})
	}
	return sessions, nil
}

// SyncFlush forces an immediate synchronization cycle for id.
func (c *CLI) SyncFlush(id string) error {
	_, err := c.run("sync", "flush", id)
	return err
}

// SyncPause pauses session id.
func (c *CLI) SyncPause(id string) error {
	_, err := c.run("sync", "pause", id)
	return err
}

// SyncResume resumes a paused or disconnected session id.
func (c *CLI) SyncResume(id string) error {
	_, err := c.run("sync", "resume", id)
	return err
}

// SyncTerminate permanently terminates session id.
func (c *CLI) SyncTerminate(id string) error {
	_, err := c.run("sync", "terminate", id)
	return err
}

// DaemonStart starts the mutagen daemon (a no-op if already running)
// and returns its PID.
//
// Deviation from the original brief: mutagen v0.18.1 has no `daemon
// list` subcommand at all (confirmed: `mutagen daemon --help` lists
// only start/stop/register/unregister), so there is no CLI-native way
// to surface the daemon's PID. DaemonStart instead resolves it via
// `lsof -t` against the daemon.lock file mutagen holds open under
// DataDir/daemon/daemon.lock — the same file mutagen itself uses for
// single-instance locking, so whichever PID holds it open unlocked is
// the running daemon for this DataDir.
func (c *CLI) DaemonStart() (int, error) {
	if c.DataDir == "" {
		return 0, fmt.Errorf("mutagen: DaemonStart requires DataDir to locate the daemon lock file")
	}
	if _, err := c.run("daemon", "start"); err != nil {
		return 0, err
	}
	return c.daemonPID()
}

// DaemonStop stops the mutagen daemon if running.
func (c *CLI) DaemonStop() error {
	_, err := c.run("daemon", "stop")
	return err
}

func (c *CLI) daemonPID() (int, error) {
	lockPath := filepath.Join(c.DataDir, "daemon", "daemon.lock")
	stdout, stderr, code, err := c.execFn()("lsof", []string{"-t", lockPath}, c.env())
	if err != nil {
		return 0, fmt.Errorf("mutagen: lsof %s: %w", lockPath, err)
	}
	if code != 0 {
		return 0, fmt.Errorf("mutagen: lsof %s: exit %d: %s", lockPath, code, strings.TrimSpace(stderr))
	}
	fields := strings.Fields(stdout)
	if len(fields) == 0 {
		return 0, fmt.Errorf("mutagen: lsof %s: no holder found", lockPath)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("mutagen: lsof %s: parse pid %q: %w", lockPath, fields[0], err)
	}
	return pid, nil
}
