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
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	switch dispatchName(os.Args[0]) {
	case "scp":
		fmt.Fprintln(os.Stderr, "tart-mutagen-ssh: scp invocation not supported — mutagen-agent is pre-installed in the guest, no SCP transfer should occur; this indicates a regression")
		os.Exit(2)
	}

	vm, cmd, err := parseSSHArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "tart-mutagen-ssh: %v\n", err)
		os.Exit(2)
	}

	// Mutagen assumes sshd's "cd HOME before executing remote command"
	// semantics and sends its agent path as `.mutagen/agents/<v>/mutagen-agent`
	// (relative to HOME). Tart Guest Agent doesn't chdir to the target user's
	// HOME by default (its Workdir is unset by the tart CLI), so the relative
	// path doesn't resolve. Rewrite to devm's absolute agent path.
	if len(cmd) > 0 && strings.HasPrefix(cmd[0], ".mutagen/") {
		cmd[0] = "/home/devm/" + cmd[0]
	}

	tartArgs := append([]string{"exec", vm}, cmd...)
	c := exec.Command("tart", tartArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	if err := c.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "tart-mutagen-ssh: exec tart: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "tart-mutagen-ssh: wait: %v\n", err)
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
