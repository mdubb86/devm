package orchestrator

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"strings"
	"time"
)

// forwardEnv is the set of host env vars that ride into every
// interactive `tart exec` so TUIs inside the guest see the real
// terminal capabilities: TERM/COLORTERM drive color and keybinding
// resolution; LANG/LC_ALL/LC_CTYPE drive locale-dependent sort order,
// date formatting, and character boundaries. Without forwarding, the
// guest defaults to xterm+C — colors off, non-ASCII broken.
//
// The base image generates en_US.UTF-8 (see image/provision-base.sh)
// so LANG/LC_* forwarding lands on a real locale instead of triggering
// "cannot change locale" warnings on every shell invocation.
var forwardEnv = []string{"TERM", "COLORTERM", "LANG", "LC_ALL", "LC_CTYPE"}

// terminalEnvForward returns the argv prefix `env KEY=VAL KEY=VAL …`
// that sets host terminal env inside the guest before the guest
// wrapper runs. Chains through env(1) because `tart exec` has no
// --env flag of its own. Includes DEVM_TERMINFO_BLOB when the host
// terminfo entry is exportable — the guest's with-devm-env wrapper
// decodes and installs it via `tic` when the entry is missing from
// the sandbox's terminfo db.
//
// Returns nil when there's nothing to forward — caller then invokes
// the wrapper directly without an env(1) prefix.
func terminalEnvForward() []string {
	args := []string{"env"}
	for _, k := range forwardEnv {
		if v := os.Getenv(k); v != "" {
			args = append(args, k+"="+v)
		}
	}
	if blob := captureHostTerminfo(); blob != "" {
		args = append(args, "DEVM_TERMINFO_BLOB="+blob)
	}
	if len(args) == 1 { // just "env", nothing to set
		return nil
	}
	return args
}

// captureHostTerminfo runs `infocmp -x "$TERM"` on the host and returns
// the base64-encoded terminfo source so it can ride along as an env var
// on `tart exec`. The VM-side with-devm-env wrapper decodes it and
// pipes to `tic` if the terminfo entry is missing from the VM's db.
//
// Returns "" on any failure (no $TERM, no host infocmp, unknown entry,
// empty output, timeout). The caller treats the empty case as
// "don't forward" — the sandbox falls back to whatever ncurses-term
// already shipped.
//
// Timeout: 1 second. infocmp is local; if it's slower than that
// something is broken and we'd rather skip than block the shell.
func captureHostTerminfo() string {
	term := os.Getenv("TERM")
	if term == "" {
		return ""
	}
	if _, err := exec.LookPath("infocmp"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "infocmp", "-x", term).Output()
	if err != nil {
		return ""
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(out)
}
