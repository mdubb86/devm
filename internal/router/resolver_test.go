package router

import (
	"bytes"
	"context"
	"testing"

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
