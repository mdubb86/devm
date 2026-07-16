package nftscript

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestBuildSvcIngressScript(t *testing.T) {
	s := BuildSvcIngressScript([]int{54322, 6543})
	assert.Contains(t, s, "flush chain inet devm_filter svc_ingress")
	assert.Contains(t, s, "ct original proto-dst 54322 accept")
	assert.Contains(t, s, "ct original proto-dst 6543 accept")
	assert.Contains(t, s, `comment "devm: direct ingress`)
	assert.Contains(t, s, "/etc/nftables.d/svc_ingress.conf")

	// Empty set still flushes (closes everything) and snapshots.
	empty := BuildSvcIngressScript(nil)
	assert.Contains(t, empty, "flush chain inet devm_filter svc_ingress")
	assert.NotContains(t, empty, "ct original proto-dst")
}

func TestDirectPorts_NonDockerReturnsNil(t *testing.T) {
	cfg := schema.Config{
		Docker: false,
		Services: map[string]schema.Service{
			"db": {Port: 54322, Direct: true},
		},
	}
	assert.Nil(t, DirectPorts(cfg))
}

func TestDirectPorts_DockerReturnsSortedDirectPorts(t *testing.T) {
	cfg := schema.Config{
		Docker: true,
		Services: map[string]schema.Service{
			"db":  {Port: 54322, Direct: true},
			"api": {Port: 8080, Direct: false},
			"web": {Port: 6543, Direct: true},
			"nop": {Direct: true}, // Port == 0 excluded
		},
	}
	assert.Equal(t, []int{6543, 54322}, DirectPorts(cfg))
}
