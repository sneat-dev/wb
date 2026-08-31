package deps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestBumpOperationIDIsIndependentOfEventOrder(t *testing.T) {
	t.Parallel()
	left := []ReleaseEvent{{Dependency: "example.com/b", Version: "v1.2.0"}, {Dependency: "example.com/a", Version: "v0.4.0"}}
	right := []ReleaseEvent{left[1], left[0]}
	if BumpOperationID(left) != BumpOperationID(right) {
		t.Fatalf("operation IDs differ: %s != %s", BumpOperationID(left), BumpOperationID(right))
	}
}

func TestBumpOperationIDForUsesTheEcosystemPrefixAndBumpOperationIDStaysGoOnly(t *testing.T) {
	t.Parallel()
	events := []ReleaseEvent{{Dependency: "@sneat/core", Version: "1.2.3"}}
	goID := BumpOperationIDFor(EcosystemGo, events)
	npmID := BumpOperationIDFor(EcosystemNPM, events)
	if !strings.HasPrefix(goID, "deps-bump-go-") {
		t.Fatalf("go operation id = %q", goID)
	}
	if !strings.HasPrefix(npmID, "deps-bump-npm-") {
		t.Fatalf("npm operation id = %q", npmID)
	}
	if goID == npmID {
		t.Fatalf("go and npm campaigns for the same events must not collide: %s", goID)
	}
	// BumpOperationID (no ecosystem parameter) is the pre-npm public API;
	// every existing caller must keep getting exactly the Go-prefixed id.
	if BumpOperationID(events) != goID {
		t.Fatalf("BumpOperationID(events) = %q, want %q", BumpOperationID(events), goID)
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

func TestGoFleetGraphTreatsSeedProviderAsFixedCampaignRoot(t *testing.T) {
	t.Parallel()
	seed := []ReleaseEvent{{Dependency: "example.com/a", Version: "v0.2.0", Source: "explicit"}}
	events := append(append([]ReleaseEvent(nil), seed...), ReleaseEvent{Dependency: "example.com/b", Version: "v0.2.0", Source: "observed_release"})
	first := goFleetGraph{
		modules: map[string]goFleetModule{
			"example.com/a": {Path: "example.com/a", Repository: "acme/a"},
			"example.com/b": {Path: "example.com/b", Repository: "acme/b"},
		},
		requirements: map[string][]goFleetRequirement{
			"example.com/a": {{Dependency: "example.com/a", Version: "v0.1.0", ConsumerModule: "example.com/b", Repository: "acme/b"}},
			"example.com/b": {
				{Dependency: "example.com/b", Version: "v0.1.0", ConsumerModule: "example.com/a", Repository: "acme/a"},
				{Dependency: "example.com/b", Version: "v0.1.0", ConsumerModule: "example.com/c", Repository: "acme/c"},
			},
		},
	}
	if err := first.validateAcyclicPropagation(seed); err != nil {
		t.Fatal(err)
	}
	targets, deferred := first.coalescedRepositoriesForEvents(seed, events)
	if len(targets) != 1 || len(targets["acme/b"]) != 1 || targets["acme/b"][0].Dependency != "example.com/a" || len(deferred) != 1 || deferred[0] != "acme/c" {
		t.Fatalf("first targets = %+v, deferred = %v", targets, deferred)
	}
	if _, scheduled := targets["acme/a"]; scheduled {
		t.Fatalf("fixed seed provider was scheduled: %+v", targets)
	}

	// After acme/b's release is observed, acme/a still consumes that release
	// but remains fixed; acme/c is the only next-wave target.
	next := first
	next.requirements = map[string][]goFleetRequirement{
		"example.com/a": {{Dependency: "example.com/a", Version: "v0.2.0", ConsumerModule: "example.com/b", Repository: "acme/b"}},
		"example.com/b": first.requirements["example.com/b"],
	}
	if err := next.validateAcyclicPropagation(seed); err != nil {
		t.Fatal(err)
	}
	targets, deferred = next.coalescedRepositoriesForEvents(seed, events)
	if len(targets) != 1 || len(targets["acme/c"]) != 1 || targets["acme/c"][0].Dependency != "example.com/b" || len(deferred) != 0 {
		t.Fatalf("next targets = %+v, deferred = %v", targets, deferred)
	}
	if _, scheduled := targets["acme/a"]; scheduled {
		t.Fatalf("fixed seed provider was rescheduled: %+v", targets)
	}
}

func TestNpmFleetGraphTreatsSeedProviderAsFixedCampaignRoot(t *testing.T) {
	t.Parallel()
	seed := []ReleaseEvent{{Dependency: "@acme/a", Version: "0.2.0", Source: "explicit"}}
	events := append(append([]ReleaseEvent(nil), seed...), ReleaseEvent{Dependency: "@acme/b", Version: "0.2.0", Source: "observed_release"})
	first := npmFleetGraph{
		packages: map[string]npmFleetPackage{
			"@acme/a": {Name: "@acme/a", Repository: "acme/a"},
			"@acme/b": {Name: "@acme/b", Repository: "acme/b"},
		},
		requirements: map[string][]npmFleetRequirement{
			"@acme/a": {{Dependency: "@acme/a", Version: "0.1.0", ConsumerPackage: "@acme/b", Repository: "acme/b"}},
			"@acme/b": {
				{Dependency: "@acme/b", Version: "0.1.0", ConsumerPackage: "@acme/a", Repository: "acme/a"},
				{Dependency: "@acme/b", Version: "0.1.0", ConsumerPackage: "@acme/c", Repository: "acme/c"},
			},
		},
	}
	if err := first.validateAcyclicPropagation(seed); err != nil {
		t.Fatal(err)
	}
	targets, deferred := first.coalescedRepositoriesForEvents(seed, events)
	if len(targets) != 1 || len(targets["acme/b"]) != 1 || targets["acme/b"][0].Dependency != "@acme/a" || len(deferred) != 1 || deferred[0] != "acme/c" {
		t.Fatalf("first targets = %+v, deferred = %v", targets, deferred)
	}
	if _, scheduled := targets["acme/a"]; scheduled {
		t.Fatalf("fixed seed provider was scheduled: %+v", targets)
	}

	next := first
	next.requirements = map[string][]npmFleetRequirement{
		"@acme/a": {{Dependency: "@acme/a", Version: "0.2.0", ConsumerPackage: "@acme/b", Repository: "acme/b"}},
		"@acme/b": first.requirements["@acme/b"],
	}
	if err := next.validateAcyclicPropagation(seed); err != nil {
		t.Fatal(err)
	}
	targets, deferred = next.coalescedRepositoriesForEvents(seed, events)
	if len(targets) != 1 || len(targets["acme/c"]) != 1 || targets["acme/c"][0].Dependency != "@acme/b" || len(deferred) != 0 {
		t.Fatalf("next targets = %+v, deferred = %v", targets, deferred)
	}
	if _, scheduled := targets["acme/a"]; scheduled {
		t.Fatalf("fixed seed provider was rescheduled: %+v", targets)
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

// TestGoGraphDiscoveryFailureSkipsUnreadableCloneRegardlessOfGoManifest pins
// the production bug that motivated this: a 132-repository `wb deps bump go
// --fleet` run aborted outright because one local clone
// (sneat-co/sneat-payments) had no 'origin' remote configured, and
// `git fetch --quiet origin` failed with "fatal: 'origin' does not appear to
// be a git repository". Unlike a ref that is merely unavailable on an
// otherwise healthy clone, an unreadable/remote-less clone cannot be
// inspected at all, so it must be skipped (and reported) even when it
// contains a go.mod — continuing to hard-fail every other repository over
// one clone that needs manual repair helps no one.
func TestGoGraphDiscoveryFailureSkipsUnreadableCloneRegardlessOfGoManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/payments\n\ngo 1.24\n")
	cause := fmt.Errorf("git fetch --quiet origin: exit status 128: fatal: 'origin' does not appear to be a git repository")
	skip, err := classifyGoGraphDiscoveryFailure("sneat-co/sneat-payments", root, cause, goGraphDiscoveryPolicy{SkipFailedNonGo: true})
	if err != nil || skip == nil || skip.Repository != "sneat-co/sneat-payments" {
		t.Fatalf("unreadable clone classification: error=%v skip=%+v", err, skip)
	}
	if !strings.Contains(skip.Reason, "unreadable") {
		t.Fatalf("skip reason = %q, want it to explain the clone is unreadable", skip.Reason)
	}
	// Strict mode (no policy relief at all) must still fail loudly — the
	// unreadable-clone relief is gated behind SkipFailedNonGo exactly like
	// the existing manifest-based relief is.
	skip, err = classifyGoGraphDiscoveryFailure("sneat-co/sneat-payments", root, cause, goGraphDiscoveryPolicy{})
	if err == nil || skip != nil {
		t.Fatalf("strict classification of an unreadable clone: error=%v skip=%+v", err, skip)
	}
}

func TestNpmGraphDiscoveryFailureSkipsUnreadableCloneRegardlessOfPackageJSON(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "package.json"), `{"name": "@sneat/payments", "version": "1.0.0"}`+"\n")
	cause := fmt.Errorf("git fetch --quiet origin: exit status 128: fatal: 'origin' does not appear to be a git repository")
	skip, err := classifyNpmGraphDiscoveryFailure("sneat-co/sneat-payments", root, cause, npmGraphDiscoveryPolicy{SkipFailedNonNPM: true})
	if err != nil || skip == nil || skip.Repository != "sneat-co/sneat-payments" {
		t.Fatalf("unreadable clone classification: error=%v skip=%+v", err, skip)
	}
	if !strings.Contains(skip.Reason, "unreadable") {
		t.Fatalf("skip reason = %q, want it to explain the clone is unreadable", skip.Reason)
	}
	skip, err = classifyNpmGraphDiscoveryFailure("sneat-co/sneat-payments", root, cause, npmGraphDiscoveryPolicy{})
	if err == nil || skip != nil {
		t.Fatalf("strict classification of an unreadable clone: error=%v skip=%+v", err, skip)
	}
}

func TestLooksLikeUnreadableCloneMatchesKnownGitFatalErrorsOnly(t *testing.T) {
	t.Parallel()
	unreadable := []error{
		fmt.Errorf("git fetch --quiet origin: exit status 128: fatal: 'origin' does not appear to be a git repository"),
		fmt.Errorf("git status: exit status 128: fatal: not a git repository (or any of the parent directories): .git"),
		fmt.Errorf("git fetch --quiet origin: exit status 128: fatal: No such remote 'origin'"),
	}
	for _, cause := range unreadable {
		if !looksLikeUnreadableClone(cause) {
			t.Errorf("looksLikeUnreadableClone(%q) = false, want true", cause)
		}
	}
	readable := []error{
		nil,
		errors.New("origin/main is unavailable"),
		fmt.Errorf("acme/consumer does not contain origin/main: exit status 128: fatal: Needed a single revision"),
	}
	for _, cause := range readable {
		if looksLikeUnreadableClone(cause) {
			t.Errorf("looksLikeUnreadableClone(%v) = true, want false", cause)
		}
	}
}

func TestGoFleetGraphRejectsRelevantCrossRepositoryCycle(t *testing.T) {
	t.Parallel()
	graph := goFleetGraph{
		modules: map[string]goFleetModule{
			"example.com/provider": {Path: "example.com/provider", Repository: "acme/provider"},
			"example.com/a":        {Path: "example.com/a", Repository: "acme/a"},
			"example.com/b":        {Path: "example.com/b", Repository: "acme/b"},
		},
		requirements: map[string][]goFleetRequirement{
			"example.com/provider": {{Dependency: "example.com/provider", ConsumerModule: "example.com/a", Repository: "acme/a"}},
			"example.com/a":        {{Dependency: "example.com/a", ConsumerModule: "example.com/b", Repository: "acme/b"}},
			"example.com/b":        {{Dependency: "example.com/b", ConsumerModule: "example.com/a", Repository: "acme/a"}},
		},
	}
	err := graph.validateAcyclicPropagation([]ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0"}})
	if err == nil || !strings.Contains(err.Error(), "acme/a -> acme/b -> acme/a") {
		t.Fatalf("error = %v", err)
	}
}

func TestNpmFleetGraphRejectsRelevantNonSeedCrossRepositoryCycle(t *testing.T) {
	t.Parallel()
	graph := npmFleetGraph{
		packages: map[string]npmFleetPackage{
			"@acme/provider": {Name: "@acme/provider", Repository: "acme/provider"},
			"@acme/a":        {Name: "@acme/a", Repository: "acme/a"},
			"@acme/b":        {Name: "@acme/b", Repository: "acme/b"},
		},
		requirements: map[string][]npmFleetRequirement{
			"@acme/provider": {{Dependency: "@acme/provider", ConsumerPackage: "@acme/a", Repository: "acme/a"}},
			"@acme/a":        {{Dependency: "@acme/a", ConsumerPackage: "@acme/b", Repository: "acme/b"}},
			"@acme/b":        {{Dependency: "@acme/b", ConsumerPackage: "@acme/a", Repository: "acme/a"}},
		},
	}
	err := graph.validateAcyclicPropagation([]ReleaseEvent{{Dependency: "@acme/provider", Version: "0.2.0"}})
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
	// Not t.Parallel(): this test drives a real (non-DryRun) orchestrate.Run,
	// which needs WB_HOME scoped to this test's own temp dir so it can't
	// collide with, or leak into, anything else — and t.Setenv cannot be used
	// safely once a test is parallel, since parallel siblings would then read
	// and overwrite the same process-global env var concurrently.
	t.Setenv(wbhome.EnvOverride, t.TempDir())
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

func TestRunBumpResumesAbandonedRootOperationLock(t *testing.T) {
	// The lock has the exact legacy bytes left after a process dies. RunBump
	// must forward Resume to the root operation lock before it touches the
	// persisted campaign report or starts any wave.
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	githubDir := t.TempDir()
	seed := []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}}
	operation := BumpOperationID(seed)
	home, err := wbhome.EnsureRoot(githubDir)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := worktrees.OpenOperationLockDirectory(filepath.Join(home, "worktrees", operation))
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "worktrees", operation, ".lock"), []byte(fmt.Sprintf("operation=%s\npid=%d\n", operation, killedBumpProcessPID(t))), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := BumpReport{
		SchemaVersion: 1, Operation: operation, Status: "awaiting_release",
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
		Options:  Options{GitHubDir: githubDir, Ref: "main", Resume: true, Timeout: time.Second},
		Previous: &previous, PollInterval: time.Millisecond,
		LatestGoVersion: func(context.Context, string) (string, error) { return "v0.5.0", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Waves[0].Releases[0].After != "v0.5.0" {
		t.Fatalf("report = %+v", report)
	}
}

func killedBumpProcessPID(t *testing.T) int {
	t.Helper()
	process := exec.Command("sh", "-c", "sleep 60")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	pid := process.Process.Pid
	if err := process.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err == nil {
		t.Fatal("killed child unexpectedly exited without a signal")
	}
	return pid
}

func TestRunBumpAllowsFixpointScanAfterMaxMutationWave(t *testing.T) {
	// Not t.Parallel(): this test drives a real (non-DryRun) orchestrate.Run,
	// which needs WB_HOME scoped to this test's own temp dir so it can't
	// collide with, or leak into, anything else — and t.Setenv cannot be used
	// safely once a test is parallel, since parallel siblings would then read
	// and overwrite the same process-global env var concurrently.
	t.Setenv(wbhome.EnvOverride, t.TempDir())
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

// TestRunBumpSurvivesUnreadableCloneAcrossFleet reproduces the exact
// production incident that motivated this fix: `wb deps bump go --fleet`
// aborted a 132-repository campaign because one local clone had no 'origin'
// remote configured. The other repositories in the fleet must still be
// planned; the broken one must show up as a discovery skip, not silently
// vanish.
func TestRunBumpSurvivesUnreadableCloneAcrossFleet(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	adapter := newBumpRepository(t, root, githubDir, "adapter", "module example.com/adapter\n\ngo 1.24\n\nrequire example.com/provider v0.1.0\n")
	provider := newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n")
	brokenCanonical := filepath.Join(githubDir, "acme", "payments")
	writeTestFile(t, filepath.Join(brokenCanonical, "go.mod"), "module example.com/payments\n\ngo 1.24\n")
	runTestGit(t, brokenCanonical, "init", "-b", "main")
	runTestGit(t, brokenCanonical, "config", "user.name", "WB Test")
	runTestGit(t, brokenCanonical, "config", "user.email", "wb@example.test")
	runTestGit(t, brokenCanonical, "add", "-A")
	runTestGit(t, brokenCanonical, "commit", "-m", "initial")
	// Deliberately no 'origin' remote configured on acme/payments.

	report, err := RunBump(context.Background(), []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}},
		[]Repository{provider, adapter, {Slug: "acme/payments", Path: brokenCanonical}},
		BumpOptions{Options: Options{GitHubDir: githubDir, Ref: "main", Parallel: 2, DryRun: true}},
	)
	if err != nil {
		t.Fatalf("one unreadable clone must not abort the whole fleet bump: %v", err)
	}
	if report.Status != "planned" || len(report.Waves) != 1 || len(report.Waves[0].Repositories) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if repository := report.Waves[0].Repositories[0]; repository.Repository != "acme/adapter" || repository.Status != "planned" {
		t.Fatalf("wave repository = %+v", repository)
	}
	if len(report.DiscoverySkips) != 1 || report.DiscoverySkips[0].Repository != "acme/payments" {
		t.Fatalf("discovery skips = %+v, want acme/payments skipped and reported", report.DiscoverySkips)
	}
	if !strings.Contains(report.DiscoverySkips[0].Reason, "unreadable") {
		t.Fatalf("skip reason = %q, want it to explain the clone is unreadable", report.DiscoverySkips[0].Reason)
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

// seedBumpRemoteClone nests its bare remote as <root>/remotes/<owner>/
// <name>.git, so remoteOriginSlug's generic (non-github.com) URL fallback
// resolves it to exactly "<owner>/<name>" from the local path alone, the
// same way a real github.com/<owner>/<name> remote would resolve. It is
// cloned into <githubDir>/<canonicalOwner>/<canonicalName>, which may differ
// from owner/name to simulate a repository whose local directory no longer
// matches its own remote identity (a stale duplicate clone left behind by
// an org move or rename) — or, when they match, a correctly located one.
// Two calls sharing owner/name reuse the identical underlying remote,
// exactly modeling one physical repository cloned into two directories.
func seedBumpRemoteClone(t *testing.T, root, githubDir, owner, name, canonicalOwner, canonicalName string, files map[string]string) Repository {
	t.Helper()
	remote := filepath.Join(root, "remotes", owner, name+".git")
	if _, err := os.Stat(remote); os.IsNotExist(err) {
		seed := filepath.Join(root, "seed-"+owner+"-"+name)
		for path, body := range files {
			writeTestFile(t, filepath.Join(seed, path), body)
		}
		runTestGit(t, seed, "init", "-b", "main")
		runTestGit(t, seed, "config", "user.name", "WB Test")
		runTestGit(t, seed, "config", "user.email", "wb@example.test")
		runTestGit(t, seed, "add", "-A")
		runTestGit(t, seed, "commit", "-m", "initial")
		if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
			t.Fatal(err)
		}
		runTestGit(t, root, "clone", "--bare", seed, remote)
	}
	canonical := filepath.Join(githubDir, canonicalOwner, canonicalName)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "clone", remote, canonical)
	return Repository{Slug: canonicalOwner + "/" + canonicalName, Path: canonical, CloneURL: remote}
}

// TestRunBumpResolvesStaleDuplicateCloneModuleAmbiguity is the Defect D
// regression: production hit `go module github.com/sneat-co/chatwright is
// declared by chatwright/cloud:go.mod, sneat-co/chatwright:go.mod` (and the
// same shape for sneat-co/preferans and sneat-games/chessraiders/go) because
// an org move/rename left a stale duplicate local clone behind, and ANY
// ambiguous module declaration anywhere in the fleet aborted the ENTIRE
// bump — including this one, whose seed events (example.com/provider) have
// nothing to do with the ambiguous module at all. Exactly one declaring
// repository here is self-consistent with its own origin remote (the
// acme/widgets vs. old-org/widgets-copy production shape), so the conflict
// is resolved and recorded as a warning instead of aborting.
func TestRunBumpResolvesStaleDuplicateCloneModuleAmbiguity(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n"),
		newBumpRepository(t, root, githubDir, "consumer", "module example.com/consumer\n\ngo 1.24\n\nrequire example.com/provider v0.1.0\n"),
	}
	files := map[string]string{"go.mod": "module example.com/mycompany/widgets\n\ngo 1.24\n"}
	repositories = append(repositories,
		seedBumpRemoteClone(t, root, githubDir, "acme", "widgets", "acme", "widgets", files),
		seedBumpRemoteClone(t, root, githubDir, "acme", "widgets", "old-org", "widgets-copy", files),
	)

	report, err := RunBump(context.Background(), []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}}, repositories, BumpOptions{
		Options: Options{GitHubDir: githubDir, Ref: "main", Parallel: 2, DryRun: true},
	})
	if err != nil {
		t.Fatalf("a resolvable duplicate module declaration elsewhere in the fleet must not abort an unrelated bump: %v", err)
	}
	if report.Status != "planned" {
		t.Fatalf("report = %+v", report)
	}
	if len(report.AmbiguousModules) != 1 {
		t.Fatalf("ambiguous modules = %+v, want exactly one", report.AmbiguousModules)
	}
	warning := report.AmbiguousModules[0]
	if warning.Module != "example.com/mycompany/widgets" || warning.Repository != "acme/widgets" {
		t.Fatalf("ambiguous module warning = %+v", warning)
	}
	if len(warning.Duplicates) != 1 || warning.Duplicates[0] != "old-org/widgets-copy:go.mod" {
		t.Fatalf("duplicates = %+v, want old-org/widgets-copy:go.mod", warning.Duplicates)
	}
	if !strings.Contains(report.Markdown(), "example.com/mycompany/widgets") {
		t.Fatalf("markdown is missing the ambiguous module resolution:\n%s", report.Markdown())
	}
}

// TestRunBumpFailsForGenuinelyUnrelatedModuleCollision pins the floor: two
// independently self-consistent repositories — each correctly named for its
// own distinct remote — that coincidentally declare the same (non-
// github.com) module path are NOT a stale-duplicate-clone pattern, and this
// must still abort the bump rather than guess a resolution.
func TestRunBumpFailsForGenuinelyUnrelatedModuleCollision(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n"),
		newBumpRepository(t, root, githubDir, "consumer", "module example.com/consumer\n\ngo 1.24\n\nrequire example.com/provider v0.1.0\n"),
	}
	files := map[string]string{"go.mod": "module example.com/shared/lib\n\ngo 1.24\n"}
	repositories = append(repositories,
		seedBumpRemoteClone(t, root, githubDir, "vendor-a", "lib", "vendor-a", "lib", files),
		seedBumpRemoteClone(t, root, githubDir, "vendor-b", "lib", "vendor-b", "lib", files),
	)

	_, err := RunBump(context.Background(), []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}}, repositories, BumpOptions{
		Options: Options{GitHubDir: githubDir, Ref: "main", Parallel: 2, DryRun: true},
	})
	if err == nil {
		t.Fatal("a genuinely unrelated module-path collision elsewhere in the fleet must still abort the bump")
	}
	if !strings.Contains(err.Error(), "vendor-a/lib") || !strings.Contains(err.Error(), "vendor-b/lib") {
		t.Fatalf("error must name both repositories; got %v", err)
	}
}
