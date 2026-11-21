package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
)

func TestSecretHashesFromBindings(t *testing.T) {
	bindings := []serviceapi.SecretBinding{
		{Name: "A", Value: "value-a"},
		{Name: "B", Value: "value-b"},
	}
	got := SecretHashesFromBindings(bindings)

	sumA := sha256.Sum256([]byte("value-a"))
	sumB := sha256.Sum256([]byte("value-b"))
	assert.Equal(t, hex.EncodeToString(sumA[:]), got["A"])
	assert.Equal(t, hex.EncodeToString(sumB[:]), got["B"])
	assert.Len(t, got, 2)
}

func TestSecretHashesFromBindings_Empty(t *testing.T) {
	assert.Nil(t, SecretHashesFromBindings(nil))
	assert.Nil(t, SecretHashesFromBindings([]serviceapi.SecretBinding{}))
}
