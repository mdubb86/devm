package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/mdubb86/devm/internal/serviceapi"
)

// SecretHashesFromBindings returns a {Name: hex(sha256(Value))} map
// for the given resolved-from-keychain secret bindings. Feeds into
// the reconcile RPC as the CLI-side "current" set that the daemon's
// diff engine compares against its stored SecretHashes to detect
// keychain-value rotation.
//
// Empty / nil input yields nil so the map is trivially JSON-omitted.
func SecretHashesFromBindings(bindings []serviceapi.SecretBinding) map[string]string {
	if len(bindings) == 0 {
		return nil
	}
	out := make(map[string]string, len(bindings))
	for _, b := range bindings {
		sum := sha256.Sum256([]byte(b.Value))
		out[b.Name] = hex.EncodeToString(sum[:])
	}
	return out
}
