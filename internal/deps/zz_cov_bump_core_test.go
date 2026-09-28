package deps

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// This file raises statement coverage of bump.go lines 1-760 with
// behaviour-asserting tests only: every case below pins a returned value, an
// error message, a persisted report field, or an exact command effect.

// -- shared fixtures ------------------------------------------------------

func depsCovSeedEvents() []ReleaseEvent {
	return []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}}
}

func depsCovGoDryRunFleet(t *testing.T) (string, []Repository) {
	t.Helper()
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	return githubDir, []Repository{
		newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n"),
		newBumpRepository(t, root, githubDir, "adapter", "module example.com/adapter\n\ngo 1.24\n\nrequire example.com/provider v0.1.0\n"),
	}
}

func depsCovDryRunBumpOptions(githubDir string) BumpOptions {
	return BumpOptions{Options: Options{GitHubDir: githubDir, Ref: "main", Parallel: 1, ParallelExplicit: true, DryRun: true}}
}

func depsCovEmptyReport(seed []ReleaseEvent) BumpReport {
	return BumpReport{
		SchemaVersion: 1, Operation: BumpOperationID(seed), Status: "running",
		Ecosystem: EcosystemGo, SeedEvents: seed, BaseRef: "main",
	}
}

// depsCovPersist records every report handed to BumpOptions.Persist and can
// fail on a predicate, so a test can assert that a persistence failure at one
// exact campaign stage is surfaced instead of swallowed.
type depsCovPersist struct {
	calls    int
	reports  []BumpReport
	failIf   func(BumpReport) bool
	sentinel error
}

func (recorder *depsCovPersist) persist(report BumpReport) error {
	recorder.calls++
	recorder.reports = append(recorder.reports, report)
	if recorder.failIf != nil && recorder.failIf(report) {
		return recorder.sentinel
	}
	return nil
}

// -- ValidateBumpOptions / normalizeBumpOptions ---------------------------

func TestDepsCovBumpCoreValidateBumpOptionsRefusesEveryInvalidContract(t *testing.T) {
	t.Parallel()
	githubDir := t.TempDir()
	valid := depsCovSeedEvents()
	previous := BumpReport{
		SchemaVersion: 1, Operation: BumpOperationID(valid), Status: "running",
		Ecosystem: EcosystemGo, SeedEvents: valid, BaseRef: "main",
	}
	cases := []struct {
		name    string
		options BumpOptions
		events  []ReleaseEvent
		want    string
	}{
		{
			name:    "no events",
			options: BumpOptions{Options: Options{GitHubDir: githubDir}},
			want:    "at least one --changed module@version event is required",
		},
		{
			// normalizeBumpOptions mutates its events slice in place
			// (reusing the backing array via events[:0]), so every case
			// below that reaches that mutation needs its own freshly
			// allocated slice from depsCovSeedEvents() rather than the
			// shared valid: parallel subtests sharing one backing array
			// raced on it under -race once #646 made this test's subtests
			// parallel (WARNING: DATA RACE, CI run 35995162478).
			name:    "unsupported ecosystem",
			options: BumpOptions{Ecosystem: "python", Options: Options{GitHubDir: githubDir}},
			events:  depsCovSeedEvents(),
			want:    `unsupported dependency ecosystem "python"`,
		},
		{
			name:    "invalid go module path",
			options: BumpOptions{Options: Options{GitHubDir: githubDir}},
			events:  []ReleaseEvent{{Dependency: "Example.com/Bad", Version: "v1.0.0"}},
			want:    "invalid Go module",
		},
		{
			name:    "invalid go version",
			options: BumpOptions{Options: Options{GitHubDir: githubDir}},
			events:  []ReleaseEvent{{Dependency: "example.com/provider", Version: "latest"}},
			want:    `invalid go version "latest" for example.com/provider`,
		},
		{
			name:    "conflicting versions",
			options: BumpOptions{Options: Options{GitHubDir: githubDir}},
			events: []ReleaseEvent{
				{Dependency: "example.com/provider", Version: "v1.0.0"},
				{Dependency: "example.com/provider", Version: "v2.0.0"},
			},
			want: "conflicting changed versions for example.com/provider: v1.0.0 and v2.0.0",
		},
		{
			name:    "negative max waves",
			options: BumpOptions{MaxWaves: -1, Options: Options{GitHubDir: githubDir}},
			events:  depsCovSeedEvents(),
			want:    "max waves must be at least 1",
		},
		{
			name:    "negative poll interval",
			options: BumpOptions{PollInterval: -time.Second, Options: Options{GitHubDir: githubDir}},
			events:  depsCovSeedEvents(),
			want:    "release poll interval must not be negative",
		},
		{
			name:    "negative refresh interval",
			options: BumpOptions{RefreshAfter: -time.Second, Options: Options{GitHubDir: githubDir}},
			events:  depsCovSeedEvents(),
			want:    "release refresh interval must not be negative",
		},
		{
			name:    "missing github directory",
			options: BumpOptions{},
			events:  depsCovSeedEvents(),
			want:    "GitHub directory is required",
		},
		{
			name:    "resume without a persisted report",
			options: BumpOptions{Options: Options{GitHubDir: githubDir, Resume: true}},
			events:  depsCovSeedEvents(),
			want:    "--resume requires the persisted deps-bump.yaml report",
		},
		{
			name:    "previous report without resume",
			options: BumpOptions{Previous: &previous, Options: Options{GitHubDir: githubDir}},
			events:  depsCovSeedEvents(),
			want:    "a previous bump report requires --resume",
		},
		{
			name:    "invalid npm package",
			options: BumpOptions{Ecosystem: EcosystemNPM, Options: Options{GitHubDir: githubDir}},
			events:  []ReleaseEvent{{Dependency: "Not Scoped", Version: "1.0.0"}},
			want:    "invalid npm package",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateBumpOptions(testCase.options, testCase.events)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("ValidateBumpOptions error = %v, want it to contain %q", err, testCase.want)
			}
		})
	}
	if err := ValidateBumpOptions(BumpOptions{Options: Options{GitHubDir: githubDir}}, valid); err != nil {
		t.Fatalf("ValidateBumpOptions rejected a valid contract: %v", err)
	}
}

func TestDepsCovBumpCoreNormalizeBumpOptionsAppliesDefaultsAndReconcilesEvents(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	options := BumpOptions{Options: Options{GitHubDir: t.TempDir()}, Now: func() time.Time { return now }}
	normalized, lifecycle, events, err := normalizeBumpOptions(options, []ReleaseEvent{
		{Dependency: "  example.com/b  ", Version: " v1.0.0 ", CheckedAt: now.Add(-time.Hour)},
		{Dependency: "example.com/a", Version: "v2.0.0"},
		{Dependency: "example.com/b", Version: "v1.0.0", CheckedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Ecosystem != EcosystemGo {
		t.Fatalf("default ecosystem = %q, want go", normalized.Ecosystem)
	}
	if normalized.MaxWaves != 20 {
		t.Fatalf("default MaxWaves = %d, want 20", normalized.MaxWaves)
	}
	if normalized.PollInterval != 30*time.Second {
		t.Fatalf("default PollInterval = %s, want 30s", normalized.PollInterval)
	}
	if normalized.RefreshAfter != 0 {
		t.Fatalf("default RefreshAfter = %s, want 0", normalized.RefreshAfter)
	}
	if len(events) != 2 || events[0].Dependency != "example.com/a" || events[1].Dependency != "example.com/b" {
		t.Fatalf("normalized events = %+v, want sorted and deduplicated by dependency", events)
	}
	if events[0].Source != "explicit" || !events[0].CheckedAt.Equal(now) {
		t.Fatalf("events[0] = %+v, want the explicit source and the injected clock", events[0])
	}
	if events[1].Version != "v1.0.0" || !events[1].CheckedAt.Equal(now) {
		t.Fatalf("events[1] = %+v, want the newest observation to win for a duplicate dependency", events[1])
	}
	if lifecycle.Operation != BumpOperationIDFor(EcosystemGo, events) {
		t.Fatalf("lifecycle operation = %q, want the ecosystem-qualified campaign id", lifecycle.Operation)
	}
	if lifecycle.Branch != "wb/deps/bump-"+strings.TrimPrefix(lifecycle.Operation, "deps-bump-go-") {
		t.Fatalf("lifecycle branch = %q", lifecycle.Branch)
	}
	if lifecycle.Ref != "main" || lifecycle.Parallel != 1 {
		t.Fatalf("lifecycle defaults = ref %q parallel %d, want main and 1", lifecycle.Ref, lifecycle.Parallel)
	}

	npmOptions := BumpOptions{Ecosystem: EcosystemNPM, Options: Options{GitHubDir: t.TempDir()}}
	npmNormalized, _, npmEvents, err := normalizeBumpOptions(npmOptions, []ReleaseEvent{{Dependency: "@acme/provider", Version: "1.2.3"}})
	if err != nil {
		t.Fatal(err)
	}
	if npmNormalized.Ecosystem != EcosystemNPM || len(npmEvents) != 1 || npmEvents[0].Version != "1.2.3" {
		t.Fatalf("npm normalization = %+v events %+v", npmNormalized, npmEvents)
	}
}

// -- currentVerification / sameReleaseEvents ------------------------------

func TestDepsCovBumpCoreCurrentVerificationReturnsAnIndependentCopy(t *testing.T) {
	t.Parallel()
	if checks := currentVerification(orchestrate.Options{}); checks != nil {
		t.Fatalf("verification checks without Verify = %+v, want nil", checks)
	}
	lifecycle := orchestrate.Options{
		Verify: true,
		Checks: []quality.Check{quality.CheckLint, quality.CheckTest},
	}
	checks := currentVerification(lifecycle)
	if len(checks) != 2 || checks[0] != quality.CheckLint || checks[1] != quality.CheckTest {
		t.Fatalf("verification checks = %+v", checks)
	}
	checks[0] = quality.CheckBuild
	if lifecycle.Checks[0] != quality.CheckLint {
		t.Fatal("currentVerification returned the lifecycle's own slice; a caller could mutate the campaign's verification policy")
	}
}

func TestDepsCovBumpCoreSameReleaseEventsComparesLengthDependencyAndVersion(t *testing.T) {
	t.Parallel()
	base := []ReleaseEvent{
		{Dependency: "example.com/a", Version: "v1.0.0"},
		{Dependency: "example.com/b", Version: "v1.0.0"},
	}
	if !sameReleaseEvents(nil, nil) {
		t.Fatal("two empty seed sets must compare equal")
	}
	if sameReleaseEvents(base, base[:1]) {
		t.Fatal("a length mismatch must not compare equal")
	}
	if sameReleaseEvents(base, []ReleaseEvent{
		{Dependency: "example.com/a", Version: "v1.0.0"},
		{Dependency: "example.com/b", Version: "v2.0.0"},
	}) {
		t.Fatal("a version mismatch must not compare equal")
	}
	if sameReleaseEvents(base, []ReleaseEvent{
		{Dependency: "example.com/a", Version: "v1.0.0"},
		{Dependency: "example.com/other", Version: "v1.0.0"},
	}) {
		t.Fatal("a dependency mismatch must not compare equal")
	}
	if !sameReleaseEvents(base, append([]ReleaseEvent(nil), base...)) {
		t.Fatal("identical seed sets must compare equal")
	}
}

// -- resumeBumpReport -----------------------------------------------------

func depsCovResumeOptions() BumpOptions {
	return BumpOptions{
		Options:      Options{Timeout: time.Second},
		PollInterval: time.Millisecond,
		LatestGoVersion: func(context.Context, string) (string, error) {
			return "v0.5.0", nil
		},
		LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
			return PublishedGoRelease{
				Version: "v0.5.0", Requirements: map[string]string{"example.com/provider": "v0.2.0"},
			}, nil
		},
	}
}

func depsCovResumeOptionsFor(previous *BumpReport) BumpOptions {
	options := depsCovResumeOptions()
	options.Previous = previous
	return options
}

func depsCovAwaitingWave(seed []ReleaseEvent) BumpWaveReport {
	return BumpWaveReport{
		Index: 1, Status: "awaiting_release", Events: seed,
		Releases: []ReleaseObservation{{
			Module: "example.com/adapter", Repository: "acme/adapter", Before: "v0.4.0",
			Source: "go list -m example.com/adapter@latest", Status: "awaiting_release",
		}},
	}
}

func TestDepsCovBumpCoreResumeBumpReportRefusesMismatchedIdentity(t *testing.T) {
	t.Parallel()
	seed := depsCovSeedEvents()
	empty := depsCovEmptyReport(seed)
	mutate := func(change func(*BumpReport)) BumpReport {
		report := empty
		change(&report)
		return report
	}
	cases := []struct {
		name     string
		previous BumpReport
		seed     []ReleaseEvent
		want     string
	}{
		{
			name:     "schema version",
			previous: mutate(func(report *BumpReport) { report.SchemaVersion = 2 }),
			seed:     seed,
			want:     "resume report schema version 2 is unsupported; want 1",
		},
		{
			name:     "operation",
			previous: mutate(func(report *BumpReport) { report.Operation = "deps-bump-go-other" }),
			seed:     seed,
			want:     "does not match operation, ecosystem, and base ref",
		},
		{
			name:     "ecosystem",
			previous: mutate(func(report *BumpReport) { report.Ecosystem = EcosystemNPM }),
			seed:     seed,
			want:     "does not match operation, ecosystem, and base ref",
		},
		{
			name:     "base ref",
			previous: mutate(func(report *BumpReport) { report.BaseRef = "develop" }),
			seed:     seed,
			want:     "does not match operation, ecosystem, and base ref",
		},
		{
			name: "seed event version",
			previous: mutate(func(report *BumpReport) {
				report.SeedEvents = []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.3.0", Source: "explicit"}}
			}),
			seed: seed,
			want: "seed events do not match this command",
		},
		{
			name: "seed event count",
			previous: mutate(func(report *BumpReport) {
				report.SeedEvents = append(append([]ReleaseEvent(nil), seed...), ReleaseEvent{Dependency: "example.com/extra", Version: "v1.0.0"})
			}),
			seed: seed,
			want: "seed events do not match this command",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			recorder := &depsCovPersist{}
			options := depsCovResumeOptions()
			options.Persist = recorder.persist
			options.Previous = &testCase.previous
			report, events, startWave, err := resumeBumpReport(context.Background(), empty, testCase.seed, options)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("resumeBumpReport error = %v, want it to contain %q", err, testCase.want)
			}
			if startWave != 1 {
				t.Fatalf("start wave = %d, want 1", startWave)
			}
			if report.Operation != empty.Operation || report.Status != empty.Status {
				t.Fatalf("refused resume returned %+v, want the untouched empty report", report)
			}
			if len(events) != len(testCase.seed) {
				t.Fatalf("refused resume returned events %+v, want the seed events", events)
			}
			if recorder.calls != 0 {
				t.Fatalf("a refused resume persisted %d reports", recorder.calls)
			}
		})
	}
}

func TestDepsCovBumpCoreResumeBumpReportResumesEachWaveState(t *testing.T) {
	t.Parallel()
	seed := depsCovSeedEvents()
	empty := depsCovEmptyReport(seed)

	t.Run("completed campaign is terminal", func(t *testing.T) {
		t.Parallel()
		previous := empty
		previous.Status = "completed"
		previous.Waves = []BumpWaveReport{{Index: 1, Status: "completed", Events: seed}}
		options := depsCovResumeOptions()
		options.Previous = &previous
		report, events, startWave, err := resumeBumpReport(context.Background(), empty, seed, options)
		if err != nil {
			t.Fatal(err)
		}
		if report.Status != "completed" || startWave != 2 || events != nil {
			t.Fatalf("completed resume = status %q startWave %d events %+v", report.Status, startWave, events)
		}
	})

	t.Run("no waves restarts at wave one", func(t *testing.T) {
		t.Parallel()
		previous := empty
		previous.Status = "awaiting_release"
		options := depsCovResumeOptions()
		options.Previous = &previous
		report, events, startWave, err := resumeBumpReport(context.Background(), empty, seed, options)
		if err != nil {
			t.Fatal(err)
		}
		if report.Status != "running" || startWave != 1 || len(events) != 1 {
			t.Fatalf("empty-wave resume = status %q startWave %d events %+v", report.Status, startWave, events)
		}
	})

	t.Run("completed last wave accumulates its events", func(t *testing.T) {
		t.Parallel()
		previous := empty
		previous.Status = "running"
		previous.Waves = []BumpWaveReport{{
			Index: 2, Status: "completed",
			Events: []ReleaseEvent{{Dependency: "example.com/extra", Version: "v1.0.0", Source: "observed_release"}},
		}}
		report, events, startWave, err := resumeBumpReport(context.Background(), empty, seed, depsCovResumeOptionsFor(&previous))
		if err != nil {
			t.Fatal(err)
		}
		if startWave != 3 || len(events) != 2 {
			t.Fatalf("completed-wave resume = startWave %d events %+v", startWave, events)
		}
		if events[0].Dependency != "example.com/extra" || events[1].Dependency != "example.com/provider" {
			t.Fatalf("accumulated events = %+v, want both the seed and the completed wave's events", events)
		}
		_ = report
	})

	t.Run("awaiting release completes once the release appears", func(t *testing.T) {
		t.Parallel()
		previous := empty
		previous.Waves = []BumpWaveReport{depsCovAwaitingWave(seed)}
		recorder := &depsCovPersist{}
		options := depsCovResumeOptions()
		options.Persist = recorder.persist
		options.Previous = &previous
		report, events, startWave, err := resumeBumpReport(context.Background(), empty, seed, options)
		if err != nil {
			t.Fatal(err)
		}
		if report.Status != "running" || report.Waves[0].Status != "completed" {
			t.Fatalf("resumed report = %+v", report)
		}
		if report.Waves[0].Releases[0].After != "v0.5.0" || startWave != 2 {
			t.Fatalf("resumed releases = %+v startWave %d", report.Waves[0].Releases, startWave)
		}
		if recorder.calls != 1 {
			t.Fatalf("resume persisted %d reports, want exactly one", recorder.calls)
		}
		if len(events) != 2 || events[0].Dependency != "example.com/adapter" || events[0].Version != "v0.5.0" ||
			events[1].Dependency != "example.com/provider" {
			t.Fatalf("resumed events = %+v, want the observed release carried alongside the seed", events)
		}
	})

	t.Run("held wave keeps naming the human blocker on failure", func(t *testing.T) {
		t.Parallel()
		previous := empty
		wave := depsCovAwaitingWave(seed)
		wave.Status = "awaiting_hold_release"
		wave.HeldRepositories = []HeldRepository{{Repository: "acme/adapter", PR: "https://github.test/acme/adapter/pull/1"}}
		previous.Waves = []BumpWaveReport{wave}
		options := depsCovResumeOptions()
		options.Timeout = 5 * time.Millisecond
		options.LatestGoVersion = func(context.Context, string) (string, error) { return "v0.4.0", nil }
		options.Previous = &previous
		report, _, startWave, err := resumeBumpReport(context.Background(), empty, seed, options)
		if err == nil || !strings.Contains(err.Error(), "did not advance beyond v0.4.0") {
			t.Fatalf("held-wave resume error = %v", err)
		}
		if report.Status != "awaiting_hold_release" {
			t.Fatalf("held-wave resume status = %q, want the hold kept as the named blocker", report.Status)
		}
		if startWave != 1 {
			t.Fatalf("held-wave resume startWave = %d, want the wave index it stalled on", startWave)
		}
	})

	t.Run("an interrupted wave is replayed from its own events", func(t *testing.T) {
		t.Parallel()
		previous := empty
		previous.Waves = []BumpWaveReport{{
			Index: 1, Status: "processing",
			Events: []ReleaseEvent{{Dependency: "example.com/extra", Version: "v1.0.0", Source: "explicit"}},
		}}
		report, events, startWave, err := resumeBumpReport(context.Background(), empty, seed, depsCovResumeOptionsFor(&previous))
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Waves) != 0 {
			t.Fatalf("interrupted wave was not dropped for replay: %+v", report.Waves)
		}
		if startWave != 1 || len(events) != 2 {
			t.Fatalf("interrupted-wave resume = startWave %d events %+v", startWave, events)
		}
	})
}

// -- resumeReleaseObservations -------------------------------------------

func TestDepsCovBumpCoreResumeReleaseObservationsPollsOnlyPendingObservations(t *testing.T) {
	t.Parallel()
	versionCalls := 0
	releaseCalls := 0
	observations, err := resumeReleaseObservations(context.Background(), []ReleaseObservation{
		{Module: "example.com/skip", Status: "released", After: "v1.0.0"},
		{Module: "example.com/plain", Before: "v0.1.0", Status: "awaiting_release"},
		{
			Module: "example.com/requirements", Before: "v0.1.0", Status: "awaiting_release",
			ExpectedRequirements: map[string]string{"example.com/provider": "v0.2.0"},
		},
	}, BumpOptions{
		Options:      Options{Timeout: time.Second},
		PollInterval: time.Millisecond,
		LatestGoVersion: func(_ context.Context, module string) (string, error) {
			versionCalls++
			if module != "example.com/plain" {
				return "", fmt.Errorf("unexpected version lookup for %s", module)
			}
			return "v0.2.0", nil
		},
		LatestGoRelease: func(_ context.Context, module string) (PublishedGoRelease, error) {
			releaseCalls++
			if module != "example.com/requirements" {
				return PublishedGoRelease{}, fmt.Errorf("unexpected release lookup for %s", module)
			}
			return PublishedGoRelease{
				Version: "v0.2.0", Requirements: map[string]string{"example.com/provider": "v0.2.0"},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if versionCalls != 1 || releaseCalls != 1 {
		t.Fatalf("version lookups = %d, release lookups = %d, want one of each", versionCalls, releaseCalls)
	}
	if observations[0].After != "v1.0.0" || observations[0].Status != "released" {
		t.Fatalf("an already-released observation was repolled: %+v", observations[0])
	}
	if observations[1].Status != "released" || observations[1].After != "v0.2.0" {
		t.Fatalf("plain observation = %+v, want the published version", observations[1])
	}
	if observations[2].Status != "released" || observations[2].After != "v0.2.0" {
		t.Fatalf("requirement observation = %+v, want the release that selects the provider event", observations[2])
	}
}

func TestDepsCovBumpCoreResumeReleaseObservationsJoinsLookupFailures(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("registry unavailable")
	observations, err := resumeReleaseObservations(context.Background(), []ReleaseObservation{
		{Module: "example.com/a", Before: "v0.1.0", Status: "awaiting_release"},
		{Module: "example.com/b", Before: "v0.1.0", Status: "awaiting_release"},
	}, BumpOptions{
		Options:      Options{Timeout: time.Second},
		PollInterval: time.Millisecond,
		LatestGoVersion: func(context.Context, string) (string, error) {
			return "", sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the registry failure joined", err)
	}
	for _, observation := range observations {
		if observation.Status != "failed" || observation.Reason != sentinel.Error() {
			t.Fatalf("failed observation = %+v", observation)
		}
	}
}

// -- mergeGraph* evidence helpers ----------------------------------------

func TestDepsCovBumpCoreMergeGraphEvidenceDeduplicatesAndSorts(t *testing.T) {
	t.Parallel()
	if got := mergeGraphDiscoverySkips(); len(got) != 0 {
		t.Fatalf("mergeGraphDiscoverySkips() = %+v, want empty", got)
	}
	skips := mergeGraphDiscoverySkips(
		[]GraphDiscoverySkip{{Repository: "acme/b", Reason: "first"}, {Repository: "acme/a", Reason: "first"}},
		[]GraphDiscoverySkip{{Repository: "acme/a", Reason: "second"}, {Repository: "acme/c", Reason: "only"}},
	)
	if len(skips) != 3 || skips[0].Repository != "acme/a" || skips[0].Reason != "second" ||
		skips[1].Repository != "acme/b" || skips[2].Repository != "acme/c" {
		t.Fatalf("merged discovery skips = %+v, want one sorted row per repository with the newest reason", skips)
	}

	fallbacks := mergeGraphDefaultBranchFallbacks(
		[]GraphDefaultBranchFallback{{Repository: "acme/b", Ref: "main"}, {Repository: "acme/a", Ref: "trunk"}},
		[]GraphDefaultBranchFallback{{Repository: "acme/a", Ref: "develop"}, {Repository: "acme/c", Ref: "main"}},
	)
	if len(fallbacks) != 3 || fallbacks[0].Repository != "acme/a" || fallbacks[0].Ref != "develop" ||
		fallbacks[1].Repository != "acme/b" || fallbacks[2].Repository != "acme/c" {
		t.Fatalf("merged default-branch fallbacks = %+v", fallbacks)
	}

	warnings := mergeGraphManifestWarnings(
		[]GraphManifestWarning{
			{Repository: "acme/a", Manifest: "go.mod", Reason: "first"},
			{Repository: "acme/b", Manifest: "go.mod", Reason: "b"},
		},
		[]GraphManifestWarning{
			{Repository: "acme/a", Manifest: "tools/go.mod", Reason: "nested"},
			{Repository: "acme/a", Manifest: "go.mod", Reason: "updated"},
		},
	)
	want := []GraphManifestWarning{
		{Repository: "acme/a", Manifest: "go.mod", Reason: "updated"},
		{Repository: "acme/a", Manifest: "tools/go.mod", Reason: "nested"},
		{Repository: "acme/b", Manifest: "go.mod", Reason: "b"},
	}
	if len(warnings) != len(want) {
		t.Fatalf("merged manifest warnings = %+v", warnings)
	}
	for index := range want {
		if warnings[index] != want[index] {
			t.Fatalf("merged manifest warnings[%d] = %+v, want %+v", index, warnings[index], want[index])
		}
	}

	modules := mergeGraphAmbiguousModules(
		[]GraphAmbiguousModuleWarning{{Module: "example.com/b", Repository: "acme/b", Duplicates: []string{"x"}}},
		[]GraphAmbiguousModuleWarning{
			{Module: "example.com/a", Repository: "acme/a"},
			{Module: "example.com/b", Repository: "acme/z", Duplicates: []string{"z"}},
		},
	)
	if len(modules) != 2 || modules[0].Module != "example.com/a" || modules[1].Module != "example.com/b" ||
		modules[1].Repository != "acme/z" || len(modules[1].Duplicates) != 1 || modules[1].Duplicates[0] != "z" {
		t.Fatalf("merged ambiguous modules = %+v", modules)
	}
}

// -- refreshStaleReleaseEvents -------------------------------------------

func TestDepsCovBumpCoreRefreshStaleReleaseEventsStampsUncheckedEvents(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	lookups := 0
	events, refreshes, err := refreshStaleReleaseEvents(context.Background(), []ReleaseEvent{
		{Dependency: "example.com/zero", Version: "v1.0.0", Source: "explicit"},
		{Dependency: "example.com/fresh", Version: "v1.0.0", Source: "observed_release", CheckedAt: now.Add(-time.Minute)},
	}, BumpOptions{
		RefreshAfter: 5 * time.Minute,
		Now:          func() time.Time { return now },
		LatestGoVersion: func(context.Context, string) (string, error) {
			lookups++
			return "", errors.New("refresh must not reach the registry")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if lookups != 0 || len(refreshes) != 0 {
		t.Fatalf("lookups = %d, refreshes = %+v, want neither", lookups, refreshes)
	}
	checked := map[string]time.Time{}
	for _, event := range events {
		checked[event.Dependency] = event.CheckedAt
	}
	if !checked["example.com/zero"].Equal(now) {
		t.Fatalf("unchecked event CheckedAt = %s, want the injected clock", checked["example.com/zero"])
	}
	if !checked["example.com/fresh"].Equal(now.Add(-time.Minute)) {
		t.Fatalf("fresh event CheckedAt = %s, want it left exactly as observed", checked["example.com/fresh"])
	}
}

func TestDepsCovBumpCoreRefreshStaleReleaseEventsReportsRetainedAndInvalidVersions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	stale := func(dependency string) ReleaseEvent {
		return ReleaseEvent{Dependency: dependency, Version: "v1.2.0", Source: "observed_release", CheckedAt: now.Add(-time.Hour)}
	}

	events, refreshes, err := refreshStaleReleaseEvents(context.Background(), []ReleaseEvent{stale("example.com/current")}, BumpOptions{
		RefreshAfter: time.Minute,
		Now:          func() time.Time { return now },
		LatestGoVersion: func(context.Context, string) (string, error) {
			return "v1.2.0", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshes) != 1 || refreshes[0].Before != "v1.2.0" || refreshes[0].After != "v1.2.0" ||
		!strings.Contains(refreshes[0].Reason, "no newer release") {
		t.Fatalf("refreshes = %+v, want the retained event explained", refreshes)
	}
	if events[0].Source != "observed_release" || events[0].Version != "v1.2.0" {
		t.Fatalf("event = %+v, want the accumulated event retained", events[0])
	}

	_, _, err = refreshStaleReleaseEvents(context.Background(), []ReleaseEvent{stale("example.com/bad")}, BumpOptions{
		RefreshAfter: time.Minute,
		Now:          func() time.Time { return now },
		LatestGoVersion: func(context.Context, string) (string, error) {
			return "not-a-version", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), `registry returned invalid version "not-a-version"`) {
		t.Fatalf("invalid-version refresh error = %v", err)
	}
}

// -- operation identity / wave prompt ------------------------------------

func TestDepsCovBumpCoreOperationPrefixAndWavePromptDefaultToGoAndSortEvents(t *testing.T) {
	t.Parallel()
	if got := bumpOperationPrefix(""); got != "deps-bump-go-" {
		t.Fatalf("bumpOperationPrefix(\"\") = %q, want the Go prefix", got)
	}
	if got := bumpOperationPrefix(EcosystemNPM); got != "deps-bump-npm-" {
		t.Fatalf("bumpOperationPrefix(npm) = %q", got)
	}
	prompt := bumpWavePrompt(EcosystemNPM, 2, []ReleaseEvent{
		{Dependency: "@acme/b", Version: "2.0.0"},
		{Dependency: "@acme/a", Version: "1.0.0"},
	})
	want := "wb deps bump npm wave 2: propagate root release event(s) @acme/a@1.0.0, @acme/b@2.0.0 through the dependency fleet."
	if prompt != want {
		t.Fatalf("bumpWavePrompt = %q, want %q", prompt, want)
	}
}

// -- RunBump --------------------------------------------------------------

func TestDepsCovBumpCoreRunBumpRecordsVerificationPolicyAndRefusesAHeldLock(t *testing.T) {
	t.Run("verification policy is recorded for a dry run", func(t *testing.T) {
		githubDir, repositories := depsCovGoDryRunFleet(t)
		options := depsCovDryRunBumpOptions(githubDir)
		options.Verify = true
		report, err := RunBump(context.Background(), depsCovSeedEvents(), repositories, options)
		if err != nil {
			t.Fatal(err)
		}
		if report.ValidationMode != ValidationModeFull {
			t.Fatalf("validation mode = %q, want full", report.ValidationMode)
		}
		if len(report.Verification) != 3 || report.Verification[0] != quality.CheckLint ||
			report.Verification[1] != quality.CheckTest || report.Verification[2] != quality.CheckBuild {
			t.Fatalf("recorded verification = %+v, want the default lint/test/build checks", report.Verification)
		}
	})

	t.Run("a live operation lock refuses a second campaign", func(t *testing.T) {
		t.Setenv(wbhome.EnvOverride, t.TempDir())
		githubDir := t.TempDir()
		seed := depsCovSeedEvents()
		lock, err := orchestrate.AcquireOperationLock(githubDir, BumpOperationID(seed), false)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Release() }()
		report, err := RunBump(context.Background(), seed, nil, BumpOptions{
			Options: Options{GitHubDir: githubDir, Ref: "main"},
		})
		if err == nil || !strings.Contains(err.Error(), "already active") {
			t.Fatalf("error = %v, want the live lock refused", err)
		}
		if report.Status != "" {
			t.Fatalf("refused campaign report = %+v, want the zero report", report)
		}
	})
}

func TestDepsCovBumpCoreRunBumpResumesCompletedAndRefusesMismatchedReports(t *testing.T) {
	t.Run("completed report is returned without a new wave", func(t *testing.T) {
		t.Setenv(wbhome.EnvOverride, t.TempDir())
		seed := depsCovSeedEvents()
		previous := depsCovEmptyReport(seed)
		previous.Status = "completed"
		previous.Waves = []BumpWaveReport{{Index: 1, Status: "completed", Events: seed}}
		report, err := RunBump(context.Background(), seed, nil, BumpOptions{
			Options:  Options{GitHubDir: t.TempDir(), Ref: "main", Resume: true, Verify: true},
			Previous: &previous,
		})
		if err != nil {
			t.Fatal(err)
		}
		if report.Status != "completed" || len(report.Waves) != 1 {
			t.Fatalf("report = %+v, want the persisted completed campaign returned unchanged", report)
		}
		if report.ValidationMode != ValidationModeFull || len(report.Verification) != 3 {
			t.Fatalf("report policy = mode %q verification %+v, want THIS invocation's verification", report.ValidationMode, report.Verification)
		}
	})

	t.Run("mismatched report aborts before discovery", func(t *testing.T) {
		t.Setenv(wbhome.EnvOverride, t.TempDir())
		seed := depsCovSeedEvents()
		previous := depsCovEmptyReport(seed)
		previous.SchemaVersion = 2
		_, err := RunBump(context.Background(), seed, nil, BumpOptions{
			Options:  Options{GitHubDir: t.TempDir(), Ref: "main", Resume: true},
			Previous: &previous,
		})
		if err == nil || !strings.Contains(err.Error(), "schema version 2 is unsupported") {
			t.Fatalf("error = %v, want the schema mismatch surfaced", err)
		}
	})
}

//nolint:paralleltest // calls runnertest.AllowRealProcess (via a fixture helper), which Go's testing package forbids combined with t.Parallel
func TestDepsCovBumpCoreRunBumpSurfacesPersistFailureAtEveryStage(t *testing.T) {
	githubDir, repositories := depsCovGoDryRunFleet(t)
	options := depsCovDryRunBumpOptions(githubDir)
	seed := depsCovSeedEvents()

	baseline := &depsCovPersist{}
	if _, err := RunBump(context.Background(), seed, repositories, options); err != nil {
		t.Fatal(err)
	}
	options.Persist = baseline.persist
	report, err := RunBump(context.Background(), seed, repositories, options)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "planned" {
		t.Fatalf("baseline campaign status = %q, want planned", report.Status)
	}
	if baseline.calls < 8 {
		t.Fatalf("baseline campaign persisted %d reports; the stage table needs the full dry-run sequence", baseline.calls)
	}

	for failAt := 2; failAt <= baseline.calls; failAt++ {
		sentinel := fmt.Errorf("persist call %d failed", failAt)
		recorder := &depsCovPersist{sentinel: sentinel}
		recorder.failIf = func(BumpReport) bool { return recorder.calls == failAt }
		failingOptions := depsCovDryRunBumpOptions(githubDir)
		failingOptions.Persist = recorder.persist
		if _, err := RunBump(context.Background(), seed, repositories, failingOptions); !errors.Is(err, sentinel) {
			t.Fatalf("persist failure at call %d returned %v, want the injected error", failAt, err)
		}
		if recorder.reports[len(recorder.reports)-1].Operation == "" {
			t.Fatalf("persist failure at call %d recorded no operation identity", failAt)
		}
	}
}

//nolint:paralleltest // calls runnertest.AllowRealProcess (via a fixture helper), which Go's testing package forbids combined with t.Parallel
func TestDepsCovBumpCoreRunBumpSurfacesPersistFailureWhenRecordingCompletion(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n"),
	}
	sentinel := errors.New("persist completion failed")
	recorder := &depsCovPersist{sentinel: sentinel}
	recorder.failIf = func(report BumpReport) bool { return report.Status == "completed" }
	_, err := RunBump(context.Background(), depsCovSeedEvents(), repositories,
		BumpOptions{Options: Options{GitHubDir: githubDir, Ref: "main", Parallel: 1, ParallelExplicit: true, DryRun: true}, Persist: recorder.persist})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the completion persistence failure surfaced", err)
	}
	last := recorder.reports[len(recorder.reports)-1]
	if last.Status != "completed" || last.Phase != BumpPhaseCompleted {
		t.Fatalf("last persisted report = %+v, want the completed phase persisted before the failure surfaced", last)
	}
}

//nolint:paralleltest // calls runnertest.AllowRealProcess (via a fixture helper), which Go's testing package forbids combined with t.Parallel
func TestDepsCovBumpCoreRunBumpFailsDiscoveryOnMalformedRootManifest(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	provider := newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n")
	broken := newBumpRepository(t, root, githubDir, "broken", "module example.com/broken\n\ngo 1.24\n")
	writeTestFile(t, filepath.Join(broken.Path, "go.mod"), "require (\n")
	runTestGit(t, broken.Path, "add", "-A")
	runTestGit(t, broken.Path, "commit", "-m", "break the root manifest")
	runTestGit(t, broken.Path, "push", "origin", "main")

	report, err := RunBump(context.Background(), depsCovSeedEvents(), []Repository{provider, broken}, depsCovDryRunBumpOptions(githubDir))
	if err == nil || !strings.Contains(err.Error(), "parse go.mod") {
		t.Fatalf("error = %v, want the unparseable root manifest to fail discovery", err)
	}
	if report.Status != "failed" {
		t.Fatalf("report status = %q, want failed", report.Status)
	}
}

//nolint:paralleltest // calls runnertest.AllowRealProcess (via a fixture helper), which Go's testing package forbids combined with t.Parallel
func TestDepsCovBumpCoreRunBumpFailsOnACrossRepositoryCycle(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n"),
		newBumpRepository(t, root, githubDir, "a", "module example.com/a\n\ngo 1.24\n\nrequire (\n\texample.com/provider v0.1.0\n\texample.com/b v0.1.0\n)\n"),
		newBumpRepository(t, root, githubDir, "b", "module example.com/b\n\ngo 1.24\n\nrequire example.com/a v0.1.0\n"),
	}
	report, err := RunBump(context.Background(), depsCovSeedEvents(), repositories, depsCovDryRunBumpOptions(githubDir))
	if err == nil || !strings.Contains(err.Error(), "acme/a -> acme/b -> acme/a") {
		t.Fatalf("error = %v, want the relevant cycle rejected", err)
	}
	if report.Status != "failed" {
		t.Fatalf("report status = %q, want failed", report.Status)
	}
}

//nolint:paralleltest // calls runnertest.AllowRealProcess (via a fixture helper), which Go's testing package forbids combined with t.Parallel
func TestDepsCovBumpCoreRunBumpSurfacesAStaleEventRefreshFailure(t *testing.T) {
	githubDir, repositories := depsCovGoDryRunFleet(t)
	sentinel := errors.New("registry unavailable")
	options := depsCovDryRunBumpOptions(githubDir)
	options.RefreshAfter = time.Minute
	options.LatestGoVersion = func(context.Context, string) (string, error) { return "", sentinel }
	seed := []ReleaseEvent{{
		Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit",
		CheckedAt: time.Now().Add(-time.Hour),
	}}
	report, err := RunBump(context.Background(), seed, repositories, options)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the stale-event refresh failure surfaced", err)
	}
	if !strings.Contains(err.Error(), "refresh stale release event example.com/provider@v0.2.0") {
		t.Fatalf("error = %v, want it to name the stale event", err)
	}
	if report.Status != "failed" {
		t.Fatalf("report status = %q, want failed", report.Status)
	}
}

func TestDepsCovBumpCoreRunBumpFailsTheWaveWhenApplyFails(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newNpmBumpRepository(t, root, githubDir, "provider", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/provider", "left-pad", "1.0.0"),
		}),
		newNpmBumpRepository(t, root, githubDir, "adapter", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/adapter", "@acme/provider", "1.0.0"),
			"nx.json":      "{ this is not valid json\n",
		}),
	}
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	report, err := RunBump(context.Background(),
		[]ReleaseEvent{{Dependency: "@acme/provider", Version: "2.0.0", Source: "explicit"}},
		repositories, BumpOptions{
			Ecosystem: EcosystemNPM,
			Options: Options{
				GitHubDir: githubDir, Ref: "main", Parallel: 1, ParallelExplicit: true,
				ValidationMode: ValidationModeNone,
			},
		})
	if err == nil || !strings.Contains(err.Error(), "parse nx.json") {
		t.Fatalf("error = %v, want the wave apply failure surfaced", err)
	}
	if report.Status != "failed" {
		t.Fatalf("report status = %q, want failed", report.Status)
	}
	if len(report.Waves) != 1 || report.Waves[0].Status != "failed" {
		t.Fatalf("waves = %+v, want one failed wave recorded", report.Waves)
	}
}

func TestDepsCovBumpCoreRunBumpFailsWhenABaselineReleaseCannotBeObserved(t *testing.T) {
	fixture := newFetchCacheFixture(t)
	t.Setenv(wbhome.EnvOverride, filepath.Join(fixture.root, ".wb"))
	sentinel := errors.New("registry unavailable")
	options := fixture.bumpOptions(false)
	options.LatestNpmVersion = func(_ context.Context, pkg string) (string, error) {
		if pkg == "@acme/lib" {
			return "", sentinel
		}
		return "1.0.0", nil
	}
	report, err := RunBump(context.Background(),
		[]ReleaseEvent{{Dependency: "@acme/provider", Version: "2.0.0", Source: "explicit"}},
		fixture.repos, options)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the baseline lookup failure surfaced", err)
	}
	if !strings.Contains(err.Error(), "observe baseline release for @acme/lib") {
		t.Fatalf("error = %v, want it to name the baseline module", err)
	}
	if report.Status != "failed" || len(report.Waves) != 1 || report.Waves[0].Status != "failed" {
		t.Fatalf("report = %+v, want a failed wave recorded before the merge", report)
	}
}

// depsCovPrepareMergeFixture wires the fake server-side-merge gh and the
// fetch-counting git shim onto PATH and returns campaign options for the shared
// 4-repository npm fixture.
func depsCovPrepareMergeFixture(t *testing.T, fixture fetchCacheFixture) BumpOptions {
	t.Helper()
	t.Setenv(wbhome.EnvOverride, filepath.Join(fixture.root, ".wb"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(fixture.root, "state"))
	fixture.installServerSideMergeGH(t)
	// installFetchCountingGit is also what puts the fixture's bin directory
	// (holding the fake gh) first on PATH.
	fixture.installFetchCountingGit(t)
	return fixture.bumpOptions(false)
}

func TestDepsCovBumpCoreRunBumpRefusesToExceedMaxWaves(t *testing.T) {
	fixture := newFetchCacheFixture(t)
	options := depsCovPrepareMergeFixture(t, fixture)
	options.MaxWaves = 1
	report, err := RunBump(context.Background(),
		[]ReleaseEvent{{Dependency: "@acme/provider", Version: "2.0.0", Source: "explicit"}},
		fixture.repos, options)
	if err == nil || !strings.Contains(err.Error(), "dependency bump exceeded --max-waves=1") {
		t.Fatalf("error = %v, want the wave cap enforced", err)
	}
	if report.Status != "failed" || len(report.Waves) != 1 {
		t.Fatalf("report = %+v, want exactly the first wave recorded before the refusal", report)
	}
}

func TestDepsCovBumpCoreRunBumpStopsOnAHeldPullRequest(t *testing.T) {
	fixture := newFetchCacheFixture(t)
	options := depsCovPrepareMergeFixture(t, fixture)
	options.Hold = []string{"acme/lib"}
	report, err := RunBump(context.Background(),
		[]ReleaseEvent{{Dependency: "@acme/provider", Version: "2.0.0", Source: "explicit"}},
		fixture.repos, options)
	if err != nil {
		t.Fatalf("a held campaign is a stopping point, not a failure: %v", err)
	}
	if report.Status != "awaiting_hold_release" || report.Phase != BumpPhaseAwaitingRelease {
		t.Fatalf("report status = %q phase = %q, want the hold named", report.Status, report.Phase)
	}
	if len(report.HeldRepositories) != 1 || report.HeldRepositories[0].Repository != "acme/lib" || report.HeldRepositories[0].PR == "" {
		t.Fatalf("held repositories = %+v, want acme/lib's open pull request", report.HeldRepositories)
	}
	if len(report.Waves) != 1 || report.Waves[0].Status != "awaiting_hold_release" {
		t.Fatalf("waves = %+v, want the first wave parked on the hold", report.Waves)
	}
	if len(report.Waves[0].Repositories) != 1 || !report.Waves[0].Repositories[0].Held || report.Waves[0].Repositories[0].PR == "" {
		t.Fatalf("held wave repositories = %+v, want acme/lib held with its open pull request", report.Waves[0].Repositories)
	}
}

func TestDepsCovBumpCoreRunBumpSurfacesPersistFailureWhileRecordingWaveState(t *testing.T) {
	campaign := func(t *testing.T, sentinel error, failIf func(BumpReport) bool) error {
		t.Helper()
		fixture := newFetchCacheFixture(t)
		options := depsCovPrepareMergeFixture(t, fixture)
		recorder := &depsCovPersist{sentinel: sentinel}
		recorder.failIf = failIf
		options.Persist = recorder.persist
		_, err := RunBump(context.Background(),
			[]ReleaseEvent{{Dependency: "@acme/provider", Version: "2.0.0", Source: "explicit"}},
			fixture.repos, options)
		return err
	}

	t.Run("merged wave", func(t *testing.T) {
		sentinel := errors.New("persist wave state failed")
		err := campaign(t, sentinel, func(report BumpReport) bool {
			waves := report.Waves
			return len(waves) > 0 && waves[len(waves)-1].Status == "merged"
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want the merged-wave persistence failure surfaced", err)
		}
	})

	t.Run("completed wave", func(t *testing.T) {
		sentinel := errors.New("persist wave state failed")
		err := campaign(t, sentinel, func(report BumpReport) bool {
			waves := report.Waves
			return len(waves) > 0 && waves[len(waves)-1].Status == "completed"
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want the completed-wave persistence failure surfaced", err)
		}
	})
}

// depsCovNpmCarrierFleet is an npm provider -> carrier -> consumer chain whose
// middle repository already selects the seeded provider version on origin/main,
// with a consumer that depends on the carrier: exactly the state that makes
// discoverExistingReleaseCarriers consult the registry before wave two.
func depsCovNpmCarrierFleet(t *testing.T) (string, []Repository) {
	t.Helper()
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	return githubDir, []Repository{
		newNpmBumpRepository(t, root, githubDir, "provider", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/provider", "left-pad", "1.0.0"),
		}),
		newNpmBumpRepository(t, root, githubDir, "carrier", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/carrier", "@acme/provider", "2.0.0"),
		}),
		newNpmBumpRepository(t, root, githubDir, "consumer", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/consumer", "@acme/carrier", "0.1.0"),
		}),
	}
}

// depsCovStaleThenFreshCarrierRelease returns a mismatched consumer release on
// its first lookup and a matching one afterwards, modelling an origin manifest
// that is already current while the published release is still one version
// behind.
func depsCovStaleThenFreshCarrierRelease() func(context.Context, string) (PublishedGoRelease, error) {
	calls := 0
	return func(_ context.Context, module string) (PublishedGoRelease, error) {
		calls++
		if module != "@acme/carrier" {
			return PublishedGoRelease{}, fmt.Errorf("unexpected release lookup for %s", module)
		}
		if calls == 1 {
			return PublishedGoRelease{
				Version: "2.0.1", Requirements: map[string]string{"@acme/provider": "1.0.0"},
			}, nil
		}
		return PublishedGoRelease{
			Version: "2.0.2", Requirements: map[string]string{"@acme/provider": "2.0.0"},
		}, nil
	}
}

func TestDepsCovBumpCoreRunBumpPlansWithoutWaitingOnAStaleCarrierInDryRun(t *testing.T) {
	githubDir, repositories := depsCovNpmCarrierFleet(t)
	report, err := RunBump(context.Background(),
		[]ReleaseEvent{{Dependency: "@acme/provider", Version: "2.0.0", Source: "explicit"}},
		repositories, BumpOptions{
			Ecosystem:        EcosystemNPM,
			Options:          Options{GitHubDir: githubDir, Ref: "main", Parallel: 1, ParallelExplicit: true, DryRun: true},
			LatestNpmRelease: depsCovStaleThenFreshCarrierRelease(),
		})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "planned" || len(report.Waves) != 1 {
		t.Fatalf("report = %+v, want a single planned wave", report)
	}
	wave := report.Waves[0]
	if wave.Status != "planned" || report.Phase != BumpPhasePlanned {
		t.Fatalf("wave status = %q phase = %q, want the dry run to stop as planned", wave.Status, report.Phase)
	}
	if len(wave.Releases) != 1 || wave.Releases[0].Status != "awaiting_release" ||
		!strings.Contains(wave.Releases[0].Reason, "does not select every provider event") {
		t.Fatalf("carrier evidence = %+v, want the unpublished carrier recorded", wave.Releases)
	}
}

func TestDepsCovBumpCoreRunBumpRefreshesAStaleCarrierAndContinues(t *testing.T) {
	testenv.Isolate(t)
	githubDir, repositories := depsCovNpmCarrierFleet(t)
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	report, err := RunBump(context.Background(),
		[]ReleaseEvent{{Dependency: "@acme/provider", Version: "2.0.0", Source: "explicit"}},
		repositories, BumpOptions{
			Ecosystem: EcosystemNPM,
			Options: Options{
				GitHubDir: githubDir, Ref: "main", Parallel: 1, ParallelExplicit: true,
				ValidationMode: ValidationModeNone,
			},
			LatestNpmRelease: depsCovStaleThenFreshCarrierRelease(),
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Waves) != 2 {
		t.Fatalf("waves = %+v, want the refreshed carrier wave plus the downstream wave", report.Waves)
	}
	first := report.Waves[0]
	if first.Status != "completed" || len(first.Releases) != 1 || first.Releases[0].After != "2.0.2" ||
		first.Releases[0].Status != "released" {
		t.Fatalf("first wave = %+v, want the carrier release refreshed to 2.0.2 and the wave completed", first)
	}
	second := report.Waves[1]
	if second.Status != "awaiting_merge" || report.Status != "awaiting_merge" || report.Phase != BumpPhaseAwaitingMerge {
		t.Fatalf("second wave = %+v status = %q phase = %q, want the downstream consumer waiting on merge", second, report.Status, report.Phase)
	}
	if len(second.Repositories) != 1 || second.Repositories[0].Repository != "acme/consumer" {
		t.Fatalf("second wave repositories = %+v, want the carrier's consumer", second.Repositories)
	}
}

// depsCovPublishedCarrierFleet is a Go chain whose middle repository already
// selects the seeded provider release on origin/main AND has a published
// release containing it, with a consumer already selecting that release: a
// campaign with nothing left to plan.
func depsCovPublishedCarrierFleet(t *testing.T) (string, []Repository) {
	t.Helper()
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	return githubDir, []Repository{
		newBumpRepository(t, root, githubDir, "provider", "module example.com/provider\n\ngo 1.24\n"),
		newBumpRepository(t, root, githubDir, "carrier", "module example.com/carrier\n\ngo 1.24\n\nrequire example.com/provider v0.2.0\n"),
		newBumpRepository(t, root, githubDir, "consumer", "module example.com/consumer\n\ngo 1.24\n\nrequire example.com/carrier v0.2.2\n"),
	}
}

//nolint:paralleltest // calls runnertest.AllowRealProcess (via a fixture helper), which Go's testing package forbids combined with t.Parallel
func TestDepsCovBumpCoreRunBumpCompletesWhenEveryConsumerAlreadyCarriesTheRelease(t *testing.T) {
	githubDir, repositories := depsCovPublishedCarrierFleet(t)
	report, err := RunBump(context.Background(), depsCovSeedEvents(), repositories, BumpOptions{
		Options: Options{GitHubDir: githubDir, Ref: "main", Parallel: 1, ParallelExplicit: true, DryRun: true},
		LatestGoRelease: func(_ context.Context, module string) (PublishedGoRelease, error) {
			if module != "example.com/carrier" {
				return PublishedGoRelease{}, fmt.Errorf("unexpected release lookup for %s", module)
			}
			return PublishedGoRelease{
				Version: "v0.2.2", Requirements: map[string]string{"example.com/provider": "v0.2.0"},
				Source: "test registry",
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Phase != BumpPhaseCompleted {
		t.Fatalf("report status = %q phase = %q, want the campaign complete", report.Status, report.Phase)
	}
	if len(report.Waves) != 1 || report.Waves[0].Status != "completed" {
		t.Fatalf("waves = %+v, want one completed evidence wave", report.Waves)
	}
	wave := report.Waves[0]
	if len(wave.Repositories) != 0 {
		t.Fatalf("wave repositories = %+v, want no work invented for an already-current fleet", wave.Repositories)
	}
	if len(wave.Releases) != 1 || wave.Releases[0].Status != "released" || wave.Releases[0].After != "v0.2.2" {
		t.Fatalf("wave releases = %+v, want the published carrier recorded", wave.Releases)
	}
}

func TestDepsCovBumpCoreRunBumpWaitsWhenTheCarrierReleaseNeverAppears(t *testing.T) {
	testenv.Isolate(t)
	githubDir, repositories := depsCovNpmCarrierFleet(t)
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	calls := 0
	report, err := RunBump(context.Background(),
		[]ReleaseEvent{{Dependency: "@acme/provider", Version: "2.0.0", Source: "explicit"}},
		repositories, BumpOptions{
			Ecosystem: EcosystemNPM,
			Options: Options{
				GitHubDir: githubDir, Ref: "main", Parallel: 1, ParallelExplicit: true,
				ValidationMode: ValidationModeNone,
			},
			LatestNpmRelease: func(_ context.Context, module string) (PublishedGoRelease, error) {
				calls++
				if module != "@acme/carrier" {
					return PublishedGoRelease{}, fmt.Errorf("unexpected release lookup for %s", module)
				}
				if calls == 1 {
					return PublishedGoRelease{
						Version: "2.0.1", Requirements: map[string]string{"@acme/provider": "1.0.0"},
					}, nil
				}
				return PublishedGoRelease{}, errors.New("registry offline")
			},
		})
	if err == nil || !strings.Contains(err.Error(), "registry offline") {
		t.Fatalf("error = %v, want the carrier lookup failure surfaced", err)
	}
	if report.Status != "awaiting_release" || report.Phase != BumpPhaseAwaitingRelease {
		t.Fatalf("report status = %q phase = %q, want the campaign parked on the missing carrier release", report.Status, report.Phase)
	}
	if len(report.Waves) != 1 || report.Waves[0].Status != "awaiting_release" {
		t.Fatalf("waves = %+v, want the parked carrier wave recorded", report.Waves)
	}
}

func TestDepsCovBumpCoreRunBumpWaitsWhenTheConsumerReleaseNeverAppears(t *testing.T) {
	fixture := newFetchCacheFixture(t)
	options := depsCovPrepareMergeFixture(t, fixture)
	inner := options.LatestNpmVersion
	calls := map[string]int{}
	options.LatestNpmVersion = func(ctx context.Context, pkg string) (string, error) {
		calls[pkg]++
		if pkg == "@acme/lib" && calls[pkg] > 1 {
			return "", errors.New("registry offline")
		}
		return inner(ctx, pkg)
	}
	report, err := RunBump(context.Background(),
		[]ReleaseEvent{{Dependency: "@acme/provider", Version: "2.0.0", Source: "explicit"}},
		fixture.repos, options)
	if err == nil || !strings.Contains(err.Error(), "registry offline") {
		t.Fatalf("error = %v, want the post-merge release lookup failure surfaced", err)
	}
	if report.Status != "awaiting_release" || report.Phase != BumpPhaseAwaitingRelease {
		t.Fatalf("report status = %q phase = %q, want the campaign parked on the missing consumer release", report.Status, report.Phase)
	}
	if len(report.Waves) != 1 || report.Waves[0].Status != "awaiting_release" {
		t.Fatalf("waves = %+v, want the merged wave parked awaiting its release", report.Waves)
	}
	if calls["@acme/lib"] != 2 {
		t.Fatalf("baseline + post-merge lib lookups = %d, want exactly 2", calls["@acme/lib"])
	}
}

// depsCovNpmCarrierCampaignOptions runs the carrier fleet without a merge, so
// the campaign's only remote effect is the read-only discovery fetch.
func depsCovNpmCarrierCampaignOptions(githubDir string) BumpOptions {
	return BumpOptions{
		Ecosystem: EcosystemNPM,
		Options: Options{
			GitHubDir: githubDir, Ref: "main", Parallel: 1, ParallelExplicit: true,
			ValidationMode: ValidationModeNone,
		},
		LatestNpmRelease: depsCovStaleThenFreshCarrierRelease(),
	}
}

func TestDepsCovBumpCoreRunBumpSurfacesPersistFailureWhileParkingCampaigns(t *testing.T) {
	carrierSeed := []ReleaseEvent{{Dependency: "@acme/provider", Version: "2.0.0", Source: "explicit"}}

	t.Run("dry-run carrier park", func(t *testing.T) {
		githubDir, repositories := depsCovNpmCarrierFleet(t)
		sentinel := errors.New("persist planned carrier failed")
		recorder := &depsCovPersist{sentinel: sentinel}
		recorder.failIf = func(report BumpReport) bool { return report.Status == "planned" }
		options := BumpOptions{
			Ecosystem:        EcosystemNPM,
			Options:          Options{GitHubDir: githubDir, Ref: "main", Parallel: 1, ParallelExplicit: true, DryRun: true},
			Persist:          recorder.persist,
			LatestNpmRelease: depsCovStaleThenFreshCarrierRelease(),
		}
		_, err := RunBump(context.Background(), carrierSeed, repositories, options)
		if !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want the planned-carrier persistence failure surfaced", err)
		}
	})

	t.Run("refreshed carrier wave", func(t *testing.T) {
		testenv.Isolate(t)
		githubDir, repositories := depsCovNpmCarrierFleet(t)
		t.Setenv(wbhome.EnvOverride, t.TempDir())
		sentinel := errors.New("persist completed carrier failed")
		recorder := &depsCovPersist{sentinel: sentinel}
		recorder.failIf = func(report BumpReport) bool {
			waves := report.Waves
			return len(waves) > 0 && waves[len(waves)-1].Status == "completed"
		}
		options := depsCovNpmCarrierCampaignOptions(githubDir)
		options.Persist = recorder.persist
		_, err := RunBump(context.Background(), carrierSeed, repositories, options)
		if !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want the refreshed-carrier persistence failure surfaced", err)
		}
	})

	t.Run("awaiting merge", func(t *testing.T) {
		testenv.Isolate(t)
		githubDir, repositories := depsCovNpmCarrierFleet(t)
		t.Setenv(wbhome.EnvOverride, t.TempDir())
		sentinel := errors.New("persist awaiting-merge failed")
		recorder := &depsCovPersist{sentinel: sentinel}
		recorder.failIf = func(report BumpReport) bool {
			waves := report.Waves
			return len(waves) > 0 && waves[len(waves)-1].Status == "awaiting_merge"
		}
		options := depsCovNpmCarrierCampaignOptions(githubDir)
		options.Persist = recorder.persist
		_, err := RunBump(context.Background(), carrierSeed, repositories, options)
		if !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want the awaiting-merge persistence failure surfaced", err)
		}
	})

	t.Run("held release", func(t *testing.T) {
		fixture := newFetchCacheFixture(t)
		options := depsCovPrepareMergeFixture(t, fixture)
		options.Hold = []string{"acme/lib"}
		sentinel := errors.New("persist held release failed")
		recorder := &depsCovPersist{sentinel: sentinel}
		recorder.failIf = func(report BumpReport) bool { return report.Status == "awaiting_hold_release" }
		options.Persist = recorder.persist
		_, err := RunBump(context.Background(),
			[]ReleaseEvent{{Dependency: "@acme/provider", Version: "2.0.0", Source: "explicit"}},
			fixture.repos, options)
		if !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want the held-release persistence failure surfaced", err)
		}
	})
}
