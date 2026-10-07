package softnet

import (
	"regexp"
	"testing"
)

func TestContractSHA_IsLowercaseHex64(t *testing.T) {
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(ContractSHA) {
		t.Fatalf("ContractSHA must be 64 lowercase hex chars, got %q", ContractSHA)
	}
}

func TestComputeContractSHA_ChangesWhenBytesChange(t *testing.T) {
	a := computeContractSHA([]byte("package softnet\n"))
	b := computeContractSHA([]byte("package softnet\n// cosmetic edit\n"))
	if a == b {
		t.Fatalf("hash must change when input bytes change: a=%q b=%q", a, b)
	}
	if len(a) != 64 || len(b) != 64 {
		t.Fatalf("hashes must be 64 chars: len(a)=%d len(b)=%d", len(a), len(b))
	}
}
