package umbrella

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// State is the open/closed lifecycle state reported by an umbrella provider.
type State string

const (
	StateOpen   State = "open"
	StateClosed State = "closed"
)

// Info is the provider-resolved metadata for an umbrella reference.
// It deliberately omits title and body text so private umbrella prose can
// never leak into public pull requests, commits, or reports.
type Info struct {
	Ref   Ref    `json:"ref"`
	State State  `json:"state"`
	Link  string `json:"link"`
}

// Provider plugs an umbrella storage registry into WB.
type Provider interface {
	Registry() string
	Resolve(ctx context.Context, ref Ref) (Info, error)
	AppendStatus(ctx context.Context, ref Ref, line string) error
	Close(ctx context.Context, ref Ref, summary string) error
	RenderLink(ref Ref) (string, error)
}

// Registry routes typed umbrella references to registered providers.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry builds a provider registry from the given providers.
func NewRegistry(providers ...Provider) *Registry {
	reg := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(provider.Registry()))
		if name != "" {
			reg.providers[name] = provider
		}
	}
	return reg
}

// DefaultProviders constructs the built-in umbrella provider registry.
// Only the GitHub provider is registered today.
//
// TODO(sneat-dev/wb#1027): register the Synchestra umbrella provider once
// implemented in its follow-up issue.
func DefaultProviders(exec GitHubExecutor) *Registry {
	return NewRegistry(NewGitHubProvider(exec))
}

// Supported returns the sorted list of registered umbrella registry names.
func (r *Registry) Supported() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Provider returns the provider registered for registry, or an exact refusal
// naming the unsupported registry and the supported registries.
func (r *Registry) Provider(registry string) (Provider, error) {
	name := strings.ToLower(strings.TrimSpace(registry))
	if r != nil {
		if provider, ok := r.providers[name]; ok {
			return provider, nil
		}
	}
	supported := "(none)"
	if r != nil && len(r.providers) > 0 {
		supported = strings.Join(r.Supported(), ", ")
	}
	// TODO(sneat-dev/wb#1027): remove this explicit Synchestra refusal once
	// the Synchestra umbrella provider ships in its follow-up issue.
	if name == RegistrySynchestra {
		return nil, fmt.Errorf("umbrella registry %q is not implemented yet (supported: %s); see follow-up to sneat-dev/wb#1027", name, supported)
	}
	return nil, fmt.Errorf("unknown umbrella registry %q (supported: %s)", name, supported)
}

// Resolve resolves and validates ref through its registered provider.
func (r *Registry) Resolve(ctx context.Context, ref Ref) (Info, error) {
	provider, err := r.Provider(ref.Registry)
	if err != nil {
		return Info{}, err
	}
	return provider.Resolve(ctx, ref)
}

// AppendStatus appends one status line to ref through its registered provider.
func (r *Registry) AppendStatus(ctx context.Context, ref Ref, line string) error {
	provider, err := r.Provider(ref.Registry)
	if err != nil {
		return err
	}
	return provider.AppendStatus(ctx, ref, line)
}

// Close posts a final summary and closes ref through its registered provider.
func (r *Registry) Close(ctx context.Context, ref Ref, summary string) error {
	provider, err := r.Provider(ref.Registry)
	if err != nil {
		return err
	}
	return provider.Close(ctx, ref, summary)
}

// RenderLink renders the link text for ref through its registered provider.
func (r *Registry) RenderLink(ref Ref) (string, error) {
	provider, err := r.Provider(ref.Registry)
	if err != nil {
		return "", err
	}
	return provider.RenderLink(ref)
}
