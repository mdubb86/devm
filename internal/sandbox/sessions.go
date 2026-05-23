package sandbox

import (
	"fmt"
	"strconv"
	"strings"
)

// Session represents one interactive pty session inside the sandbox.
// Sessions are discovered by walking /proc for processes whose
// controlling tty resolves to a /dev/pts/N device. System processes
// (PID 1 init, daemons launched without a tty) have tty `?` and are
// naturally excluded — no whitelist needed.
type Session struct {
	PID  int
	Comm string // process name from /proc/<pid>/comm
	TTY  string // e.g. "pts/1"
	User string // resolved username from /proc/<pid>/status Uid line
}

// probeScript walks /proc in the sandbox and prints one line per
// process whose fd 0 (stdin) is a /dev/pts/N device. Output format is
// space-separated: "<pid> <comm> <pts/N> <user>". The script is kept
// POSIX-y so it works under /bin/sh in any reasonable image.
const probeScript = `for d in /proc/[0-9]*; do
  pid="${d#/proc/}"
  [ -r "$d/comm" ] || continue
  comm=$(cat "$d/comm" 2>/dev/null) || continue
  # fd 0 is a symlink to the controlling device. Pty sessions point at /dev/pts/N.
  tty=$(readlink "$d/fd/0" 2>/dev/null) || continue
  case "$tty" in
    /dev/pts/*) ;;
    *) continue ;;
  esac
  ttyshort="${tty#/dev/}"
  uid=$(awk '/^Uid:/ {print $2}' "$d/status" 2>/dev/null)
  user=$(awk -F: -v u="$uid" '$3==u {print $1; exit}' /etc/passwd 2>/dev/null)
  [ -z "$user" ] && user="uid=$uid"
  printf "%s %s %s %s\n" "$pid" "$comm" "$ttyshort" "$user"
done
`

// Sessions returns the active interactive pty sessions in the sandbox.
// Convenience wrapper around SessionsWithRunner using the sandbox's Runner.
func (s *Sandbox) Sessions() ([]Session, error) {
	return s.SessionsWithRunner(s.Runner)
}

// SessionsWithRunner is the testable inner. The runner's Output is
// invoked with `sbx exec <name> sh -c <probeScript>`.
func (s *Sandbox) SessionsWithRunner(r Runner) ([]Session, error) {
	out, err := r.Output("sbx", "exec", s.Name, "sh", "-c", probeScript)
	if err != nil {
		return nil, fmt.Errorf("sessions: sbx exec: %w", err)
	}
	return parseSessions(string(out)), nil
}

// parseSessions extracts Session records from the probe output.
// Malformed lines are silently skipped — the script can race processes
// that vanish mid-walk, producing partial lines.
func parseSessions(out string) []Session {
	var sessions []Session
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		sessions = append(sessions, Session{
			PID:  pid,
			Comm: fields[1],
			TTY:  fields[2],
			User: fields[3],
		})
	}
	return sessions
}
