// Package agent provides access to the cross-compiled devm-agent binaries
// embedded into the host devm binary. The binaries are produced by
// `make embed-agent` (see Makefile) and live under internal/agent/bin/.
//
//go:generate make -C ../.. embed-agent
package agent

import (
	_ "embed"
	"fmt"
)

//go:embed bin/devm-agent-linux-amd64
var BinaryAMD64 []byte

//go:embed bin/devm-agent-linux-arm64
var BinaryARM64 []byte

// PickForVM returns the embedded agent binary appropriate for the given
// GOARCH value. The caller should pass runtime.GOARCH — sbx VMs share the
// host's architecture on OrbStack/Lima setups, so the in-VM agent matches.
func PickForVM(arch string) ([]byte, error) {
	switch arch {
	case "amd64":
		return BinaryAMD64, nil
	case "arm64":
		return BinaryARM64, nil
	default:
		return nil, fmt.Errorf("no embedded devm-agent binary for arch %q", arch)
	}
}
