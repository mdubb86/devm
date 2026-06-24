package router

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	localias "github.com/mdubb86/localias/client"

	"github.com/mdubb86/devm/internal/schema"
)

// Resolver manages how *.PROJECT.local hostnames resolve on the Mac.
// Implementations: snippetResolver (prints a hint), localiasResolver
// (calls the localias service).
type Resolver interface {
	Apply(ctx context.Context, hostnames []string) error
	Remove(ctx context.Context, hostnames []string) error
}

// snippetResolver prints a copy-paste snippet for /etc/hosts when
// any of the hostnames don't resolve. Remove is a no-op — devm
// doesn't manage the user's /etc/hosts file.
type snippetResolver struct {
	out   io.Writer
	check func(hostnames []string) []string // injectable for testing
}

func newSnippetResolver() *snippetResolver {
	return &snippetResolver{out: os.Stdout, check: CheckResolution}
}

func (r *snippetResolver) Apply(_ context.Context, hostnames []string) error {
	unresolved := r.check(hostnames)
	if len(unresolved) == 0 {
		return nil
	}
	fmt.Fprintf(r.out,
		"\nThese hostnames don't resolve. Add to /etc/hosts:\n"+
			"  127.0.0.1 %s\n\n"+
			"Or use a tool like localias to manage hostnames automatically.\n",
		strings.Join(unresolved, " "),
	)
	return nil
}

func (r *snippetResolver) Remove(_ context.Context, _ []string) error {
	return nil
}

// localiasClient is the subset of the localias Go client devm uses.
// Mocked in tests; satisfied by *localias.Client in production.
type localiasClient interface {
	Add(ctx context.Context, alias string) (bool, error)
	Remove(ctx context.Context, alias string) (bool, error)
}

type localiasResolver struct {
	client localiasClient
}

func newLocaliasResolver() *localiasResolver {
	return &localiasResolver{client: localias.New()}
}

func (r *localiasResolver) Apply(ctx context.Context, hostnames []string) error {
	for _, h := range hostnames {
		if _, err := r.client.Add(ctx, h); err != nil {
			return fmt.Errorf("localias: register %s: %w", h, err)
		}
	}
	return nil
}

func (r *localiasResolver) Remove(ctx context.Context, hostnames []string) error {
	for _, h := range hostnames {
		if _, err := r.client.Remove(ctx, h); err != nil {
			return fmt.Errorf("localias: remove %s: %w", h, err)
		}
	}
	return nil
}

// NewResolver dispatches on cfg.Project.HostResolver. Unknown values
// would have failed schema validation, but we double-check defensively.
func NewResolver(cfg schema.Config) (Resolver, error) {
	switch cfg.Project.HostResolver {
	case "", "snippet":
		return newSnippetResolver(), nil
	case "localias":
		return newLocaliasResolver(), nil
	default:
		return nil, fmt.Errorf("unknown project.host_resolver: %q", cfg.Project.HostResolver)
	}
}
