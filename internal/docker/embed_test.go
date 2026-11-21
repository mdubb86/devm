package docker

import "testing"

func TestShim_NonEmpty(t *testing.T) {
	b := Shim()
	if len(b) < 100_000 {
		t.Errorf("shim binary suspiciously small (%d bytes) — build wiring likely broken", len(b))
	}
	// ELF magic — every linux binary starts with 0x7f ELF.
	if len(b) < 4 || b[0] != 0x7f || b[1] != 'E' || b[2] != 'L' || b[3] != 'F' {
		t.Errorf("shim binary is not an ELF file (first 4 bytes: %x)", b[:4])
	}
}
