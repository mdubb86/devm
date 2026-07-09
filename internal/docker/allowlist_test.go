package docker

import (
	"reflect"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
)

func TestEffectiveAllowlist_DockerFalse_ReturnsUserOnly(t *testing.T) {
	cfg := schema.Config{
		Network: schema.Network{
			Allow: []schema.AllowEntry{
				{Host: "example.com"},
				{Host: "example.org"},
			},
		},
	}
	got := EffectiveAllowlist(cfg)
	want := []string{"example.com", "example.org"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestEffectiveAllowlist_DockerTrue_AddsDockerHub(t *testing.T) {
	cfg := schema.Config{
		Docker: true,
		Network: schema.Network{
			Allow: []schema.AllowEntry{{Host: "example.com"}},
		},
	}
	got := EffectiveAllowlist(cfg)
	want := []string{
		"example.com",
		"registry-1.docker.io",
		"auth.docker.io",
		"production.cloudfront.docker.com",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestEffectiveAllowlist_DockerTrue_NoDuplicate(t *testing.T) {
	cfg := schema.Config{
		Docker: true,
		Network: schema.Network{
			Allow: []schema.AllowEntry{
				{Host: "registry-1.docker.io"}, // user already listed it
				{Host: "example.com"},
			},
		},
	}
	got := EffectiveAllowlist(cfg)
	want := []string{
		"registry-1.docker.io",
		"example.com",
		"auth.docker.io",
		"production.cloudfront.docker.com",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}
