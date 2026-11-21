package softnet

import (
	"fmt"
	"io"
	"os"
)

// logOut is where logf writes; tests redirect it to capture output.
var logOut io.Writer = os.Stderr

// logf writes a "[softnet] "-prefixed, newline-terminated line to logOut.
// softnet runs as a tart subprocess, so its stderr is captured by the
// supervisor; this is the only logging story it has.
func logf(format string, args ...any) {
	fmt.Fprintf(logOut, "[softnet] "+format+"\n", args...)
}
