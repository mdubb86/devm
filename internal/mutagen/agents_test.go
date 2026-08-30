package mutagen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLinuxArm64Agent_ReturnsBinaryBytes(t *testing.T) {
	body, err := LinuxArm64Agent()
	require.NoError(t, err)
	require.Greater(t, len(body), 1_000_000,
		"agent binary should be several MB, not a stub or error message")
	// ELF magic: linux/arm64 binaries start with 0x7F 'E' 'L' 'F'.
	assert.Equal(t, byte(0x7F), body[0])
	assert.Equal(t, byte('E'), body[1])
	assert.Equal(t, byte('L'), body[2])
	assert.Equal(t, byte('F'), body[3])
}
