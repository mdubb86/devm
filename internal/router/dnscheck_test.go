package router

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckResolution_ReturnsUnresolvedOnly(t *testing.T) {
	out := CheckResolution([]string{
		"localhost",                          // always resolves
		"e2e-impossible-host-9aef3b.invalid", // .invalid never resolves
	})
	assert.Equal(t, []string{"e2e-impossible-host-9aef3b.invalid"}, out)
}

func TestCheckResolution_EmptyInput_EmptyOutput(t *testing.T) {
	assert.Empty(t, CheckResolution(nil))
}
