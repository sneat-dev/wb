package deps

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sneat-dev/wb/internal/progress"
)

// LatestScopeResolution is one module a `--scope` glob matched, and either the
// published version WB read from the registry or the reason it could not.
//
// Every matched module gets a row, resolved or not. A campaign seeded from the
// registry must be auditable after the fact: "this scope produced four events"
// is not the same statement as "this scope matched four modules", and an
// operator who cannot see the difference cannot tell a deliberately unpublished
// module from a registry lookup that quietly failed.
type LatestScopeResolution struct {
	Dependency string `yaml:"dependency"`
	Repository string `yaml:"repository,omitempty"`
	Version    string `yaml:"version,omitempty"`
	Reason     string `yaml:"reason,omitempty"`
}

// DeriveLatestReleaseEvents answers "what has this scope published?" so the
// operator does not have to type it.
//
// Seeding a campaign meant naming every `module@version` by hand on the command
// line. For a coordinated release of a dozen packages under one scope that is a
// dozen chances to typo a version, to name a module that was never published,
// or to silently omit one — and an omitted provider is not an error, it is a
// consumer that stays stale.
//
// So WB reads the fleet graph for the modules the selected repositories
// actually declare, keeps the ones a `--scope` glob matches, and asks the same
// registry the wave engine itself polls for each one's published latest
// version. The result is exactly the `--changed` list the operator would have
// typed, derived from what is published rather than from what they remembered.
//
// Scopes are `path.Match` globs matched against the module path or package
// name, identical to `wb deps drift --scope`: `*` never crosses a `/`, so
// `@sneat/*` matches `@sneat/core` and `github.com/dal-go/*` matches
// `github.com/dal-go/dalgo` but not a nested `github.com/dal-go/dalgo/x`.
//
// A module that matches but has no readable published version is never
// invented into an event; it is returned as a resolution carrying its reason.
func DeriveLatestReleaseEvents(ctx context.Context, repositories []Repository, scopes []string, options BumpOptions) ([]ReleaseEvent, []LatestScopeResolution, error) {
	scopes = normalizeScopes(scopes)
	if len(scopes) == 0 {
		return nil, nil, fmt.Errorf("deriving release events from the registry requires at least one --scope glob, e.g. --scope '@sneat/*'")
	}
	if options.NoRegistry {
		return nil, nil, fmt.Errorf("--latest reads published versions from the registry, so it cannot be combined with a plan that forbids registry lookups")
	}
	ecosystem := options.Ecosystem
	if ecosystem == "" {
		ecosystem = EcosystemGo
	}
	options.Ecosystem = ecosystem
	// A repository --exclude removed from the campaign must not seed it
	// either. Deriving an event from a module WB has agreed never to touch
	// would push the whole fleet onto a version this run refuses to verify.
	repositories, _ = partitionExcludedRepositories(repositories, options.ExcludeRepositories)
	graph, err := BuildGraph(ctx, repositories, GraphOptions{
		Ecosystem: ecosystem, GitHubDir: options.GitHubDir, Ref: options.Ref,
		Parallel: options.Parallel, Timeout: options.Timeout, Retry: options.Retry,
		Progress: options.Progress,
	})
	if err != nil {
		return nil, nil, err
	}
	matched := matchedScopeModules(graph, scopes)
	if len(matched) == 0 {
		return nil, nil, fmt.Errorf("no module declared by the selected repositories matches %s; nothing to derive", strings.Join(quoteAll(scopes), ", "))
	}
	resolutions := resolveLatestScopeModules(ctx, matched, options)
	var events []ReleaseEvent
	for _, resolution := range resolutions {
		if resolution.Version == "" {
			continue
		}
		events = append(events, ReleaseEvent{
			Dependency: resolution.Dependency, Version: resolution.Version,
			Source: "registry:latest", CheckedAt: bumpNow(options),
		})
	}
	if len(events) == 0 {
		return nil, resolutions, fmt.Errorf(
			"%s matched %d module(s) but none has a readable published version: %s",
			strings.Join(quoteAll(scopes), ", "), len(matched), firstScopeReasons(resolutions),
		)
	}
	return mergeReleaseEvents(events), resolutions, nil
}

// matchedScopeModules keeps one entry per declared module path, so a module
// declared in several manifests of one repository is looked up once.
func matchedScopeModules(graph Graph, scopes []string) []LatestScopeResolution {
	seen := map[string]bool{}
	var matched []LatestScopeResolution
	for _, module := range graph.Modules {
		if module.Path == "" || seen[module.Path] || !matchesAnyGlob(module.Path, scopes) {
			continue
		}
		seen[module.Path] = true
		matched = append(matched, LatestScopeResolution{Dependency: module.Path, Repository: module.Repository})
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Dependency < matched[j].Dependency })
	return matched
}

// resolveLatestScopeModules reads each matched module's published version. The
// lookups run on the same worker bound the rest of the campaign uses: a scope
// covering a large fleet is dozens of independent registry reads, and doing
// them one at a time is the dominant cost of simply deciding what to seed.
func resolveLatestScopeModules(ctx context.Context, matched []LatestScopeResolution, options BumpOptions) []LatestScopeResolution {
	resolutions := append([]LatestScopeResolution(nil), matched...)
	workers := readOnlyWorkerCount(options.Parallel, options.ParallelExplicit, len(resolutions))
	jobs := make(chan int)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				version, err := latestReleaseVersion(ctx, resolutions[index].Dependency, options)
				if err != nil {
					resolutions[index].Reason = err.Error()
					continue
				}
				resolutions[index].Version = version
			}
		}()
	}
	for index := range resolutions {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	progress.Report(options.Progress, progress.Event{
		Operation: "deps bump", Phase: "derive_release_events", State: progress.Completed,
		Completed: len(resolutions), Total: len(resolutions),
	})
	return resolutions
}

// NormalizeScopes drops blank and whitespace-only scope globs. It is exported
// so a caller can refuse "--latest with no usable scope" before discovering a
// fleet, using exactly the emptiness rule the derivation itself applies rather
// than a second one that could disagree.
func NormalizeScopes(scopes []string) []string {
	return normalizeScopes(scopes)
}

func normalizeScopes(scopes []string) []string {
	var normalized []string
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		normalized = append(normalized, scope)
	}
	return normalized
}

func quoteAll(values []string) []string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, fmt.Sprintf("%q", value))
	}
	return quoted
}

// firstScopeReasons names why the matched modules produced nothing, so the
// refusal carries the registry's own words rather than a generic failure.
func firstScopeReasons(resolutions []LatestScopeResolution) string {
	var reasons []string
	for _, resolution := range resolutions {
		if resolution.Reason == "" {
			continue
		}
		reasons = append(reasons, resolution.Dependency+": "+resolution.Reason)
		if len(reasons) == 3 {
			break
		}
	}
	if len(reasons) == 0 {
		return "no registry reason was recorded"
	}
	return strings.Join(reasons, "; ")
}
