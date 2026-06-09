package orchestrator

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"strings"
	"time"
)

// captureHostTerminfo runs `infocmp -x "$TERM"` on the host and returns
// the base64-encoded terminfo source so it can ride along on an
// `sbx exec -e DEVM_TERMINFO_BLOB=<blob>` flag. The sandbox-side
// with-devm-env wrapper decodes it and pipes to `tic` if the terminfo
// entry is missing from the sandbox's db.
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
