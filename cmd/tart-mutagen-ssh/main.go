// Package main implements tart-mutagen-ssh: a shim invoked by the mutagen
// daemon in place of the system ssh binary (via MUTAGEN_SSH_PATH). It
// translates mutagen's ssh-CLI argv into `tart exec <vm> <cmd...>` —
// running mutagen-agent inside the guest via Tart's gRPC-over-vsock
// control channel instead of over sshd. `tart exec` already lands as the
// devm user (devm's provisioner renames Tart's default admin user to
// devm), so no sudo or shell wrap is needed. Tart Guest Agent doesn't
// chdir to that user's HOME, so this shim rewrites mutagen's HOME-relative
// agent path to an absolute one.
//
// See docs/superpowers/specs/2026-08-29-mutagen-tart-transport.md for the
// design.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// logf appends one line to the shim's own log file at /tmp/tart-mutagen-ssh.log
// AND writes it to stderr. The file is defense against mutagen's inconsistent
// stderr-capture (its ClientVersionHandshake failure path drops stderr entirely,
// so stderr-only logging is invisible for some failure modes).
//
// The file is best-effort: append failure is logged to stderr but doesn't fail
// the shim invocation. Rotation is the user's problem (log is small — a few
// lines per session).
func logf(format string, args ...any) {
	line := fmt.Sprintf("tart-mutagen-ssh: "+format, args...)
	fmt.Fprintln(os.Stderr, line)
	if f, err := os.OpenFile("/tmp/tart-mutagen-ssh.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		defer f.Close()
		fmt.Fprintf(f, "%s %d %s\n", time.Now().Format(time.RFC3339), os.Getpid(), line)
	}
}

func main() {
	switch dispatchName(os.Args[0]) {
	case "scp":
		logf("scp invocation not supported — mutagen-agent is pre-installed in the guest, no SCP transfer should occur; this indicates a regression")
		os.Exit(2)
	}

	vm, cmd, err := parseSSHArgs(os.Args[1:])
	if err != nil {
		logf("%v", err)
		os.Exit(2)
	}

	// mutagen sends its command as one ssh argv element (a space-joined
	// command string, e.g. ".mutagen/agents/0.18.1/mutagen-agent synchronizer
	// --log-level=info"). sshd normally hands this to $SHELL -c which
	// tokenizes. tart exec doesn't wrap in a shell, and adding one
	// (sh -c "exec ...") introduced stdio buffering issues that broke
	// mutagen's handshake. Do the tokenization ourselves in Go: mutagen's
	// commands never contain shell metacharacters (verified: only paths,
	// mode keywords, and --flag=value pairs), so strings.Fields is safe.
	//
	// If mutagen ever DOES send a metacharacter, log-and-error rather than
	// silently split wrong.
	raw := strings.Join(cmd, " ")
	if strings.ContainsAny(raw, "|&;<>()`$\"'\\") {
		logf("refusing to run command with shell metacharacters: %q", raw)
		os.Exit(1)
	}
	tokens := strings.Fields(raw)
	if len(tokens) == 0 {
		logf("no command tokens after tokenization: %q", raw)
		os.Exit(2)
	}

	// Mutagen assumes sshd's "cd HOME before executing remote command"
	// semantics and sends its agent path as `.mutagen/agents/<v>/mutagen-agent`
	// (relative to HOME). Tart Guest Agent doesn't chdir to the target user's
	// HOME by default (its Workdir is unset by the tart CLI), so the relative
	// path doesn't resolve. Rewrite to devm's absolute agent path.
	originalFirst := tokens[0]
	if strings.HasPrefix(tokens[0], ".mutagen/") {
		tokens[0] = "/home/devm/" + tokens[0]
	}

	// If we rewrote the agent path, log that so failures upstream can tell
	// whether the path we asked tart to run actually exists in-guest. tart
	// exec itself doesn't say "no such file" clearly in some paths.
	if strings.HasPrefix(tokens[0], "/home/devm/.mutagen/") {
		logf("agent path %s (rewritten from mutagen's HOME-relative %q)", tokens[0], originalFirst)
	}

	// Invoke tart directly with the tokenized command — no sh -c wrapper.
	// This keeps mutagen's stdio (stdin/stdout, its binary protobuf
	// handshake channel) piped straight from mutagen through this process
	// to tart exec's own child, with no intervening shell fork to disturb
	// buffering.
	// -i wires host's stdin to the remote command. Without it, tart exec
	// gives the remote command /dev/null on stdin — mutagen-agent reads
	// its handshake stream from stdin, so no -i means immediate EOF.
	tartArgs := append([]string{"exec", "-i", vm}, tokens...)
	logf("invoking tart %s", strings.Join(tartArgs, " "))
	c := exec.Command("tart", tartArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	// Tee the child's stderr to both mutagen (via os.Stderr) and our
	// persistent log file at /tmp/tart-mutagen-ssh.log. Mutagen's
	// ClientVersionHandshake failure path drops captured stderr entirely,
	// so this file is our only reliable view of agent-side output.
	if f, err := os.OpenFile("/tmp/tart-mutagen-ssh.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		defer f.Close()
		prefix := fmt.Sprintf("%s %d tart-child: ", time.Now().Format(time.RFC3339), os.Getpid())
		c.Stderr = io.MultiWriter(os.Stderr, &prefixWriter{w: f, prefix: []byte(prefix), atLine: true})
	} else {
		c.Stderr = os.Stderr
	}

	if err := c.Start(); err != nil {
		logf("exec tart: %v", err)
		os.Exit(1)
	}

	// Forward common termination signals so mutagen's session teardown
	// cleanly stops the child.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for s := range sigCh {
			if c.Process != nil {
				_ = c.Process.Signal(s)
			}
		}
	}()

	if err := c.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		logf("wait: %v", err)
		os.Exit(1)
	}
}

// dispatchName returns "scp" if the binary was invoked as scp (any
// symlink/copy named "scp"), else "ssh".
func dispatchName(argv0 string) string {
	if filepath.Base(argv0) == "scp" {
		return "scp"
	}
	return "ssh"
}

// parseSSHArgs walks ssh's argv skipping -o* flags, extracts the
// <user@host> positional (mapping to a Tart VM name by stripping the
// "devm-" prefix), and returns the trailing remote command.
//
// Mutagen v0.18.1 only ever emits -o flags in the single-arg form
// (-oKEY=VAL). If a future mutagen ever emits -o KEY VAL, this parser
// would need to advance i twice; that's a defect to fix loud rather than
// silently mishandle, so unknown -X <val> sequences aren't accommodated.
func parseSSHArgs(args []string) (string, []string, error) {
	var userHost string
	var cmd []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-o") {
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		userHost = a
		cmd = args[i+1:]
		break
	}
	if userHost == "" {
		return "", nil, fmt.Errorf("no host in argv: %v", args)
	}
	host := userHost
	if at := strings.LastIndex(userHost, "@"); at >= 0 {
		host = userHost[at+1:]
	}
	vm := strings.TrimPrefix(host, "devm-")
	if len(cmd) == 0 {
		return "", nil, fmt.Errorf("no command in argv: %v", args)
	}
	return vm, cmd, nil
}

// prefixWriter adds a fixed prefix to every line written to the underlying
// writer. Used to tag tart-child stderr in the shim log file so it's
// distinguishable from the shim's own logf lines.
type prefixWriter struct {
	w      io.Writer
	prefix []byte
	atLine bool // true when the next write starts a new line
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	for i, line := range bytes.SplitAfter(b, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if i == 0 && !p.atLine {
			p.w.Write(line)
		} else {
			p.w.Write(p.prefix)
			p.w.Write(line)
		}
	}
	p.atLine = len(b) > 0 && b[len(b)-1] == '\n'
	return len(b), nil
}
