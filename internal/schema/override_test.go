package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

func TestConfigOverridePartial(t *testing.T) {
	src := []byte(`
project:
  port_offset: 50
`)
	var o ConfigOverride
	err := yaml.Unmarshal(src, &o)
	assert.NoError(t, err)
	assert.NotNil(t, o.Project)
	assert.Equal(t, 50, *o.Project.PortOffset)
}
