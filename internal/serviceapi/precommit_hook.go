// precommit_hook.go installs devm's pre-commit hook into a guest repo's
// shared git hooks dir. The hook refuses `git commit devm.yaml` and
// steers the user toward `propose`.
package serviceapi

import (
	"fmt"
	"path/filepath"
	"strings"
)

// preCommitHookMarker is the identity token that lives on line 2 of
// every devm-installed hook. Used to distinguish a managed hook (safe
// to overwrite) from a foreign one (leave alone).
const preCommitHookMarker = "# devm-managed pre-commit"

// preCommitHookBody is written verbatim to <sharedGitDir>/hooks/pre-commit.
const preCommitHookBody = `#!/bin/sh
` + preCommitHookMarker + `
# Guides devm.yaml edits through 'propose'. Delete or replace this file
# to disable.
if git diff --cached --name-only 2>/dev/null | grep -qxF -e devm.yaml -e devm.me.yaml; then
    cat >&2 <<'EOF'
devm: refusing to commit devm.yaml directly.

Send this edit to the Mac side with 'propose' -- it will appear in
the devm menu bar for review + approve. That is the intended path
for guest-side edits.

To force this commit anyway, pass --no-verify.
EOF
    exit 1
fi
`

// InstallPreCommitHook installs the devm-managed pre-commit hook into
// the repo at <guestHomeDir>/<workspaceLabel>'s shared git dir. If a
// foreign hook exists (no devm marker on line 2), it is left alone. If
// a managed hook exists, it is overwritten so updates ship. Install is
// atomic: the body is written to a .tmp file, chmod'd, then renamed
// over the final path.
func InstallPreCommitHook(exec GuestExec, workspaceLabel string) error {
	workDir := filepath.Join(guestHomeDir, workspaceLabel)

	stdout, stderr, exitCode, err := exec(fmt.Sprintf("git -C %s rev-parse --git-common-dir", PosixShellQuote(workDir)))
	if err != nil {
		return fmt.Errorf("precommit hook: git-common-dir: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("precommit hook: git-common-dir: exit %d: %s", exitCode, strings.TrimSpace(stderr))
	}
	sharedGitDir := strings.TrimSpace(stdout)
	if !strings.HasPrefix(sharedGitDir, "/") {
		sharedGitDir = filepath.Join(workDir, sharedGitDir)
	}
	hookPath := filepath.Join(sharedGitDir, "hooks", "pre-commit")

	_, _, existsCode, err := exec(fmt.Sprintf("test -e %s", PosixShellQuote(hookPath)))
	if err != nil {
		return fmt.Errorf("precommit hook: check existing hook: %w", err)
	}
	if existsCode == 0 {
		headOut, headErr, headExit, err := exec(fmt.Sprintf("head -n 2 %s", PosixShellQuote(hookPath)))
		if err != nil {
			return fmt.Errorf("precommit hook: read existing hook: %w", err)
		}
		if headExit != 0 {
			return fmt.Errorf("precommit hook: read existing hook: exit %d: %s", headExit, strings.TrimSpace(headErr))
		}
		lines := strings.SplitN(headOut, "\n", 3)
		if len(lines) < 2 || strings.TrimSpace(lines[1]) != preCommitHookMarker {
			// Foreign hook — leave alone.
			return nil
		}
	}

	tmpPath := hookPath + ".tmp"
	installScript := fmt.Sprintf("cat > %s <<'DEVM_PRECOMMIT_HOOK_EOF'\n%s\nDEVM_PRECOMMIT_HOOK_EOF\nchmod 0755 %s\nmv %s %s\n",
		PosixShellQuote(tmpPath), preCommitHookBody, PosixShellQuote(tmpPath), PosixShellQuote(tmpPath), PosixShellQuote(hookPath))
	_, installStderr, installExit, err := exec(installScript)
	if err != nil {
		return fmt.Errorf("precommit hook: install: %w", err)
	}
	if installExit != 0 {
		return fmt.Errorf("precommit hook: install: exit %d: %s", installExit, strings.TrimSpace(installStderr))
	}
	return nil
}
