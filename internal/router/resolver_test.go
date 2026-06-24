package router

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnippetResolver_Apply_PrintsSnippet_WhenUnresolved(t *testing.T) {
	var buf bytes.Buffer
	r := &snippetResolver{
		out:   &buf,
		check: func(hostnames []string) []string { return hostnames }, // all unresolved
	}
	err := r.Apply(context.Background(), []string{"a.foo.local", "b.foo.local"})
	require.NoError(t, err)
	got := buf.String()
	assert.Contains(t, got, "127.0.0.1 a.foo.local b.foo.local")
	assert.Contains(t, got, "/etc/hosts")
	assert.Contains(t, got, "localias")
}

func TestSnippetResolver_Apply_QuietWhenAllResolve(t *testing.T) {
	var buf bytes.Buffer
	r := &snippetResolver{
		out:   &buf,
		check: func(hostnames []string) []string { return nil }, // all resolve
	}
	err := r.Apply(context.Background(), []string{"a.foo.local"})
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "should print nothing when everything resolves")
}

func TestSnippetResolver_Remove_NoOp(t *testing.T) {
	var buf bytes.Buffer
	r := &snippetResolver{out: &buf, check: CheckResolution}
	err := r.Remove(context.Background(), []string{"a.foo.local"})
	require.NoError(t, err)
	assert.Empty(t, buf.String())
}

func TestNewResolver_DispatchesOnHostResolver(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"default (empty) is snippet", "", "*router.snippetResolver"},
		{"snippet explicit", "snippet", "*router.snippetResolver"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := schema.Config{Project: schema.Project{HostResolver: c.value}}
			r, err := NewResolver(cfg)
			require.NoError(t, err)
			assert.Equal(t, c.want, typeName(r))
		})
	}
}

func typeName(v any) string { return fmt.Sprintf("%T", v) }

func TestNewResolver_RejectsUnknownValue(t *testing.T) {
	cfg := schema.Config{Project: schema.Project{HostResolver: "dnsmasq"}}
	_, err := NewResolver(cfg)
	require.Error(t, err)
}
