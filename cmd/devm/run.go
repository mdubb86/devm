package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/scriptfile"

	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run <fn> [args...]",
	Short: "Run a devm.sh function on the Mac host",
	Long:  "Run a function from the project's devm.sh / devm.me.sh on the Mac host, with $DEVM_SIDE=host set so scripts can branch on the side they are running on. CLI-direct: no daemon required.",
	Args:  cobra.MinimumNArgs(1),
	// DisableFlagParsing passes dash-prefixed args straight through to
	// the user's function (devm run build --debug). Cobra's own flag
	// parser would otherwise claim them. We special-case -h/--help below
	// to keep the standard help affordance working.
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		if args[0] == "-h" || args[0] == "--help" {
			return cmd.Help()
		}
		fn, fnArgs := args[0], args[1:]

		resolved, err := discoverProjectFn()
		if err != nil {
			return err
		}
		cfg, err := config.Load(resolved.MacCwd)
		if err != nil {
			return err
		}
		devmShExists := anyScriptFileExists(resolved.MacCwd)
		if err := resolveRunFunction(cfg.Functions, fn, resolved.MacCwd, devmShExists); err != nil {
			return err
		}

		body, err := scriptfile.RenderWrapper(scriptfile.WrapperInput{
			FunctionName: fn,
			Env:          buildHostEnv(cfg, resolved.MacCwd),
			PathPrepend:  cfg.Path,
			Cwd:          resolved.MacCwd,
			DevmShPath:   filepath.Join(resolved.MacCwd, "devm.sh"),
			DevmMeShPath: filepath.Join(resolved.MacCwd, "devm.me.sh"),
		})
		if err != nil {
			return err
		}

		return runBash(cmd.Context(), body, fnArgs)
	},
}

// anyScriptFileExists reports whether devm.sh or devm.me.sh is present
// in dir. LoadFunctions silently skips missing files, so an empty
// function set alone can't distinguish "file absent" from "file present
// but defines no top-level functions" — this probe separates them for
// the error message.
func anyScriptFileExists(dir string) bool {
	for _, name := range []string{"devm.sh", "devm.me.sh"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// resolveRunFunction validates that fn is a function defined somewhere
// in the project's devm.sh / devm.me.sh. available is the merged
// function set; devmShExists distinguishes "neither file is on disk"
// from "file exists but declared zero top-level functions".
func resolveRunFunction(available []string, fn, projectRoot string, devmShExists bool) error {
	if !devmShExists {
		return fmt.Errorf("no devm.sh in %s — nothing to run", projectRoot)
	}
	if len(available) == 0 {
		return fmt.Errorf("devm.sh in %s defines no functions", projectRoot)
	}
	for _, a := range available {
		if a == fn {
			return nil
		}
	}
	sorted := append([]string(nil), available...)
	sort.Strings(sorted)
	return fmt.Errorf("function %q not found; available: %s",
		fn, strings.Join(sorted, ", "))
}

// buildHostEnv returns the env map to pass to scriptfile.RenderWrapper
// for a host-side run. DEVM_SIDE=host and WORKSPACE=projectRoot are
// injected; secret-marked env entries are dropped per the spec (scripts
// don't see secret values on either side).
func buildHostEnv(cfg schema.Config, projectRoot string) map[string]string {
	env := make(map[string]string, len(cfg.Env)+2)
	for k, v := range cfg.Env {
		if v.IsSecret() {
			continue
		}
		env[k] = v.Literal
	}
	env["WORKSPACE"] = projectRoot
	env["DEVM_SIDE"] = "host"
	return env
}

// buildBashArgs produces the argv for `bash -s`. The `--` separator
// ends bash's own option processing, so a leading `-x`/`--flag` in
// fnArgs flows into the script as a positional argument rather than
// being parsed as a bash option.
func buildBashArgs(fnArgs []string) []string {
	return append([]string{"-s", "--"}, fnArgs...)
}

// exitCodeFromExitError returns the shell-style exit code for a child
// process that exited: 128+N when killed by signal N, ExitCode()
// otherwise. os.ProcessState.ExitCode() returns -1 for signal death,
// which os.Exit wraps to 255 on Unix — this function unwraps it so
// SIGINT surfaces as 130, SIGPIPE as 141, etc.
func exitCodeFromExitError(ee *exec.ExitError) int {
	if ws, ok := ee.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ee.ExitCode()
}

// runBash feeds body to bash -s, passing fnArgs as positional args so
// the function receives them as $1, $2, ..., and forwards SIGINT /
// SIGTERM to the child so Ctrl-C during a long-running function
// reaches it cleanly. Stdin / stdout / stderr are wired through.
// On a non-zero bash exit, calls os.Exit with the child's exit code
// directly — cobra's default error path would otherwise flatten any
// code to 1 and print "exit status N" to stderr.
func runBash(_ context.Context, body string, fnArgs []string) error {
	bash := exec.Command("bash", buildBashArgs(fnArgs)...)
	bash.Stdin = strings.NewReader(body)
	bash.Stdout = os.Stdout
	bash.Stderr = os.Stderr
	// Put bash + any `sleep`-style grandchildren into their own process
	// group so a single kill reaches the whole tree on SIGINT; bash
	// alone signaled with SIGINT does not reliably forward to the
	// grandchild waiting in sleep(3), especially when bash was invoked
	// non-interactively via `-s`.
	bash.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := bash.Start(); err != nil {
		return fmt.Errorf("start bash: %w", err)
	}
	pgid := bash.Process.Pid // same as pgid because Setpgid above

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				// Negative pid targets the process group.
				_ = syscall.Kill(-pgid, s.(syscall.Signal))
			case <-done:
				return
			}
		}
	}()

	waitErr := bash.Wait()
	close(done)
	if waitErr == nil {
		return nil
	}
	// Preserve bash's exit code (including signal-death codes). cobra's
	// default error handler would map any returned error to exit 1; we
	// bypass it with os.Exit so shell chains like
	// `devm run build && devm run deploy` short-circuit correctly.
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		os.Exit(exitCodeFromExitError(ee))
	}
	return fmt.Errorf("bash: %w", waitErr)
}

func init() {
	rootCmd.AddCommand(runCmd)
}
