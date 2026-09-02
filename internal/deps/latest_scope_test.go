package deps

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// Seeding a coordinated release meant typing every module@version by hand.
// --latest --scope reads the same registry the wave engine polls and derives
// that list from what is actually published, so an omitted provider — which is
// not an error, just a consumer that stays stale — cannot happen by typo.

func newLatestScopeFleet(t *testing.T) (string, []Repository) {
	t.Helper()
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	return githubDir, []Repository{
		newNpmBumpRepository(t, root, githubDir, "core", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/core", "left-pad", "^1.0.0"),
		}),
		newNpmBumpRepository(t, root, githubDir, "extras", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/extras", "@acme/core", "0.1.0"),
		}),
		newNpmBumpRepository(t, root, githubDir, "unrelated", map[string]string{
			"package.json": npmPackageJSONWithDependency("@other/thing", "@acme/core", "0.1.0"),
		}),
	}
}

func latestScopeOptions(githubDir string, versions map[string]string) BumpOptions {
	return BumpOptions{
		Ecosystem: EcosystemNPM,
		Options:   Options{GitHubDir: githubDir, Ref: "main", Parallel: 2},
		LatestNpmVersion: func(_ context.Context, module string) (string, error) {
			version, published := versions[module]
			if !published {
				return "", errors.New("404 Not Found - GET https://registry.test/" + module)
			}
			return version, nil
		},
	}
}

func TestDeriveLatestReleaseEventsSeedsEveryPublishedModuleInScope(t *testing.T) {
	githubDir, repositories := newLatestScopeFleet(t)

	events, resolutions, err := DeriveLatestReleaseEvents(context.Background(), repositories, []string{"@acme/*"},
		latestScopeOptions(githubDir, map[string]string{"@acme/core": "0.4.0", "@acme/extras": "0.2.1"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want one per published module in scope", events)
	}
	if events[0].Dependency != "@acme/core" || events[0].Version != "0.4.0" || events[0].Source != "registry:latest" {
		t.Fatalf("first event = %+v", events[0])
	}
	if events[1].Dependency != "@acme/extras" || events[1].Version != "0.2.1" {
		t.Fatalf("second event = %+v", events[1])
	}
	// The out-of-scope module is never queried, let alone seeded: a scope is a
	// selection, not a hint.
	for _, resolution := range resolutions {
		if resolution.Dependency == "@other/thing" {
			t.Fatalf("an out-of-scope module was resolved: %+v", resolutions)
		}
	}
	if len(resolutions) != 2 || resolutions[0].Repository != "acme/core" {
		t.Fatalf("resolutions = %+v", resolutions)
	}
}

// A module that matches the scope but has published nothing must appear in the
// evidence with its registry reason, and must not become an invented event.
// "This scope publishes four modules" and "four of this scope's modules could
// be read" are different statements, and only one of them is true here.
func TestDeriveLatestReleaseEventsRecordsUnpublishedModulesWithoutSeedingThem(t *testing.T) {
	githubDir, repositories := newLatestScopeFleet(t)

	events, resolutions, err := DeriveLatestReleaseEvents(context.Background(), repositories, []string{"@acme/*"},
		latestScopeOptions(githubDir, map[string]string{"@acme/core": "0.4.0"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Dependency != "@acme/core" {
		t.Fatalf("events = %+v, want only the published module", events)
	}
	if len(resolutions) != 2 {
		t.Fatalf("resolutions = %+v, want a row for every matched module", resolutions)
	}
	unpublished := resolutions[1]
	if unpublished.Dependency != "@acme/extras" || unpublished.Version != "" || !strings.Contains(unpublished.Reason, "404") {
		t.Fatalf("unpublished resolution = %+v, want the registry's own reason", unpublished)
	}
}

func TestDeriveLatestReleaseEventsRefusesWithoutAScope(t *testing.T) {
	githubDir, repositories := newLatestScopeFleet(t)

	_, _, err := DeriveLatestReleaseEvents(context.Background(), repositories, []string{"  "},
		latestScopeOptions(githubDir, nil))
	if err == nil || !strings.Contains(err.Error(), "--scope") {
		t.Fatalf("error = %v, want a refusal naming --scope", err)
	}
}

func TestDeriveLatestReleaseEventsRefusesAScopeThatMatchesNothing(t *testing.T) {
	githubDir, repositories := newLatestScopeFleet(t)

	_, _, err := DeriveLatestReleaseEvents(context.Background(), repositories, []string{"@nobody/*"},
		latestScopeOptions(githubDir, map[string]string{"@acme/core": "0.4.0"}))
	if err == nil || !strings.Contains(err.Error(), "@nobody/*") {
		t.Fatalf("error = %v, want a refusal naming the scope that matched nothing", err)
	}
}

// A scope whose modules all fail to resolve is a refusal, not an empty
// campaign: silently bumping nothing looks identical to success.
func TestDeriveLatestReleaseEventsRefusesWhenNothingInScopeIsPublished(t *testing.T) {
	githubDir, repositories := newLatestScopeFleet(t)

	_, resolutions, err := DeriveLatestReleaseEvents(context.Background(), repositories, []string{"@acme/*"},
		latestScopeOptions(githubDir, nil))
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want a refusal carrying the registry's reason", err)
	}
	if len(resolutions) != 2 {
		t.Fatalf("resolutions = %+v, want the evidence returned alongside the refusal", resolutions)
	}
}

// --exclude removes a repository from the campaign entirely. Deriving a seed
// event from a module WB has agreed never to touch would push the whole fleet
// onto a version this run refuses to verify.
func TestDeriveLatestReleaseEventsHonoursExcludedRepositories(t *testing.T) {
	githubDir, repositories := newLatestScopeFleet(t)

	options := latestScopeOptions(githubDir, map[string]string{"@acme/core": "0.4.0", "@acme/extras": "0.2.1"})
	options.ExcludeRepositories = []string{"acme/extras"}
	events, resolutions, err := DeriveLatestReleaseEvents(context.Background(), repositories, []string{"@acme/*"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Dependency != "@acme/core" {
		t.Fatalf("events = %+v, want nothing derived from an excluded repository", events)
	}
	if len(resolutions) != 1 {
		t.Fatalf("resolutions = %+v", resolutions)
	}
}

func TestDeriveLatestReleaseEventsRefusesUnderANoRegistryPlan(t *testing.T) {
	githubDir, repositories := newLatestScopeFleet(t)

	options := latestScopeOptions(githubDir, map[string]string{"@acme/core": "0.4.0"})
	options.NoRegistry = true
	_, _, err := DeriveLatestReleaseEvents(context.Background(), repositories, []string{"@acme/*"}, options)
	if err == nil || !strings.Contains(err.Error(), "registry") {
		t.Fatalf("error = %v, want --latest refused where registry lookups are forbidden", err)
	}
}

// The derived events must actually drive the campaign, not merely be printed:
// this runs the wave engine on them and asserts the consumer was planned.
func TestRunBumpAcceptsDerivedLatestEventsAndReportsTheirProvenance(t *testing.T) {
	githubDir, repositories := newLatestScopeFleet(t)

	options := latestScopeOptions(githubDir, map[string]string{"@acme/core": "0.4.0"})
	events, resolutions, err := DeriveLatestReleaseEvents(context.Background(), repositories, []string{"@acme/core"}, options)
	if err != nil {
		t.Fatal(err)
	}
	options.DryRun = true
	options.Scopes = []string{"@acme/core"}
	options.ScopeResolutions = resolutions
	report, err := RunBump(context.Background(), events, repositories, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Waves) != 1 {
		t.Fatalf("waves = %+v", report.Waves)
	}
	planned := map[string]bool{}
	for _, repository := range report.Waves[0].Repositories {
		planned[repository.Repository] = true
	}
	if !planned["acme/extras"] || !planned["acme/unrelated"] {
		t.Fatalf("derived events did not drive the wave: %+v", report.Waves[0].Repositories)
	}
	if len(report.SeedEvents) != 1 || report.SeedEvents[0].Source != "registry:latest" {
		t.Fatalf("seed events = %+v, want the derivation's provenance preserved", report.SeedEvents)
	}
	markdown := report.Markdown()
	for _, want := range []string{"Derived scopes", "@acme/core", "0.4.0"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("markdown missing %q:\n%s", want, markdown)
		}
	}
}

func TestMergeReleaseEventsKeepsTheNewestObservationPerDependency(t *testing.T) {
	t.Parallel()
	merged := MergeReleaseEvents(
		[]ReleaseEvent{{Dependency: "@acme/core", Version: "0.5.0", Source: "explicit"}},
		[]ReleaseEvent{
			{Dependency: "@acme/core", Version: "0.4.0", Source: "registry:latest"},
			{Dependency: "@acme/extras", Version: "0.2.1", Source: "registry:latest"},
		},
	)
	if len(merged) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
	// A release still in flight, named with --changed, outranks the older
	// version the registry can currently see.
	if merged[0].Version != "0.5.0" || merged[0].Source != "explicit" {
		t.Fatalf("merged[0] = %+v, want the in-flight explicit event to win", merged[0])
	}
	if merged[1].Dependency != "@acme/extras" {
		t.Fatalf("merged[1] = %+v", merged[1])
	}
}
