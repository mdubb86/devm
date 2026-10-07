package softnet

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed contract.go
var contractSource []byte

// ContractSHA is the lowercase 64-char hex SHA-256 of contract.go. Both
// the daemon and softnet reference this identifier; when a running softnet
// subprocess reports a different value, the handshake surface has drifted
// and the subprocess should be restarted (devm stop && devm start) to
// pick up the current code.
//
// Cosmetic edits to contract.go (comment or formatting changes) also change
// this hash — accepted trade-off, since the file is touched rarely and the
// "fix" is a trivial VM restart.
var ContractSHA = computeContractSHA(contractSource)

func computeContractSHA(src []byte) string {
	sum := sha256.Sum256(src)
	return hex.EncodeToString(sum[:])
}
