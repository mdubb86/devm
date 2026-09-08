package daemonlog

import (
	"fmt"
	"os"
)

// Warnf logs an expected-but-noteworthy event at warn severity: a
// drift the watchdog observed, an out-of-band mutation the daemon
// noticed. Same stderr routing as Errorf but WITHOUT the goroutine
// stack trace — warnings are not failures.
func Warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[WARN] "+format+"\n", args...)
}
