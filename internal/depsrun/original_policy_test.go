package depsrun

import (
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
)

func TestCwDepsMatchesDependencyRepository(t *testing.T) {
	expression, err := CompileRegex("^acme/")
	if err != nil {
		t.Fatal(err)
	}
	if !matchesDependencyRepository("acme/app", "acme/*", expression) {
		t.Error("glob and regex that both match must select")
	}
	if matchesDependencyRepository("acme/app", "beta/*", expression) {
		t.Error("a failing glob must not select")
	}
	if matchesDependencyRepository("beta/app", "beta/*", expression) {
		t.Error("a failing regex must not select")
	}
	if !matchesDependencyRepository("beta/app", "", nil) {
		t.Error("no filter at all must select")
	}
	if matchesDependencyRepository("acme/app", "[", nil) {
		t.Error("an invalid glob must not select")
	}
	if expression, err := CompileRegex(""); err != nil || expression != nil {
		t.Fatalf("empty regex = (%v, %v), want nil, nil", expression, err)
	}
}

func TestGitHubSlugSupportsSSHAndHTTPS(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"git@github.com:strongo/cicd.git":       "strongo/cicd",
		"https://github.com/sneat-dev/wb.git":   "sneat-dev/wb",
		"ssh://git@github.com/dal-go/dalgo.git": "dal-go/dalgo",
	}
	for remote, want := range tests {
		if got := githubSlug(remote); got != want {
			t.Errorf("githubSlug(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestParseReleaseEventsPreservesMultipleExactSeeds(t *testing.T) {
	t.Parallel()
	events, err := parseReleaseEvents(deps.EcosystemGo, []string{"example.com/a@v0.2.0", "example.com/b@v1.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != (deps.ReleaseEvent{Dependency: "example.com/a", Version: "v0.2.0", Source: "explicit"}) {
		t.Fatalf("events = %+v", events)
	}
}

func TestParseReleaseEventsSupportsScopedNpmPackages(t *testing.T) {
	t.Parallel()
	events, err := parseReleaseEvents(deps.EcosystemNPM, []string{"@sneat/core@2.3.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0] != (deps.ReleaseEvent{Dependency: "@sneat/core", Version: "2.3.1", Source: "explicit"}) {
		t.Fatalf("events = %+v", events)
	}
}

func TestResolveDepsBumpResumeParallelRejectsAnInvalidPersistedValue(t *testing.T) {
	lifecycle := deps.Options{Parallel: 4}
	report := deps.BumpReport{Parallel: 0}
	_, _, err := resolveDepsBumpResumeParallel(lifecycle, report, false)
	if err == nil || !strings.Contains(err.Error(), "resume report has invalid parallelism") {
		t.Fatalf("resolveDepsBumpResumeParallel(report.Parallel=0, explicit=false) = %v, want the invalid-parallelism refusal", err)
	}
}

func TestResolveDepsBumpResumeParallelRestoresExplicitAuthority(t *testing.T) {
	t.Parallel()
	// A campaign started with an explicit --parallel 1 and resumed without the
	// flag must keep serial authority: the read-only worker floor stays off.
	lifecycle, report, err := resolveDepsBumpResumeParallel(
		deps.Options{Parallel: 1},
		deps.BumpReport{Parallel: 1, ParallelExplicit: true},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.Parallel != 1 || !lifecycle.ParallelExplicit || !report.ParallelExplicit {
		t.Fatalf("non-explicit resume lost the original explicit authority: lifecycle=%+v report=%+v", lifecycle, report)
	}

	// A default campaign resumed without the flag keeps the floor available.
	lifecycle, report, err = resolveDepsBumpResumeParallel(
		deps.Options{Parallel: 1},
		deps.BumpReport{Parallel: 1},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.ParallelExplicit || report.ParallelExplicit {
		t.Fatalf("default resume must stay non-explicit: lifecycle=%+v report=%+v", lifecycle, report)
	}

	// An explicit override while resuming records the new authority on the
	// report so the next resume restores it too.
	lifecycle, report, err = resolveDepsBumpResumeParallel(
		deps.Options{Parallel: 2, ParallelExplicit: true},
		deps.BumpReport{Parallel: 1},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.Parallel != 2 || !lifecycle.ParallelExplicit || report.Parallel != 2 || !report.ParallelExplicit {
		t.Fatalf("explicit resume override was not recorded: lifecycle=%+v report=%+v", lifecycle, report)
	}
}
