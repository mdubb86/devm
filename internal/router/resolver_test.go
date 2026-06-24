package router

import (
	"bytes"
	"context"
	"errors"
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

type fakeLocaliasClient struct {
	added   []string
	removed []string
	addErr  error
}

func (f *fakeLocaliasClient) Add(ctx context.Context, alias string) (bool, error) {
	f.added = append(f.added, alias)
	return true, f.addErr
}
func (f *fakeLocaliasClient) Remove(ctx context.Context, alias string) (bool, error) {
	f.removed = append(f.removed, alias)
	return true, nil
}

func TestLocaliasResolver_Apply_CallsAddPerHostname(t *testing.T) {
	fake := &fakeLocaliasClient{}
	r := &localiasResolver{client: fake}
	err := r.Apply(context.Background(), []string{"a.foo.local", "b.foo.local"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a.foo.local", "b.foo.local"}, fake.added)
}

func TestLocaliasResolver_Apply_PropagatesError(t *testing.T) {
	fake := &fakeLocaliasClient{addErr: errors.New("conn refused")}
	r := &localiasResolver{client: fake}
	err := r.Apply(context.Background(), []string{"a.foo.local"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "localias")
}

func TestLocaliasResolver_Remove_CallsRemovePerHostname(t *testing.T) {
	fake := &fakeLocaliasClient{}
	r := &localiasResolver{client: fake}
	err := r.Remove(context.Background(), []string{"a.foo.local"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a.foo.local"}, fake.removed)
}

func TestNewResolver_DispatchesLocalias(t *testing.T) {
	cfg := schema.Config{Project: schema.Project{HostResolver: "localias"}}
	r, err := NewResolver(cfg)
	require.NoError(t, err)
	assert.Equal(t, "*router.localiasResolver", typeName(r))
}
