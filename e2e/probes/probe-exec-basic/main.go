// probe-exec-basic: the lowest-level Go ↔ sbx interop probe.
//
// Exercises a single primitive — `exec.Command` to sbx — and pins
// that Go can spawn sbx, capture its stdout, and parse the JSON
// shape sbx promises. Every devm-side sbx call ultimately sits on
// top of this primitive.
//
// What this catches if it goes red: the Go-↔-sbx layer broke at the
// syscall, capture, or encoding level — independent of any
// sandbox state, PTY/anchor concerns, or higher-level orchestration.
//
// Output (stdout): "OK\tsandboxes=N\tfirst_keys=[...]" on success.
// Exit codes:
//   0 — exec succeeded and JSON parsed
//   2 — exec.Command failed (sbx couldn't be spawned)
//   3 — exec succeeded but JSON parse failed
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
)

type lsResponse struct {
	Sandboxes []map[string]any `json:"sandboxes"`
}

func main() {
	out, err := exec.Command("sbx", "ls", "--json").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "EXEC_ERROR: %v\n", err)
		os.Exit(2)
	}
	var resp lsResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "PARSE_ERROR: %v\nGOT: %q\n", err, string(out))
		os.Exit(3)
	}
	fmt.Printf("OK\tsandboxes=%d\tfirst_keys=%v\n",
		len(resp.Sandboxes), firstKeys(resp.Sandboxes))
}

// firstKeys returns the sorted keys of the first sandbox entry, or
// nil if there are no entries. Sorted for deterministic output —
// the test asserts on the shape, so determinism matters.
func firstKeys(sandboxes []map[string]any) []string {
	if len(sandboxes) == 0 {
		return nil
	}
	keys := make([]string, 0, len(sandboxes[0]))
	for k := range sandboxes[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
