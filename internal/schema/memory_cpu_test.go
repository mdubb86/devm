package schema

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestParseMemorySize_Accept(t *testing.T) {
	cases := []struct {
		in string
		mb int
	}{
		{"1G", 1024},
		{"8G", 8192},
		{"8g", 8192}, // case-insensitive suffix, matches ParseDiskSize
		{"16G", 16384},
		{"64G", 65536},
		{"8GB", 8192},
		{" 8G ", 8192}, // whitespace tolerated
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseMemorySize(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.mb, got)
		})
	}
}

func TestParseMemorySize_Reject(t *testing.T) {
	cases := []string{
		"8",   // no suffix
		"8M",  // wrong unit
		"8T",  // wrong unit
		"0G",  // non-positive
		"-8G", // negative
		"abc", // garbage
		"",    // empty (callers should nil-check, but parser must still reject)
		"G",   // suffix alone, no magnitude
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			_, err := ParseMemorySize(s)
			require.Error(t, err)
		})
	}
}

func TestValidate_MemoryCpu_NilOK(t *testing.T) {
	c := Config{Project: Project{Name: "p"}, Memory: nil, Cpu: nil}
	require.NoError(t, c.Validate())
}

func TestValidate_MemoryCpu_ValidValues(t *testing.T) {
	m := "8G"
	n := 6
	c := Config{Project: Project{Name: "p"}, Memory: &m, Cpu: &n}
	require.NoError(t, c.Validate())
}

func TestValidate_Memory_MalformedRejected(t *testing.T) {
	bad := "eight gigs"
	c := Config{Project: Project{Name: "p"}, Memory: &bad}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "memory")
}

func TestValidate_Cpu_NonPositiveRejected(t *testing.T) {
	for _, n := range []int{0, -1, -8} {
		t.Run(fmt.Sprintf("%d", n), func(t *testing.T) {
			cpu := n
			c := Config{Project: Project{Name: "p"}, Cpu: &cpu}
			err := c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cpu")
		})
	}
}

func TestUnmarshal_MemoryCpu_AbsentKeysStayNil(t *testing.T) {
	var c Config
	require.NoError(t, yaml.Unmarshal([]byte("project:\n  name: p\n"), &c))
	assert.Nil(t, c.Memory)
	assert.Nil(t, c.Cpu)
}

func TestUnmarshal_MemoryCpu_PresentKeysNonNil(t *testing.T) {
	var c Config
	require.NoError(t, yaml.Unmarshal([]byte("project:\n  name: p\nmemory: \"8G\"\ncpu: 6\n"), &c))
	require.NotNil(t, c.Memory)
	require.NotNil(t, c.Cpu)
	assert.Equal(t, "8G", *c.Memory)
	assert.Equal(t, 6, *c.Cpu)
}

func TestCheckUnknownKeys_AcceptsMemoryCpu(t *testing.T) {
	err := CheckUnknownKeys([]byte("project:\n  name: p\nmemory: \"8G\"\ncpu: 6\n"))
	require.NoError(t, err)
}
