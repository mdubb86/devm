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
}

func TestComputeContractSHA_EmptyInputMatchesSHA256(t *testing.T) {
	const expected = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got := computeContractSHA(nil)
	if got != expected {
		t.Fatalf("computeContractSHA(nil) = %q, want SHA-256 empty-string hash %q", got, expected)
	}
}
