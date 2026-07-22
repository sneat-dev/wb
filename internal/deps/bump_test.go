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
	if report.Status != "planned" || len(report.Waves) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if release := report.Waves[0].Releases[0]; release.Module != "example.com/adapter" || release.After != "v0.2.1" || release.Status != "released" {
		t.Fatalf("existing release = %+v", release)
	}
	if repository := report.Waves[1].Repositories[0]; repository.Repository != "acme/consumer" || repository.Status != "planned" {
		t.Fatalf("downstream repository = %+v", repository)
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
		BaseRef:    "main", Progress: BumpProgress{Wave: 1, RepositoriesTotal: 3, RepositoriesCompleted: 2, LastRepository: "acme/adapter"}, Waves: []BumpWaveReport{{
			Index: 1, Status: "awaiting_release",
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
	if !strings.Contains(string(markdown), "Release evidence") || !strings.Contains(string(markdown), "example.com/adapter") || !strings.Contains(string(markdown), "Phase: `awaiting_release`") || !strings.Contains(string(markdown), "repositories `2/3`") {
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
