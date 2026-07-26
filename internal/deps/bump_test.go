package deps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBumpOperationIDIsIndependentOfEventOrder(t *testing.T) {
	t.Parallel()
	left := []ReleaseEvent{{Dependency: "example.com/b", Version: "v1.2.0"}, {Dependency: "example.com/a", Version: "v0.4.0"}}
	right := []ReleaseEvent{left[1], left[0]}
	if BumpOperationID(left) != BumpOperationID(right) {
		t.Fatalf("operation IDs differ: %s != %s", BumpOperationID(left), BumpOperationID(right))
	}
}

func TestWaitForGoReleaseRequiresVersionNewerThanBaseline(t *testing.T) {
	t.Parallel()
	versions := []string{"v1.2.0", "v1.3.0"}
	index := 0
	observation, err := waitForGoRelease(context.Background(), ReleaseObservation{
		Module: "example.com/provider", Repository: "acme/provider", Before: "v1.2.0",
	}, BumpOptions{
		Options: Options{Timeout: time.Second}, PollInterval: time.Millisecond,
		LatestGoVersion: func(context.Context, string) (string, error) {
			version := versions[index]
			if index < len(versions)-1 {
				index++
			}
			return version, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Status != "released" || observation.After != "v1.3.0" || index != 1 {
		t.Fatalf("observation = %+v, index=%d", observation, index)
	}
}

func TestRunBumpDryRunPlansOnlyDirectConsumers(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n"),
		newBumpRepository(t, root, githubDir, "adapter", "module example.com/adapter\n\ngo 1.24\n\nrequire example.com/provider v0.1.0\n"),
		newBumpRepository(t, root, githubDir, "consumer", "module example.com/consumer\n\ngo 1.24\n\nrequire example.com/adapter v0.1.0\n"),
	}
	report, err := RunBump(context.Background(), []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}}, repositories, BumpOptions{
		Options: Options{GitHubDir: githubDir, Ref: "main", Parallel: 2, DryRun: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "planned" || len(report.Waves) != 1 || len(report.Waves[0].Repositories) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if repository := report.Waves[0].Repositories[0]; repository.Repository != "acme/adapter" || repository.Status != "planned" {
		t.Fatalf("wave repository = %+v", repository)
	}
	if markdown := report.Markdown(); !strings.Contains(markdown, "existing Go requirement will be set with official Go tooling") {
		t.Fatalf("dry-run decisions are missing from Markdown:\n%s", markdown)
	}
}

func TestRunBumpPersistsGraphDiscoveryProgress(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n"),
		newBumpRepository(t, root, githubDir, "adapter", "module example.com/adapter\n\ngo 1.24\n\nrequire example.com/provider v0.1.0\n"),
		newBumpRepository(t, root, githubDir, "consumer", "module example.com/consumer\n\ngo 1.24\n\nrequire example.com/adapter v0.1.0\n"),
	}
	var persisted []BumpReport
	_, err := RunBump(context.Background(), []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}}, repositories, BumpOptions{
		Options: Options{GitHubDir: githubDir, Ref: "main", Parallel: 1, DryRun: true},
		Persist: func(report BumpReport) error {
			persisted = append(persisted, report)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var started, completed, processing bool
	for _, report := range persisted {
		switch {
		case report.Phase == BumpPhaseDiscoveringGraph && report.Progress.RepositoriesTotal == len(repositories) && report.Progress.RepositoriesCompleted == 0:
			started = true
		case report.Phase == BumpPhaseDiscoveringGraph && report.Progress.RepositoriesTotal == len(repositories) && report.Progress.RepositoriesCompleted == len(repositories) && report.Progress.LastRepository != "":
			completed = true
		case report.Phase == BumpPhaseProcessingWave && report.Progress.Wave == 1 && report.Progress.RepositoriesTotal == 1:
			processing = true
		}
	}
	if !started || !completed || !processing {
		t.Fatalf("persisted phases do not show discovery and processing progress: %+v", persisted)
	}
}

func TestRunBumpSecondSweepTraversesExistingPublishedConsumer(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n"),
		newBumpRepository(t, root, githubDir, "adapter", "module example.com/adapter\n\ngo 1.24\n\nrequire example.com/provider v0.2.0\n"),
		newBumpRepository(t, root, githubDir, "consumer", "module example.com/consumer\n\ngo 1.24\n\nrequire example.com/adapter v0.1.0\n"),
	}
	report, err := RunBump(context.Background(), []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}}, repositories, BumpOptions{
		Options: Options{GitHubDir: githubDir, Ref: "main", Parallel: 2, DryRun: true},
		LatestGoRelease: func(_ context.Context, module string) (PublishedGoRelease, error) {
			if module != "example.com/adapter" {
				t.Fatalf("unexpected release lookup for %s", module)
			}
			return PublishedGoRelease{
				Version: "v0.2.1", Requirements: map[string]string{"example.com/provider": "v0.2.0"},
				Source: "test registry",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "planned" || len(report.Waves) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if release := report.Waves[0].Releases[0]; release.Module != "example.com/adapter" || release.After != "v0.2.1" || release.Status != "released" {
		t.Fatalf("existing release = %+v", release)
	}
	if repository := report.Waves[0].Repositories[0]; repository.Repository != "acme/consumer" || repository.Status != "planned" {
		t.Fatalf("downstream repository = %+v", repository)
	}
}

func TestRunBumpDefersDiamondSinkToAvoidDuplicateCI(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n"),
		newBumpRepository(t, root, githubDir, "bots", "module example.com/bots\n\ngo 1.24\n\nrequire example.com/provider v0.1.0\n"),
		newBumpRepository(t, root, githubDir, "go", "module example.com/go\n\ngo 1.24\n\nrequire (\n\texample.com/provider v0.1.0\n\texample.com/bots v0.1.0\n)\n"),
	}
	report, err := RunBump(context.Background(), []ReleaseEvent{{
		Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit",
	}}, repositories, BumpOptions{
		Options: Options{GitHubDir: githubDir, Ref: "main", Parallel: 2, DryRun: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	wave := report.Waves[0]
	if len(wave.Repositories) != 1 || wave.Repositories[0].Repository != "acme/bots" {
		t.Fatalf("first wave repositories = %+v", wave.Repositories)
	}
	if len(wave.DeferredRepositories) != 1 || wave.DeferredRepositories[0] != "acme/go" {
		t.Fatalf("deferred repositories = %v", wave.DeferredRepositories)
	}
	if markdown := report.Markdown(); !strings.Contains(markdown, "No worktree or CI run was started") {
		t.Fatalf("coalescing decision is missing from Markdown:\n%s", markdown)
	}
}

func TestGoFleetGraphCoalescesAllReleasesIntoDiamondSink(t *testing.T) {
	t.Parallel()
	graph := goFleetGraph{
		modules: map[string]goFleetModule{
			"example.com/provider": {Path: "example.com/provider", Repository: "acme/provider"},
			"example.com/bots":     {Path: "example.com/bots", Repository: "acme/bots"},
			"example.com/go":       {Path: "example.com/go", Repository: "acme/go"},
		},
		requirements: map[string][]goFleetRequirement{
			"example.com/provider": {
				{Dependency: "example.com/provider", Version: "v0.2.0", ConsumerModule: "example.com/bots", Repository: "acme/bots"},
				{Dependency: "example.com/provider", Version: "v0.1.0", ConsumerModule: "example.com/go", Repository: "acme/go"},
			},
			"example.com/bots": {
				{Dependency: "example.com/bots", Version: "v0.1.0", ConsumerModule: "example.com/go", Repository: "acme/go"},
			},
		},
	}
	seed := []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0"}}
	active := append(append([]ReleaseEvent(nil), seed...), ReleaseEvent{Dependency: "example.com/bots", Version: "v0.3.0"})
	targets, deferred := graph.coalescedRepositoriesForEvents(seed, active)
	if len(deferred) != 0 || len(targets) != 1 || len(targets["acme/go"]) != 2 {
		t.Fatalf("targets = %+v, deferred = %v", targets, deferred)
	}
	if targets["acme/go"][0].Dependency != "example.com/bots" || targets["acme/go"][1].Dependency != "example.com/provider" {
		t.Fatalf("aggregate targets = %+v", targets["acme/go"])
	}
}

func TestRefreshStaleReleaseEventsUsesNewestVersionBeforeCI(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	calls := 0
	events, refreshes, err := refreshStaleReleaseEvents(context.Background(), []ReleaseEvent{
		{Dependency: "example.com/stale", Version: "v1.2.0", Source: "observed_release", CheckedAt: now.Add(-10 * time.Minute)},
		{Dependency: "example.com/fresh", Version: "v2.0.0", Source: "observed_release", CheckedAt: now.Add(-time.Minute)},
	}, BumpOptions{
		RefreshAfter: 5 * time.Minute,
		Now:          func() time.Time { return now },
		LatestGoVersion: func(_ context.Context, module string) (string, error) {
			calls++
			if module != "example.com/stale" {
				t.Fatalf("unexpected refresh for %s", module)
			}
			return "v1.3.0", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(refreshes) != 1 {
		t.Fatalf("calls = %d, refreshes = %+v", calls, refreshes)
	}
	if events[1].Dependency != "example.com/stale" || events[1].Version != "v1.3.0" || events[1].Source != "refreshed_latest" {
		t.Fatalf("events = %+v", events)
	}
	if refreshes[0].Before != "v1.2.0" || refreshes[0].After != "v1.3.0" || !strings.Contains(refreshes[0].Reason, "before downstream") {
		t.Fatalf("refresh = %+v", refreshes[0])
	}
}

func TestGoGraphDiscoveryFailureSkipsOnlyProvenNonGoRepository(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "package.json"), "{}\n")
	cause := errors.New("origin/main is unavailable")
	skip, err := classifyGoGraphDiscoveryFailure("acme/website", root, cause, goGraphDiscoveryPolicy{SkipFailedNonGo: true})
	if err != nil || skip == nil || skip.Repository != "acme/website" {
		t.Fatalf("non-Go classification: error=%v skip=%+v", err, skip)
	}
	skip, err = classifyGoGraphDiscoveryFailure("acme/website", root, cause, goGraphDiscoveryPolicy{})
	if err == nil || skip != nil {
		t.Fatalf("strict graph classification: error=%v skip=%+v", err, skip)
	}
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/relevant\n\ngo 1.24\n")
	skip, err = classifyGoGraphDiscoveryFailure("acme/service", root, cause, goGraphDiscoveryPolicy{SkipFailedNonGo: true})
	if err == nil || skip != nil || !strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("Go classification: error=%v skip=%+v", err, skip)
	}
}

func TestGoFleetGraphRejectsRelevantCrossRepositoryCycle(t *testing.T) {
	t.Parallel()
	graph := goFleetGraph{
		modules: map[string]goFleetModule{
			"example.com/a": {Path: "example.com/a", Repository: "acme/a"},
			"example.com/b": {Path: "example.com/b", Repository: "acme/b"},
		},
		requirements: map[string][]goFleetRequirement{
			"example.com/a": {{Dependency: "example.com/a", ConsumerModule: "example.com/b", Repository: "acme/b"}},
			"example.com/b": {{Dependency: "example.com/b", ConsumerModule: "example.com/a", Repository: "acme/a"}},
		},
	}
	err := graph.validateAcyclicPropagation([]ReleaseEvent{{Dependency: "example.com/a", Version: "v0.2.0"}})
	if err == nil || !strings.Contains(err.Error(), "acme/a -> acme/b -> acme/a") {
		t.Fatalf("error = %v", err)
	}
}

func TestGoFleetGraphIgnoresUnrelatedCycle(t *testing.T) {
	t.Parallel()
	graph := goFleetGraph{
		modules: map[string]goFleetModule{
			"example.com/provider": {Path: "example.com/provider", Repository: "acme/provider"},
			"example.com/app":      {Path: "example.com/app", Repository: "acme/app"},
			"example.com/a":        {Path: "example.com/a", Repository: "acme/a"},
			"example.com/b":        {Path: "example.com/b", Repository: "acme/b"},
		},
		requirements: map[string][]goFleetRequirement{
			"example.com/provider": {{Dependency: "example.com/provider", ConsumerModule: "example.com/app", Repository: "acme/app"}},
			"example.com/a":        {{Dependency: "example.com/a", ConsumerModule: "example.com/b", Repository: "acme/b"}},
			"example.com/b":        {{Dependency: "example.com/b", ConsumerModule: "example.com/a", Repository: "acme/a"}},
		},
	}
	if err := graph.validateAcyclicPropagation([]ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0"}}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateGoWaveSelectionsDetectsLaterTargetConflict(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	model := filepath.Join(t.TempDir(), "model")
	writeTestFile(t, filepath.Join(model, "go.mod"), "module example.com/model\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(worktree, "go.mod"), "module example.com/app\n\ngo 1.24\n\nrequire example.com/model v0.3.0\n\nreplace example.com/model => "+filepath.ToSlash(model)+"\n")
	decisions := []Decision{{
		Dependency: "example.com/model", File: "go.mod", TargetVersion: "v0.2.0",
		AfterVersion: "v0.2.0", Action: "updated", Reason: "individual target passed",
	}}
	err := validateGoWaveSelections(context.Background(), worktree, decisions, Options{Timeout: time.Second})
	if err == nil || decisions[0].Action != "failed" || decisions[0].AfterVersion != "v0.3.0" {
		t.Fatalf("decisions = %+v, error = %v", decisions, err)
	}
}

func TestRunBumpResumesPersistedReleaseBaseline(t *testing.T) {
	t.Parallel()
	seed := []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}}
	previous := BumpReport{
		SchemaVersion: 1, Operation: BumpOperationID(seed), Status: "awaiting_release",
		Ecosystem: EcosystemGo, SeedEvents: seed, BaseRef: "main",
		Waves: []BumpWaveReport{{
			Index: 1, Status: "awaiting_release", Events: seed,
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
	if report.Status != "completed" || report.Waves[0].Status != "completed" || report.Waves[0].Releases[0].After != "v0.5.0" {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunBumpAllowsFixpointScanAfterMaxMutationWave(t *testing.T) {
	t.Parallel()
	seed := []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}}
	previous := BumpReport{
		SchemaVersion: 1, Operation: BumpOperationID(seed), Status: "running",
		Ecosystem: EcosystemGo, SeedEvents: seed, BaseRef: "main",
		Waves: []BumpWaveReport{{Index: 1, Status: "completed", Events: seed}},
	}
	report, err := RunBump(context.Background(), seed, nil, BumpOptions{
		Options:  Options{GitHubDir: t.TempDir(), Ref: "main", Resume: true},
		Previous: &previous, MaxWaves: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Phase != BumpPhaseCompleted {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunBumpReturnsPersistenceFailureBeforeDiscovery(t *testing.T) {
	t.Parallel()
	want := errors.New("disk full")
	_, err := RunBump(context.Background(), []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0"}}, nil, BumpOptions{
		Options: Options{GitHubDir: t.TempDir(), DryRun: true},
		Persist: func(BumpReport) error { return want },
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestRunBumpResumeRequiresPersistedReport(t *testing.T) {
	t.Parallel()
	_, err := RunBump(context.Background(), []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0"}}, nil, BumpOptions{
		Options: Options{GitHubDir: t.TempDir(), Resume: true},
	})
	if err == nil || !strings.Contains(err.Error(), "persisted deps-bump.yaml") {
		t.Fatalf("error = %v", err)
	}
}

func TestBumpReportRoundTrip(t *testing.T) {
	t.Parallel()
	report := BumpReport{
		SchemaVersion: 1, Operation: "deps-bump-go-test", Status: "awaiting_release", Phase: BumpPhaseAwaitingRelease, Ecosystem: EcosystemGo,
		SeedEvents: []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}},
		BaseRef:    "main", Progress: BumpProgress{Wave: 1, RepositoriesTotal: 3, RepositoriesCompleted: 2, LastRepository: "acme/adapter"},
		DiscoverySkips: []GraphDiscoverySkip{{Repository: "acme/website", Reason: "no go.mod"}},
		Waves: []BumpWaveReport{{
			Index: 1, Status: "awaiting_release",
			DeferredRepositories: []string{"acme/app"},
			Refreshes: []ReleaseEventRefresh{{
				Dependency: "example.com/provider", Before: "v0.2.0", After: "v0.3.0", CheckedAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
				Reason: "newer release substituted before downstream worktree and CI creation",
			}},
			Releases: []ReleaseObservation{{Module: "example.com/adapter", Before: "v0.4.0", Status: "awaiting_release"}},
		}},
	}
	directory := t.TempDir()
	if err := WriteBumpReports(directory, report); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadBumpReport(directory)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Operation != report.Operation || loaded.Phase != BumpPhaseAwaitingRelease || loaded.Progress.RepositoriesCompleted != 2 || loaded.Waves[0].Releases[0].Before != "v0.4.0" {
		t.Fatalf("loaded report = %+v", loaded)
	}
	markdown, err := os.ReadFile(filepath.Join(directory, "deps-bump.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), "Release evidence") || !strings.Contains(string(markdown), "example.com/adapter") ||
		!strings.Contains(string(markdown), "Phase: `awaiting_release`") || !strings.Contains(string(markdown), "repositories `2/3`") ||
		!strings.Contains(string(markdown), "Skipped discovery failures") || !strings.Contains(string(markdown), "Stale-event registry checks") ||
		!strings.Contains(string(markdown), "Deferred to coalesce releases") {
		t.Fatalf("unexpected Markdown:\n%s", markdown)
	}
}

func newBumpRepository(t *testing.T, root, githubDir, name, goMod string) Repository {
	t.Helper()
	seed := filepath.Join(root, name+"-seed")
	remote := filepath.Join(root, name+".git")
	canonical := filepath.Join(githubDir, "acme", name)
	writeTestFile(t, filepath.Join(seed, "go.mod"), goMod)
	writeTestFile(t, filepath.Join(seed, name+".go"), "package "+strings.ReplaceAll(name, "-", "_")+"\n")
	runTestGit(t, seed, "init", "-b", "main")
	runTestGit(t, seed, "config", "user.name", "WB Test")
	runTestGit(t, seed, "config", "user.email", "wb@example.test")
	runTestGit(t, seed, "add", "-A")
	runTestGit(t, seed, "commit", "-m", "initial")
	runTestGit(t, root, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "clone", remote, canonical)
	return Repository{Slug: "acme/" + name, Path: canonical, CloneURL: remote}
}
