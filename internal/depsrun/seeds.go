package depsrun

import (
	"context"
	"fmt"

	"github.com/sneat-dev/wb/internal/deps"
)

func (service *Service) Seed(ctx context.Context, request SeedRequest) (SeedResult, error) {
	if !request.Latest {
		events, err := parseReleaseEvents(request.Ecosystem, request.Changed)
		return SeedResult{Events: events}, err
	}
	var explicit []deps.ReleaseEvent
	if len(request.Changed) > 0 {
		parsed, err := parseReleaseEvents(request.Ecosystem, request.Changed)
		if err != nil {
			return SeedResult{}, err
		}
		explicit = parsed
	}
	derived, resolutions, err := service.deps.DeriveLatest(ctx, request.Repositories, request.Scopes, request.Options)
	if err != nil {
		return SeedResult{}, err
	}
	return SeedResult{Events: deps.MergeReleaseEvents(explicit, derived), Resolutions: resolutions}, nil
}
func parseReleaseEvents(ecosystem deps.Ecosystem, values []string) ([]deps.ReleaseEvent, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one --changed module@version event is required")
	}
	events := make([]deps.ReleaseEvent, 0, len(values))
	for _, value := range values {
		target, err := deps.ParseTarget(string(ecosystem), value)
		if err != nil {
			return nil, err
		}
		events = append(events, deps.ReleaseEvent{Dependency: target.Dependency, Version: target.Version, Source: "explicit"})
	}
	return events, nil
}
