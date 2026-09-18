package deps

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// --exclude and --hold answer two different questions the founder asked on
// 2026-09-02, and conflating them is the whole hazard:
//
//   - --exclude: "this repository has no business in the campaign at all."
//     It never enters the graph, so it cannot join a wave, cannot get a
//     worktree, and cannot get a pull request.
//   - --hold: "do all the mechanical work, but the merge is mine." The
//     repository is bumped, verified, pushed, its PR is opened and CI-waited,
//     and then it is left OPEN — even under --merge. sneat-co/sneat-go and
//     sneat-co/sneat-apps are the live examples: gated deploy repositories
//     whose merge is a founder decision.

func newExcludeHoldFleet(t *testing.T) (string, []Repository) {
	t.Helper()
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	return githubDir, []Repository{
		newNpmBumpRepository(t, root, githubDir, "provider", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/provider", "left-pad", "^1.0.0"),
		}),
		newNpmBumpRepository(t, root, githubDir, "adapter", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/adapter", "@acme/provider", "0.1.0"),
		}),
		newNpmBumpRepository(t, root, githubDir, "deploy", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/deploy", "@acme/provider", "0.1.0"),
		}),
	}
}

func TestRunBumpExcludeRemovesTheRepositoryFromWaveComputation(t *testing.T) {
	githubDir, repositories := newExcludeHoldFleet(t)

	report, err := RunBump(context.Background(),
		[]ReleaseEvent{{Dependency: "@acme/provider", Version: "0.2.0", Source: "explicit"}},
		repositories, BumpOptions{
			Ecosystem: EcosystemNPM,
			Options: Options{
				GitHubDir: githubDir, Ref: "main", Parallel: 2, DryRun: true,
				ExcludeRepositories: []string{"acme/deploy"},
			},
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ExcludedRepositories) != 1 || report.ExcludedRepositories[0] != "acme/deploy" {
		t.Fatalf("excluded = %+v", report.ExcludedRepositories)
	}
	if len(report.Waves) != 1 {
		t.Fatalf("waves = %+v", report.Waves)
	}
	for _, repository := range report.Waves[0].Repositories {
		if repository.Repository == "acme/deploy" {
			t.Fatalf("an excluded repository entered wave computation: %+v", report.Waves[0].Repositories)
		}
	}
	if len(report.Waves[0].Repositories) != 1 || report.Waves[0].Repositories[0].Repository != "acme/adapter" {
		t.Fatalf("wave repositories = %+v, want only the non-excluded consumer", report.Waves[0].Repositories)
	}
	if markdown := report.Markdown(); !strings.Contains(markdown, "Excluded repositories") || !strings.Contains(markdown, "acme/deploy") {
		t.Fatalf("markdown must name excluded repositories:\n%s", markdown)
	}
}

func TestRunBumpExcludeGlobMatchesAWholeOwner(t *testing.T) {
	githubDir, repositories := newExcludeHoldFleet(t)

	report, err := RunBump(context.Background(),
		[]ReleaseEvent{{Dependency: "@acme/provider", Version: "0.2.0", Source: "explicit"}},
		repositories, BumpOptions{
			Ecosystem: EcosystemNPM,
			Options: Options{
				GitHubDir: githubDir, Ref: "main", Parallel: 1, DryRun: true,
				ExcludeRepositories: []string{"acme/*"},
			},
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ExcludedRepositories) != 3 {
		t.Fatalf("excluded = %+v, want every repository in the owner", report.ExcludedRepositories)
	}
	if report.Status != "completed" {
		t.Fatalf("status = %q, want a campaign with nothing left to do to complete", report.Status)
	}
}

// A held repository must never be treated as excluded: it is still bumped,
// still gets a pull request, and its release is still what later waves need.
func TestHoldAndExcludeAreDifferentSelections(t *testing.T) {
	githubDir, repositories := newExcludeHoldFleet(t)

	report, err := RunBump(context.Background(),
		[]ReleaseEvent{{Dependency: "@acme/provider", Version: "0.2.0", Source: "explicit"}},
		repositories, BumpOptions{
			Ecosystem: EcosystemNPM,
			Options: Options{
				GitHubDir: githubDir, Ref: "main", Parallel: 2, DryRun: true,
				Hold: []string{"acme/deploy"},
			},
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ExcludedRepositories) != 0 {
		t.Fatalf("--hold must not exclude anything: %+v", report.ExcludedRepositories)
	}
	planned := map[string]bool{}
	for _, repository := range report.Waves[0].Repositories {
		planned[repository.Repository] = true
	}
	if !planned["acme/deploy"] || !planned["acme/adapter"] {
		t.Fatalf("held repository must still be planned into its wave: %+v", report.Waves[0].Repositories)
	}
}

func TestMatchesHoldUsesPathMatchSemantics(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		slug     string
		patterns []string
		want     bool
	}{
		{slug: "sneat-co/sneat-go", patterns: []string{"sneat-co/sneat-go"}, want: true},
		{slug: "sneat-co/sneat-go", patterns: []string{"sneat-co/*"}, want: true},
		{slug: "sneat-co/sneat-go", patterns: []string{"sneat-co/sneat-apps"}, want: false},
		{slug: "sneat-co/sneat-go", patterns: []string{"sneat-co/sneat-apps", "sneat-co/sneat-go"}, want: true},
		{slug: "sneat-co/nested/thing", patterns: []string{"sneat-co/*"}, want: false},
		{slug: "sneat-co/sneat-go", patterns: nil, want: false},
		{slug: "sneat-co/sneat-go", patterns: []string{"  "}, want: false},
	} {
		if got := orchestrate.MatchesHold(testCase.slug, testCase.patterns); got != testCase.want {
			t.Fatalf("MatchesHold(%q, %v) = %v, want %v", testCase.slug, testCase.patterns, got, testCase.want)
		}
	}
}

func TestHeldRepositoriesProjectOpenPullRequests(t *testing.T) {
	t.Parallel()
	repositories := []RepositoryReport{
		{Repository: "sneat-co/zeta", Held: true, PR: "https://github.com/sneat-co/zeta/pull/2", Reason: "held"},
		{Repository: "sneat-co/alpha", Held: true, PR: "https://github.com/sneat-co/alpha/pull/1", Reason: "held"},
		{Repository: "sneat-co/merged", Merged: true, PR: "https://github.com/sneat-co/merged/pull/3"},
	}
	held := heldRepositoryPullRequests(repositories)
	if len(held) != 2 || held[0].Repository != "sneat-co/alpha" || held[1].Repository != "sneat-co/zeta" {
		t.Fatalf("held = %+v, want only held repositories, sorted", held)
	}
	if held[0].PR != "https://github.com/sneat-co/alpha/pull/1" {
		t.Fatalf("held PR = %+v", held[0])
	}
	// A campaign without --hold can never report a hold, whatever the results
	// happen to carry.
	if blockers := heldReleaseBlockers(repositories, nil); blockers != nil {
		t.Fatalf("heldReleaseBlockers without --hold = %+v, want none", blockers)
	}
	if blockers := heldReleaseBlockers(repositories, []string{"sneat-co/*"}); len(blockers) != 2 {
		t.Fatalf("heldReleaseBlockers = %+v", blockers)
	}
}

func TestMergeHeldRepositoriesIsIdempotentAndSorted(t *testing.T) {
	t.Parallel()
	first := []HeldRepository{{Repository: "sneat-co/zeta", PR: "z"}}
	merged := mergeHeldRepositories(first, []HeldRepository{
		{Repository: "sneat-co/alpha", PR: "a"},
		{Repository: "sneat-co/zeta", PR: "z-again"},
	})
	if len(merged) != 2 || merged[0].Repository != "sneat-co/alpha" || merged[1].PR != "z" {
		t.Fatalf("merged = %+v, want one row per repository, first observation retained, sorted", merged)
	}
}

// The documented recovery from a hold is "merge the held pull requests, then
// --resume". That only works if a wave parked in awaiting_hold_release resumes
// by observing the release the human's merge published — not by replaying the
// wave and re-running every sibling repository's CI to learn the same thing.
func TestResumeContinuesAHeldWaveOnceItsReleaseIsPublished(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	seed := []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}}
	previous := BumpReport{
		SchemaVersion: 1, Operation: BumpOperationID(seed), Status: "awaiting_hold_release",
		Ecosystem: EcosystemGo, SeedEvents: seed, BaseRef: "main",
		HeldRepositories: []HeldRepository{{Repository: "acme/adapter", PR: "https://github.test/acme/adapter/pull/1"}},
		Waves: []BumpWaveReport{{
			Index: 1, Status: "awaiting_hold_release", Events: seed,
			HeldRepositories: []HeldRepository{{Repository: "acme/adapter", PR: "https://github.test/acme/adapter/pull/1"}},
			Releases: []ReleaseObservation{{
				Module: "example.com/adapter", Repository: "acme/adapter", Before: "v0.4.0",
				Source: "go list -m example.com/adapter@latest", Status: "awaiting_release",
			}},
		}},
	}
	report, err := RunBump(context.Background(), seed, nil, BumpOptions{
		Options:  Options{GitHubDir: t.TempDir(), Ref: "main", Resume: true, Timeout: time.Second},
		Previous: &previous, PollInterval: time.Millisecond,
		LatestGoVersion: func(context.Context, string) (string, error) { return "v0.5.0", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Waves[0].Status != "completed" {
		t.Fatalf("report = %+v, want a held wave to complete once its release arrived", report)
	}
	if report.Waves[0].Releases[0].After != "v0.5.0" {
		t.Fatalf("releases = %+v", report.Waves[0].Releases)
	}
}

// And a resume while the held pull request is still open must keep saying
// exactly that. Degrading to the generic awaiting_release would tell the
// operator to wait for a release that no amount of waiting can produce.
func TestResumeKeepsSayingAHeldWaveIsWaitingOnAHuman(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	seed := []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}}
	previous := BumpReport{
		SchemaVersion: 1, Operation: BumpOperationID(seed), Status: "awaiting_hold_release",
		Ecosystem: EcosystemGo, SeedEvents: seed, BaseRef: "main",
		Waves: []BumpWaveReport{{
			Index: 1, Status: "awaiting_hold_release", Events: seed,
			HeldRepositories: []HeldRepository{{Repository: "acme/adapter", PR: "https://github.test/acme/adapter/pull/1"}},
			Releases: []ReleaseObservation{{
				Module: "example.com/adapter", Repository: "acme/adapter", Before: "v0.4.0",
				Source: "go list -m example.com/adapter@latest", Status: "awaiting_release",
			}},
		}},
	}
	report, err := RunBump(context.Background(), seed, nil, BumpOptions{
		Options:  Options{GitHubDir: t.TempDir(), Ref: "main", Resume: true, Timeout: 20 * time.Millisecond},
		Previous: &previous, PollInterval: time.Millisecond,
		LatestGoVersion: func(context.Context, string) (string, error) { return "v0.4.0", nil },
	})
	if err == nil {
		t.Fatalf("resume without the held merge must not report success: %+v", report)
	}
	if report.Status != "awaiting_hold_release" {
		t.Fatalf("status = %q, want the hold to still be named as the blocker", report.Status)
	}
}

func TestBumpMarkdownExplainsHeldPullRequests(t *testing.T) {
	t.Parallel()
	report := BumpReport{
		SchemaVersion: 1, Operation: "deps-bump-npm-test", Status: "awaiting_hold_release",
		Ecosystem: EcosystemNPM, BaseRef: "main",
		HeldRepositories: []HeldRepository{{
			Repository: "sneat-co/sneat-go", PR: "https://github.com/sneat-co/sneat-go/pull/7",
			Reason: "merge is held for a human decision",
		}},
		Waves: []BumpWaveReport{{
			Index: 1, Status: "awaiting_hold_release",
			HeldRepositories: []HeldRepository{{
				Repository: "sneat-co/sneat-go", PR: "https://github.com/sneat-co/sneat-go/pull/7",
			}},
		}},
	}
	markdown := report.Markdown()
	for _, want := range []string{"Held pull requests", "sneat-co/sneat-go", "pull/7", "waiting on them, not failed"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("markdown missing %q:\n%s", want, markdown)
		}
	}
}
