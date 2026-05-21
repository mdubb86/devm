package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmbeddedBinariesNonEmpty(t *testing.T) {
	assert.NotEmpty(t, BinaryAMD64, "linux/amd64 binary should be embedded")
	assert.NotEmpty(t, BinaryARM64, "linux/arm64 binary should be embedded")
}

func TestPickForVMSupportedArches(t *testing.T) {
	b, err := PickForVM("amd64")
	assert.NoError(t, err)
	assert.Equal(t, BinaryAMD64, b)

	b, err = PickForVM("arm64")
	assert.NoError(t, err)
	assert.Equal(t, BinaryARM64, b)
}

func TestPickForVMUnknownArch(t *testing.T) {
	_, err := PickForVM("riscv64")
	assert.Error(t, err)
}
