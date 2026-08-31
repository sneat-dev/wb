package deps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	progresspkg "github.com/sneat-dev/wb/internal/progress"
	"golang.org/x/mod/modfile"
	modmodule "golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// RunBump propagates explicit Go release events through recalculated direct
// consumer waves. Each newly observed provider release becomes the next wave.
func RunBump(ctx context.Context, events []ReleaseEvent, repositories []Repository, options BumpOptions) (BumpReport, error) {
	options, lifecycle, events, err := normalizeBumpOptions(options, events)
	if err != nil {
		return BumpReport{}, err
	}
	if !lifecycle.DryRun {
		lock, lockErr := orchestrate.AcquireOperationLock(lifecycle.GitHubDir, lifecycle.Operation, lifecycle.Resume)
		if lockErr != nil {
			return BumpReport{}, lockErr
		}
		defer func() { _ = lock.Release() }()
	}
	report := BumpReport{
		SchemaVersion: 1, Operation: lifecycle.Operation, Status: "running", Phase: BumpPhasePreparing,
		Ecosystem: options.Ecosystem, SeedEvents: append([]ReleaseEvent(nil), events...),
		GitHubDir: lifecycle.GitHubDir, BaseRef: lifecycle.Ref, Parallel: lifecycle.Parallel,
		RegistryLookupsSkipped: options.NoRegistry,
	}
	if lifecycle.Verify {
		report.Verification = append(report.Verification, lifecycle.Checks...)
	}
	startWave := 1
	if options.Previous != nil {
		var resumeErr error
		report, events, startWave, resumeErr = resumeBumpReport(ctx, report, events, options)
		if resumeErr != nil {
			return report, resumeErr
		}
		if report.Status == "completed" {
			return report, nil
		}
	}
	if err := persistBumpReport(options, report); err != nil {
		return report, err
	}
	for waveIndex := startWave; ; waveIndex++ {
		progresspkg.Report(options.Progress, progresspkg.Event{Operation: report.Operation, Phase: "discover_graph", State: progresspkg.Started, Wave: waveIndex, Total: len(repositories)})
		report.Phase = BumpPhaseDiscoveringGraph
		report.Progress = BumpProgress{Wave: waveIndex, RepositoriesTotal: len(repositories)}
		if err := persistBumpReport(options, report); err != nil {
			return report, err
		}
		var progressMu sync.Mutex
		var progressErr error
		onProgress := func(progress graphDiscoveryProgress) {
			progressMu.Lock()
			if progressErr != nil {
				progressMu.Unlock()
				return
			}
			report.Progress = BumpProgress{
				Wave:                  waveIndex,
				RepositoriesTotal:     progress.RepositoriesTotal,
				RepositoriesCompleted: progress.RepositoriesCompleted,
				LastRepository:        progress.LastRepository,
			}
			progressErr = persistBumpReport(options, report)
			progressMu.Unlock()
			progresspkg.Report(options.Progress, progresspkg.Event{Operation: report.Operation, Phase: "discover_graph", Repository: progress.LastRepository, State: progresspkg.Completed, Completed: progress.RepositoriesCompleted, Total: progress.RepositoriesTotal, Wave: waveIndex})
		}
		var graph bumpFleetGraph
		var err error
		if options.Ecosystem == EcosystemNPM {
			var npmGraph npmFleetGraph
			npmGraph, err = discoverNpmFleetGraph(ctx, repositories, lifecycle, npmGraphDiscoveryPolicy{SkipFailedNonNPM: true}, onProgress)
			graph = npmGraph
		} else {
			var goGraph goFleetGraph
			goGraph, err = discoverGoFleetGraph(ctx, repositories, lifecycle, goGraphDiscoveryPolicy{SkipFailedNonGo: true}, onProgress)
			graph = goGraph
		}
		report.DiscoverySkips = mergeGraphDiscoverySkips(report.DiscoverySkips, graph.Skips())
		report.DefaultBranchFallbacks = mergeGraphDefaultBranchFallbacks(report.DefaultBranchFallbacks, graph.BaseRefFallbacks())
		report.ManifestWarnings = mergeGraphManifestWarnings(report.ManifestWarnings, graph.ManifestWarnings())
		report.AmbiguousModules = mergeGraphAmbiguousModules(report.AmbiguousModules, graph.AmbiguousModules())
		if progressErr != nil {
			report.Status = "failed"
			return report, persistBumpFailure(options, report, progressErr)
		}
		if err != nil {
			report.Status = "failed"
			return report, persistBumpFailure(options, report, err)
		}
		if err := graph.validateUniqueModuleDeclarations(); err != nil {
			report.Status = "failed"
			return report, persistBumpFailure(options, report, err)
		}
		var refreshes []ReleaseEventRefresh
		events, refreshes, err = refreshStaleReleaseEvents(ctx, events, options)
		if err != nil {
			report.Status = "failed"
			return report, persistBumpFailure(options, report, err)
		}
		if err := graph.validateAcyclicPropagation(report.SeedEvents); err != nil {
			report.Status = "failed"
			return report, persistBumpFailure(options, report, err)
		}
		report.Phase = BumpPhasePlanningWave
		progresspkg.Report(options.Progress, progresspkg.Event{Operation: report.Operation, Phase: "plan_wave", State: progresspkg.Running, Wave: waveIndex})
		if err := persistBumpReport(options, report); err != nil {
			return report, err
		}
		carriers, carrierErr := discoverExistingReleaseCarriers(ctx, graph, events, options)
		events = mergeReleaseEvents(events, releaseEventsFromObservations(carriers))
		targetsByRepository, deferred := graph.coalescedRepositoriesForEvents(report.SeedEvents, events)
		if waveIndex > options.MaxWaves && (carrierErr != nil || len(targetsByRepository) > 0) {
			report.Status = "failed"
			return report, persistBumpFailure(options, report, fmt.Errorf("dependency bump exceeded --max-waves=%d", options.MaxWaves))
		}

		// Do not start a downstream PR while a provider whose main branch is
		// already current is still unpublished. Waiting here lets that release
		// join the same downstream PR and avoids an otherwise guaranteed second
		// GitHub Actions run.
		if carrierErr != nil && graph.pendingCarriersBlockTargets(carriers, targetsByRepository) {
			wave := BumpWaveReport{
				Index: waveIndex, Status: "awaiting_release", Events: append([]ReleaseEvent(nil), events...),
				Refreshes: refreshes, DeferredRepositories: deferred, Releases: carriers,
			}
			if lifecycle.DryRun {
				wave.Status = "planned"
				report.Waves = append(report.Waves, wave)
				report.Status = "planned"
				report.Phase = BumpPhasePlanned
				if persistErr := persistBumpReport(options, report); persistErr != nil {
					return report, persistErr
				}
				return report, nil
			}
			carriers, carrierErr = resumeReleaseObservations(ctx, carriers, options)
			wave.Releases = carriers
			events = mergeReleaseEvents(events, releaseEventsFromObservations(carriers))
			if carrierErr != nil {
				report.Waves = append(report.Waves, wave)
				report.Status = "awaiting_release"
				report.Phase = BumpPhaseAwaitingRelease
				return report, persistBumpFailure(options, report, carrierErr)
			}
			wave.Status = "completed"
			report.Waves = append(report.Waves, wave)
			if persistErr := persistBumpReport(options, report); persistErr != nil {
				return report, persistErr
			}
			continue
		}
		if len(targetsByRepository) == 0 {
			if len(refreshes) > 0 || len(carriers) > 0 {
				report.Waves = append(report.Waves, BumpWaveReport{
					Index: waveIndex, Status: "completed", Events: append([]ReleaseEvent(nil), events...),
					Refreshes: refreshes, Releases: carriers,
				})
			}
			report.Status = "completed"
			report.Phase = BumpPhaseCompleted
			if persistErr := persistBumpReport(options, report); persistErr != nil {
				return report, persistErr
			}
			return report, nil
		}
		wave := BumpWaveReport{
			Index: waveIndex, Status: "running", Events: append([]ReleaseEvent(nil), events...),
			Refreshes: refreshes, DeferredRepositories: deferred, Releases: carriers,
		}
		affectedRepositories := selectWaveRepositories(repositories, targetsByRepository)
		affectedModules := graph.affectedModules(targetsByRepository)
		baselines := map[string]ReleaseObservation{}
		if lifecycle.Merge {
			baselines, err = captureReleaseBaselines(ctx, graph, affectedModules, options)
			wave.Releases = mergeReleaseObservations(wave.Releases, sortedReleaseObservations(baselines))
			if err != nil {
				wave.Status = "failed"
				report.Waves = append(report.Waves, wave)
				report.Status = "failed"
				return report, persistBumpFailure(options, report, err)
			}
		}
		report.Waves = append(report.Waves, wave)
		waveReport := &report.Waves[len(report.Waves)-1]
		report.Phase = BumpPhaseProcessingWave
		progresspkg.Report(options.Progress, progresspkg.Event{Operation: report.Operation, Phase: "process_wave", State: progresspkg.Started, Wave: waveIndex, Total: len(affectedRepositories)})
		report.Progress = BumpProgress{Wave: waveIndex, RepositoriesTotal: len(affectedRepositories)}
		if persistErr := persistBumpReport(options, report); persistErr != nil {
			return report, persistErr
		}
		waveLifecycle := lifecycle
		waveLifecycle.Operation = fmt.Sprintf("%s-wave-%02d", lifecycle.Operation, waveIndex)
		waveLifecycle.Branch = fmt.Sprintf("wb/deps/bump-%s-wave-%02d", strings.TrimPrefix(lifecycle.Operation, bumpOperationPrefix(options.Ecosystem)), waveIndex)
		waveLifecycle.Prompt = bumpWavePrompt(options.Ecosystem, waveIndex, report.SeedEvents)
		handler := waveHandler{
			ecosystem:           options.Ecosystem,
			targetsByRepository: targetsByRepository,
			options:             options.Options,
			versionPlanID:       waveLifecycle.Operation,
		}
		results, runErr := orchestrate.Run(ctx, affectedRepositories, handler, waveLifecycle)
		for _, result := range results {
			waveReport.Repositories = append(waveReport.Repositories, repositoryReportFromResult(result))
		}
		report.Progress.RepositoriesCompleted = len(results)
		if persistErr := persistBumpReport(options, report); persistErr != nil {
			return report, persistErr
		}
		if runErr != nil {
			waveReport.Status = "failed"
			report.Status = "failed"
			return report, persistBumpFailure(options, report, runErr)
		}
		if lifecycle.DryRun {
			waveReport.Status = "planned"
			report.Status = "planned"
			report.Phase = BumpPhasePlanned
			if persistErr := persistBumpReport(options, report); persistErr != nil {
				return report, persistErr
			}
			return report, nil
		}
		if !lifecycle.Merge {
			waveReport.Status = "awaiting_merge"
			report.Status = "awaiting_merge"
			report.Phase = BumpPhaseAwaitingMerge
			if persistErr := persistBumpReport(options, report); persistErr != nil {
				return report, persistErr
			}
			return report, nil
		}
		progresspkg.Report(options.Progress, progresspkg.Event{Operation: report.Operation, Phase: "observe_releases", State: progresspkg.Waiting, Wave: waveIndex})
		waveReport.Status = "merged"
		if persistErr := persistBumpReport(options, report); persistErr != nil {
			return report, persistErr
		}
		pending := mergeReleaseObservations(carriers, mergedReleaseBaselines(results, affectedModules, baselines))
		var releaseErr error
		waveReport.Releases, releaseErr = resumeReleaseObservations(ctx, pending, options)
		if releaseErr != nil {
			waveReport.Status = "awaiting_release"
			report.Status = "awaiting_release"
			report.Phase = BumpPhaseAwaitingRelease
			return report, persistBumpFailure(options, report, releaseErr)
		}
		waveReport.Status = "completed"
		if persistErr := persistBumpReport(options, report); persistErr != nil {
			return report, persistErr
		}
		events = mergeReleaseEvents(events, releaseEventsFromObservations(waveReport.Releases))
	}
}

// ValidateBumpOptions applies the exact no-I/O normalization used by RunBump.
// Composite commands call it before an irreversible provider action so an
// invalid downstream checks/filter/retry/timeout/merge contract cannot leave
// a provider published but unable to enter the shared wave engine.
func ValidateBumpOptions(options BumpOptions, events []ReleaseEvent) error {
	_, _, _, err := normalizeBumpOptions(options, events)
	return err
}

func resumeBumpReport(ctx context.Context, empty BumpReport, seedEvents []ReleaseEvent, options BumpOptions) (BumpReport, []ReleaseEvent, int, error) {
	previous := *options.Previous
	if previous.SchemaVersion != empty.SchemaVersion {
		return empty, seedEvents, 1, fmt.Errorf("resume report schema version %d is unsupported; want %d", previous.SchemaVersion, empty.SchemaVersion)
	}
	if previous.Operation != empty.Operation || previous.Ecosystem != empty.Ecosystem || previous.BaseRef != empty.BaseRef {
		return empty, seedEvents, 1, fmt.Errorf("resume report does not match operation, ecosystem, and base ref")
	}
	if !sameReleaseEvents(previous.SeedEvents, seedEvents) {
		return empty, seedEvents, 1, fmt.Errorf("resume report seed events do not match this command")
	}
	if previous.Status == "completed" {
		return previous, nil, len(previous.Waves) + 1, nil
	}
	previous.Status = "running"
	if len(previous.Waves) == 0 {
		return previous, seedEvents, 1, nil
	}
	lastIndex := len(previous.Waves) - 1
	last := &previous.Waves[lastIndex]
	switch last.Status {
	case "merged", "awaiting_release":
		observations, err := resumeReleaseObservations(ctx, last.Releases, options)
		last.Releases = observations
		if err != nil {
			previous.Status = "awaiting_release"
			return previous, nil, last.Index, persistBumpFailure(options, previous, err)
		}
		last.Status = "completed"
		events := accumulatedBumpEvents(seedEvents, previous.Waves)
		return previous, events, last.Index + 1, persistBumpReport(options, previous)
	case "completed":
		events := accumulatedBumpEvents(seedEvents, previous.Waves)
		return previous, events, last.Index + 1, nil
	default:
		events := accumulatedBumpEvents(seedEvents, previous.Waves[:lastIndex])
		events = mergeReleaseEvents(events, last.Events)
		previous.Waves = previous.Waves[:lastIndex]
		return previous, events, last.Index, nil
	}
}

func accumulatedBumpEvents(seedEvents []ReleaseEvent, waves []BumpWaveReport) []ReleaseEvent {
	events := mergeReleaseEvents(seedEvents)
	for _, wave := range waves {
		events = mergeReleaseEvents(events, wave.Events, releaseEventsFromObservations(wave.Releases))
	}
	return events
}

func sameReleaseEvents(left, right []ReleaseEvent) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Dependency != right[index].Dependency || left[index].Version != right[index].Version {
			return false
		}
	}
	return true
}

func bumpNow(options BumpOptions) time.Time {
	if options.Now != nil {
		return options.Now().UTC()
	}
	return time.Now().UTC()
}

func refreshStaleReleaseEvents(ctx context.Context, events []ReleaseEvent, options BumpOptions) ([]ReleaseEvent, []ReleaseEventRefresh, error) {
	events = append([]ReleaseEvent(nil), events...)
	if options.NoRegistry || options.RefreshAfter == 0 {
		return events, nil, nil
	}
	now := bumpNow(options)
	var refreshes []ReleaseEventRefresh
	for index := range events {
		event := &events[index]
		if event.CheckedAt.IsZero() {
			event.CheckedAt = now
			continue
		}
		if now.Sub(event.CheckedAt) < options.RefreshAfter {
			continue
		}
		latest, err := latestReleaseVersion(ctx, event.Dependency, options)
		if err != nil {
			return events, refreshes, fmt.Errorf("refresh stale release event %s@%s: %w", event.Dependency, event.Version, err)
		}
		if !universalSemverValid(latest) {
			return events, refreshes, fmt.Errorf("refresh stale release event %s@%s: registry returned invalid version %q", event.Dependency, event.Version, latest)
		}
		refresh := ReleaseEventRefresh{
			Dependency: event.Dependency, Before: event.Version, After: event.Version, CheckedAt: now,
			Reason: "registry recheck found no newer release; accumulated event retained",
		}
		if universalSemverCompare(latest, event.Version) > 0 {
			event.Version = latest
			event.Source = "refreshed_latest"
			refresh.After = latest
			refresh.Reason = "newer release substituted before downstream worktree and CI creation"
		}
		event.CheckedAt = now
		refreshes = append(refreshes, refresh)
	}
	return mergeReleaseEvents(events), refreshes, nil
}

func mergeGraphDiscoverySkips(groups ...[]GraphDiscoverySkip) []GraphDiscoverySkip {
	byRepository := map[string]GraphDiscoverySkip{}
	for _, group := range groups {
		for _, skip := range group {
			byRepository[skip.Repository] = skip
		}
	}
	result := make([]GraphDiscoverySkip, 0, len(byRepository))
	for _, skip := range byRepository {
		result = append(result, skip)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Repository < result[j].Repository })
	return result
}

func mergeGraphDefaultBranchFallbacks(groups ...[]GraphDefaultBranchFallback) []GraphDefaultBranchFallback {
	byRepository := map[string]GraphDefaultBranchFallback{}
	for _, group := range groups {
		for _, fallback := range group {
			byRepository[fallback.Repository] = fallback
		}
	}
	result := make([]GraphDefaultBranchFallback, 0, len(byRepository))
	for _, fallback := range byRepository {
		result = append(result, fallback)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Repository < result[j].Repository })
	return result
}

func mergeGraphManifestWarnings(groups ...[]GraphManifestWarning) []GraphManifestWarning {
	type key struct{ repository, manifest string }
	byKey := map[key]GraphManifestWarning{}
	for _, group := range groups {
		for _, warning := range group {
			byKey[key{warning.Repository, warning.Manifest}] = warning
		}
	}
	result := make([]GraphManifestWarning, 0, len(byKey))
	for _, warning := range byKey {
		result = append(result, warning)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Repository == result[j].Repository {
			return result[i].Manifest < result[j].Manifest
		}
		return result[i].Repository < result[j].Repository
	})
	return result
}

func mergeGraphAmbiguousModules(groups ...[]GraphAmbiguousModuleWarning) []GraphAmbiguousModuleWarning {
	byModule := map[string]GraphAmbiguousModuleWarning{}
	for _, group := range groups {
		for _, warning := range group {
			byModule[warning.Module] = warning
		}
	}
	result := make([]GraphAmbiguousModuleWarning, 0, len(byModule))
	for _, warning := range byModule {
		result = append(result, warning)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Module < result[j].Module })
	return result
}

func resumeReleaseObservations(ctx context.Context, previous []ReleaseObservation, options BumpOptions) ([]ReleaseObservation, error) {
	observations := append([]ReleaseObservation(nil), previous...)
	errorsByObservation := make([]error, len(observations))
	workers := options.Parallel
	if workers > len(observations) {
		workers = len(observations)
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				if observations[index].Status == "released" && observations[index].After != "" {
					continue
				}
				if len(observations[index].ExpectedRequirements) > 0 {
					observations[index], errorsByObservation[index] = waitForPublishedGoRequirements(ctx, observations[index], options)
				} else {
					observations[index], errorsByObservation[index] = waitForGoRelease(ctx, observations[index], options)
				}
			}
		}()
	}
	for index := range observations {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	var observationErrors []error
	for _, err := range errorsByObservation {
		if err != nil {
			observationErrors = append(observationErrors, err)
		}
	}
	return observations, errors.Join(observationErrors...)
}

func normalizeBumpOptions(options BumpOptions, events []ReleaseEvent) (BumpOptions, orchestrate.Options, []ReleaseEvent, error) {
	if len(events) == 0 {
		return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("at least one --changed module@version event is required")
	}
	if options.Ecosystem == "" {
		options.Ecosystem = EcosystemGo
	}
	switch options.Ecosystem {
	case EcosystemGo, EcosystemNPM:
	default:
		return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("unsupported dependency ecosystem %q for deps bump (want go or npm)", options.Ecosystem)
	}
	byDependency := map[string]ReleaseEvent{}
	now := bumpNow(options)
	for _, event := range events {
		event.Dependency = strings.TrimSpace(event.Dependency)
		event.Version = strings.TrimSpace(event.Version)
		if options.Ecosystem == EcosystemNPM {
			if err := validateNpmPackageName(event.Dependency); err != nil {
				return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("invalid npm package: %w", err)
			}
		} else if err := modmodule.CheckPath(event.Dependency); err != nil {
			return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("invalid Go module %q: %w", event.Dependency, err)
		}
		if !universalSemverValid(event.Version) {
			return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("invalid %s version %q for %s", options.Ecosystem, event.Version, event.Dependency)
		}
		if event.Source == "" {
			event.Source = "explicit"
		}
		if event.CheckedAt.IsZero() {
			event.CheckedAt = now
		}
		if previous, exists := byDependency[event.Dependency]; exists && previous.Version != event.Version {
			return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("conflicting changed versions for %s: %s and %s", event.Dependency, previous.Version, event.Version)
		}
		if previous, exists := byDependency[event.Dependency]; !exists || event.CheckedAt.After(previous.CheckedAt) {
			byDependency[event.Dependency] = event
		}
	}
	events = events[:0]
	for _, event := range byDependency {
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Dependency < events[j].Dependency })
	if options.MaxWaves == 0 {
		options.MaxWaves = 20
	}
	if options.MaxWaves < 1 {
		return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("max waves must be at least 1")
	}
	if options.PollInterval == 0 {
		options.PollInterval = 30 * time.Second
	}
	if options.PollInterval < 0 {
		return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("release poll interval must not be negative")
	}
	if options.RefreshAfter < 0 {
		return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("release refresh interval must not be negative")
	}
	operation := BumpOperationIDFor(options.Ecosystem, events)
	normalized, lifecycle, err := normalizeOptions(options.Options, operation)
	if err != nil {
		return BumpOptions{}, orchestrate.Options{}, nil, err
	}
	if lifecycle.Resume && options.Previous == nil {
		return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("--resume requires the persisted deps-bump.yaml report")
	}
	if !lifecycle.Resume && options.Previous != nil {
		return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("a previous bump report requires --resume")
	}
	options.Options = normalized
	lifecycle.Branch = "wb/deps/bump-" + strings.TrimPrefix(operation, bumpOperationPrefix(options.Ecosystem))
	return options, lifecycle, events, nil
}

// bumpOperationPrefix returns the operation-identity prefix for one
// ecosystem's campaigns, e.g. "deps-bump-go-" or "deps-bump-npm-".
func bumpOperationPrefix(ecosystem Ecosystem) string {
	if ecosystem == "" {
		ecosystem = EcosystemGo
	}
	return "deps-bump-" + string(ecosystem) + "-"
}

// BumpOperationID returns the stable Go campaign identity for a sorted seed
// set. Kept for backward compatibility with every caller that predates npm
// support; new callers that also know the ecosystem should use
// BumpOperationIDFor.
func BumpOperationID(events []ReleaseEvent) string {
	return BumpOperationIDFor(EcosystemGo, events)
}

// BumpOperationIDFor returns the stable campaign identity for a sorted seed
// set of release events in the given ecosystem.
func BumpOperationIDFor(ecosystem Ecosystem, events []ReleaseEvent) string {
	events = append([]ReleaseEvent(nil), events...)
	sort.Slice(events, func(i, j int) bool { return events[i].Dependency < events[j].Dependency })
	var identity strings.Builder
	for _, event := range events {
		identity.WriteString(event.Dependency)
		identity.WriteByte('@')
		identity.WriteString(event.Version)
		identity.WriteByte('\n')
	}
	digest := sha256.Sum256([]byte(identity.String()))
	return bumpOperationPrefix(ecosystem) + hex.EncodeToString(digest[:6])
}

// bumpWavePrompt renders the root release events a wave is propagating as
// the originating instruction recorded in every wave worktree's WB manifest
// journal (see orchestrate.Options.Prompt and
// internal/worktrees.CheckAdmission) — the exact record wb's own
// commit-admission hook requires before it will accept a commit into a
// worktree wb itself created.
func bumpWavePrompt(ecosystem Ecosystem, waveIndex int, seedEvents []ReleaseEvent) string {
	events := append([]ReleaseEvent(nil), seedEvents...)
	sort.Slice(events, func(i, j int) bool { return events[i].Dependency < events[j].Dependency })
	releases := make([]string, 0, len(events))
	for _, event := range events {
		releases = append(releases, event.Dependency+"@"+event.Version)
	}
	return fmt.Sprintf(
		"wb deps bump %s wave %d: propagate root release event(s) %s through the dependency fleet.",
		ecosystem, waveIndex, strings.Join(releases, ", "),
	)
}

func selectWaveRepositories(repositories []Repository, targets map[string][]Target) []Repository {
	selected := make([]Repository, 0, len(targets))
	for _, repository := range repositories {
		if len(targets[repository.Slug]) > 0 {
			selected = append(selected, repository)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Slug < selected[j].Slug })
	return selected
}

// waveHandler drives one wave's per-repository Inspect/Apply lifecycle
// through the same adapter deps set already uses (see adapter.go), so a
// bump wave and an exact set share their manifest-mutation logic exactly.
// It replaces the former Go-only goWaveHandler; nothing in this type is
// Go-specific anymore, and every Go-only step below stays gated on
// handler.ecosystem so Go-adapter behavior is unchanged.
type waveHandler struct {
	ecosystem           Ecosystem
	targetsByRepository map[string][]Target
	options             Options
	// versionPlanID is the deterministic identity of this candidate wave. It
	// is empty outside RunBump's wave lifecycle, so exact deps set remains
	// deliberately unaffected by Nx version-plan generation.
	versionPlanID string
}

func (handler waveHandler) Inspect(ctx context.Context, canonical, base string, repository orchestrate.Repository) (orchestrate.Assessment[[]Decision], error) {
	assessment := orchestrate.Assessment[[]Decision]{}
	adapter := adapterFor(handler.ecosystem)
	for _, target := range handler.targetsByRepository[repository.Slug] {
		decisions, err := adapter.inspect(ctx, canonical, base, target, handler.options)
		assessment.Metadata = append(assessment.Metadata, decisions...)
		if err != nil {
			sortDecisions(assessment.Metadata)
			return assessment, err
		}
		if len(decisions) > 0 {
			assessment.Applicable = true
		}
		for _, decision := range decisions {
			if decision.Action != "unchanged" {
				assessment.NeedsChange = true
			}
		}
	}
	sortDecisions(assessment.Metadata)
	if !assessment.Applicable {
		assessment.Reason = "release events are absent from repository manifests"
	} else if assessment.NeedsChange {
		assessment.Reason = "published provider events require a dependency wave update"
	} else {
		assessment.Reason = "all release events are already selected"
	}
	return assessment, nil
}

func (handler waveHandler) Apply(ctx context.Context, worktree string, repository orchestrate.Repository) ([]Decision, error) {
	adapter := adapterFor(handler.ecosystem)
	var decisions []Decision
	var updateErrors []error
	for _, target := range handler.targetsByRepository[repository.Slug] {
		updated, err := adapter.apply(ctx, worktree, target, handler.options)
		decisions = append(decisions, updated...)
		if err != nil {
			updateErrors = append(updateErrors, err)
		}
	}
	// Only Go's module resolution can let a later target silently change an
	// earlier one (minimal version selection); npm's exact-literal writes
	// have no equivalent resolver step to re-check.
	if handler.ecosystem != EcosystemNPM {
		if validationErrors := validateGoWaveSelections(ctx, worktree, decisions, handler.options); validationErrors != nil {
			updateErrors = append(updateErrors, validationErrors)
		}
	}
	if handler.ecosystem == EcosystemNPM && len(updateErrors) == 0 {
		if err := generateNxVersionPlan(ctx, worktree, handler.versionPlanID, decisions, handler.options); err != nil {
			updateErrors = append(updateErrors, err)
		}
	}
	sortDecisions(decisions)
	return decisions, errors.Join(updateErrors...)
}

func (handler waveHandler) ValidatePublishable(_ context.Context, worktree string, _ orchestrate.Repository) error {
	if handler.ecosystem != EcosystemGo && handler.ecosystem != "" {
		return nil
	}
	return validatePublishableGoManifests(worktree)
}

// validateGoWaveSelections checks the complete target set after every go get
// and tidy has run. A later target can otherwise change an earlier target via
// minimal version selection while each individual adapter call appears valid.
func validateGoWaveSelections(ctx context.Context, worktree string, decisions []Decision, options Options) error {
	var validationErrors []error
	for index := range decisions {
		decision := &decisions[index]
		if decision.Dependency == "" || decision.Action == "failed" || decision.Action == "blocked_downgrade" {
			continue
		}
		moduleDir := filepath.Join(worktree, filepath.Dir(filepath.FromSlash(decision.File)))
		selected, _, err := runGoCommand(ctx, options, moduleDir, "list", "-m", "-f", "{{.Version}}", decision.Dependency)
		if err != nil {
			decision.Action = "failed"
			decision.Reason = "final wave validation failed: " + err.Error()
			validationErrors = append(validationErrors, fmt.Errorf("%s: final selection for %s: %w", decision.File, decision.Dependency, err))
			continue
		}
		selected = strings.TrimSpace(selected)
		decision.AfterRef = selected
		decision.AfterVersion = selected
		if selected == decision.TargetVersion {
			continue
		}
		decision.Action = "failed"
		decision.Reason = fmt.Sprintf("final Go module selection produced %s instead of exact wave target %s", selected, decision.TargetVersion)
		validationErrors = append(validationErrors, fmt.Errorf("%s: %s selected %s; want %s", decision.File, decision.Dependency, selected, decision.TargetVersion))
	}
	return errors.Join(validationErrors...)
}

func (handler waveHandler) CommitMessage(repository orchestrate.Repository) string {
	targets := handler.targetsByRepository[repository.Slug]
	if len(targets) == 1 {
		return fmt.Sprintf("chore(deps): bump %s to %s", targets[0].Dependency, targets[0].Version)
	}
	return "chore(deps): apply dependency release wave"
}

func (handler waveHandler) PullRequest(repository orchestrate.Repository) (string, string) {
	title := handler.CommitMessage(repository)
	return title, fmt.Sprintf("Automated by `wb deps bump`. Published provider versions were applied with %s tooling and local verification completed before this pull request was opened.", handler.ecosystem)
}

func captureReleaseBaselines(ctx context.Context, graph bumpFleetGraph, affected map[string]map[string]bool, options BumpOptions) (map[string]ReleaseObservation, error) {
	observations := map[string]ReleaseObservation{}
	var observationErrors []error
	for repository, modules := range affected {
		for module := range modules {
			if !graph.hasExternalConsumers(module, repository) {
				continue
			}
			version, err := latestReleaseVersion(ctx, module, options)
			observation := ReleaseObservation{
				Module: module, Repository: repository, Before: version, Source: latestVersionCommandDescription(options.Ecosystem, module),
				Status: "baseline", RequireNewer: true, CheckedAt: bumpNow(options),
			}
			if err != nil {
				observation.Status = "failed"
				observation.Reason = err.Error()
				observationErrors = append(observationErrors, fmt.Errorf("observe baseline release for %s: %w", module, err))
			} else {
				observation.Reason = "latest published version captured before wave merge"
			}
			observations[module] = observation
		}
	}
	return observations, errors.Join(observationErrors...)
}

func mergedReleaseBaselines(results []orchestrate.Result[[]Decision], affected map[string]map[string]bool, baselines map[string]ReleaseObservation) []ReleaseObservation {
	merged := map[string]bool{}
	for _, result := range results {
		if result.Merged {
			merged[result.Repository] = true
		}
	}
	observations := make([]ReleaseObservation, 0, len(baselines))
	for module, baseline := range baselines {
		if merged[baseline.Repository] && affected[baseline.Repository][module] {
			observations = append(observations, baseline)
		}
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].Module < observations[j].Module })
	return observations
}

// discoverExistingReleaseCarriers lets a later campaign traverse a consumer
// that origin/main and the module registry already show as updated. Both
// pieces of evidence are required: a source manifest alone does not prove that
// dependants can consume a published version.
func discoverExistingReleaseCarriers(ctx context.Context, graph bumpFleetGraph, events []ReleaseEvent, options BumpOptions) ([]ReleaseObservation, error) {
	if options.NoRegistry {
		// A manifest that is already current cannot be treated as a published
		// carrier without registry evidence. Leave it out of this plan rather
		// than consulting pnpm/npm or inventing a downstream release.
		return nil, nil
	}
	expectedByModule := map[string]map[string]string{}
	repositoryByModule := map[string]string{}
	for _, event := range events {
		for _, requirement := range graph.requirementsForDependency(event.Dependency) {
			if requirement.Version != event.Version || !graph.hasExternalConsumers(requirement.ConsumerModule, requirement.Repository) {
				continue
			}
			if expectedByModule[requirement.ConsumerModule] == nil {
				expectedByModule[requirement.ConsumerModule] = map[string]string{}
			}
			expectedByModule[requirement.ConsumerModule][event.Dependency] = event.Version
			repositoryByModule[requirement.ConsumerModule] = requirement.Repository
		}
	}
	modules := make([]string, 0, len(expectedByModule))
	for module := range expectedByModule {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	observations := make([]ReleaseObservation, len(modules))
	errorsByModule := make([]error, len(modules))
	workers := options.Parallel
	if workers > len(modules) {
		workers = len(modules)
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				module := modules[index]
				expected := expectedByModule[module]
				release, err := latestPublishedRelease(ctx, module, options)
				observation := ReleaseObservation{
					Module: module, Repository: repositoryByModule[module], Source: release.Source,
					ExpectedRequirements: cloneStringMap(expected), Status: "awaiting_release", CheckedAt: bumpNow(options),
				}
				if observation.Source == "" {
					observation.Source = latestPublishedReleaseCommandDescription(options.Ecosystem, module)
				}
				if err != nil {
					observation.Status = "failed"
					observation.Reason = err.Error()
					errorsByModule[index] = fmt.Errorf("inspect published release for %s: %w", module, err)
				} else if requirementsContain(release.Requirements, expected) {
					observation.Before = release.Version
					observation.After = release.Version
					observation.Status = "released"
					observation.Reason = "existing published consumer release selects every current provider event"
				} else {
					observation.Before = release.Version
					observation.Reason = "origin manifest is current but the latest published consumer release does not select every provider event"
					errorsByModule[index] = fmt.Errorf("%s latest release %s does not contain the dependency versions selected on origin/%s", module, release.Version, options.Ref)
				}
				observations[index] = observation
			}
		}()
	}
	for index := range modules {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	var discoveryErrors []error
	for _, err := range errorsByModule {
		if err != nil {
			discoveryErrors = append(discoveryErrors, err)
		}
	}
	return observations, errors.Join(discoveryErrors...)
}

func latestPublishedGoRelease(ctx context.Context, module string, options BumpOptions) (PublishedGoRelease, error) {
	if options.LatestGoRelease != nil {
		release, err := options.LatestGoRelease(ctx, module)
		if err != nil {
			return PublishedGoRelease{}, err
		}
		if !semver.IsValid(release.Version) {
			return PublishedGoRelease{}, fmt.Errorf("latest Go version for %s is invalid: %q", module, release.Version)
		}
		if release.Source == "" {
			release.Source = "injected release resolver for " + module
		}
		return release, nil
	}
	output, _, err := runGoCommand(ctx, options.Options, options.GitHubDir, "mod", "download", "-json", module+"@latest")
	if err != nil {
		return PublishedGoRelease{}, err
	}
	var downloaded struct {
		Version string
		GoMod   string
		Error   string
	}
	if err := json.Unmarshal([]byte(output), &downloaded); err != nil {
		return PublishedGoRelease{}, fmt.Errorf("decode published Go release for %s: %w", module, err)
	}
	if downloaded.Error != "" {
		return PublishedGoRelease{}, fmt.Errorf("download published Go release for %s: %s", module, downloaded.Error)
	}
	if !semver.IsValid(downloaded.Version) {
		return PublishedGoRelease{}, fmt.Errorf("latest Go version for %s is invalid: %q", module, downloaded.Version)
	}
	contents, err := os.ReadFile(downloaded.GoMod)
	if err != nil {
		return PublishedGoRelease{}, fmt.Errorf("read published go.mod for %s@%s: %w", module, downloaded.Version, err)
	}
	parsed, err := modfile.Parse(downloaded.GoMod, contents, nil)
	if err != nil {
		return PublishedGoRelease{}, fmt.Errorf("parse published go.mod for %s@%s: %w", module, downloaded.Version, err)
	}
	requirements := make(map[string]string, len(parsed.Require))
	for _, requirement := range parsed.Require {
		requirements[requirement.Mod.Path] = requirement.Mod.Version
	}
	return PublishedGoRelease{
		Version: downloaded.Version, Requirements: requirements,
		Source: "go mod download " + module + "@" + downloaded.Version,
	}, nil
}

func waitForPublishedGoRequirements(ctx context.Context, baseline ReleaseObservation, options BumpOptions) (ReleaseObservation, error) {
	observation := baseline
	observation.Status = "awaiting_release"
	deadline := time.Time{}
	if options.Timeout > 0 {
		deadline = time.Now().Add(options.Timeout)
	}
	for {
		release, err := latestPublishedRelease(ctx, baseline.Module, options)
		observation.CheckedAt = bumpNow(options)
		if err != nil {
			observation.Status = "failed"
			observation.Reason = err.Error()
			return observation, err
		}
		if release.Source != "" {
			observation.Source = release.Source
		}
		versionReady := baseline.Before == "" || universalSemverCompare(release.Version, baseline.Before) >= 0
		if baseline.RequireNewer && baseline.Before != "" {
			versionReady = universalSemverCompare(release.Version, baseline.Before) > 0
		}
		if requirementsContain(release.Requirements, baseline.ExpectedRequirements) && versionReady {
			observation.After = release.Version
			observation.Status = "released"
			observation.Reason = "published consumer release selects every current provider event"
			return observation, nil
		}
		observation.Reason = "waiting for a published consumer release that selects every current provider event"
		if !deadline.IsZero() && time.Now().After(deadline) {
			return observation, fmt.Errorf("release for %s did not publish expected dependency versions before timeout", baseline.Module)
		}
		select {
		case <-ctx.Done():
			return observation, ctx.Err()
		case <-time.After(options.PollInterval):
		}
	}
}

func requirementsContain(actual, expected map[string]string) bool {
	for dependency, version := range expected {
		if actual[dependency] != version {
			return false
		}
	}
	return true
}

func cloneStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

// latestReleaseVersion and latestPublishedRelease dispatch to each
// ecosystem's own registry lookup. They are the single seam every shared
// wave-engine helper above (refreshStaleReleaseEvents, captureReleaseBaselines,
// discoverExistingReleaseCarriers, waitForGoRelease, waitForPublishedGoRequirements)
// calls through, so none of them need to know which ecosystem is running —
// exactly like adapterFor is the seam Inspect/Apply call through.
func latestReleaseVersion(ctx context.Context, module string, options BumpOptions) (string, error) {
	if options.NoRegistry {
		return "", errors.New("registry lookup is disabled for this dependency plan")
	}
	if options.Ecosystem == EcosystemNPM {
		return latestNpmVersion(ctx, module, options)
	}
	return latestGoVersion(ctx, module, options)
}

func latestPublishedRelease(ctx context.Context, module string, options BumpOptions) (PublishedGoRelease, error) {
	if options.NoRegistry {
		return PublishedGoRelease{}, errors.New("registry lookup is disabled for this dependency plan")
	}
	if options.Ecosystem == EcosystemNPM {
		return latestPublishedNpmRelease(ctx, module, options)
	}
	return latestPublishedGoRelease(ctx, module, options)
}

func latestVersionCommandDescription(ecosystem Ecosystem, module string) string {
	if ecosystem == EcosystemNPM {
		return "pnpm view " + module + " version"
	}
	return "go list -m " + module + "@latest"
}

func latestPublishedReleaseCommandDescription(ecosystem Ecosystem, module string) string {
	if ecosystem == EcosystemNPM {
		return "pnpm view " + module + "@latest " + strings.Join(npmDependencyFieldNames, " ")
	}
	return "go mod download " + module + "@latest"
}

// latestNpmVersion is the npm-ecosystem analogue of latestGoVersion: the
// published "latest" dist-tag version of a package, via `pnpm view`. pnpm is
// used rather than npm because every repository this adapter targets is a
// pnpm workspace (Corepack-pinned pnpm is guaranteed present; a bare `npm`
// binary is not).
func latestNpmVersion(ctx context.Context, module string, options BumpOptions) (string, error) {
	if options.LatestNpmVersion != nil {
		return options.LatestNpmVersion(ctx, module)
	}
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, options.GitHubDir, "pnpm", "view", module, "version")
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(output)
	if !universalSemverValid(version) {
		return "", fmt.Errorf("latest npm version for %s is invalid: %q", module, version)
	}
	return version, nil
}

// latestPublishedNpmRelease is the npm-ecosystem analogue of
// latestPublishedGoRelease: the published "latest" release's own version and
// dependency requirements, used to let a campaign traverse a consumer that
// origin/<ref> and the registry both already show as updated without
// inventing a version WB never observed.
func latestPublishedNpmRelease(ctx context.Context, module string, options BumpOptions) (PublishedGoRelease, error) {
	if options.LatestNpmRelease != nil {
		release, err := options.LatestNpmRelease(ctx, module)
		if err != nil {
			return PublishedGoRelease{}, err
		}
		if !universalSemverValid(release.Version) {
			return PublishedGoRelease{}, fmt.Errorf("latest npm version for %s is invalid: %q", module, release.Version)
		}
		if release.Source == "" {
			release.Source = "injected release resolver for " + module
		}
		return release, nil
	}
	version, err := latestNpmVersion(ctx, module, options)
	if err != nil {
		return PublishedGoRelease{}, err
	}
	arguments := append([]string{"view", module + "@" + version}, npmDependencyFieldNames...)
	arguments = append(arguments, "--json")
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, options.GitHubDir, "pnpm", arguments...)
	if err != nil {
		return PublishedGoRelease{}, err
	}
	requirements, err := parsePublishedNpmRequirements(output)
	if err != nil {
		return PublishedGoRelease{}, fmt.Errorf("decode published npm dependency fields for %s@%s: %w", module, version, err)
	}
	return PublishedGoRelease{
		Version: version, Requirements: requirements,
		Source: "pnpm view " + module + "@" + version + " " + strings.Join(npmDependencyFieldNames, " "),
	}, nil
}

// parsePublishedNpmRequirements merges the same dependency fields discovery
// treats as provider selections. A package that declares one dependency at
// conflicting exact values in two fields is ambiguous, so verification stops
// instead of accepting one field arbitrarily.
func parsePublishedNpmRequirements(output string) (map[string]string, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || trimmed == "undefined" {
		return map[string]string{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		return nil, err
	}
	requirements := map[string]string{}
	for _, field := range npmDependencyFieldNames {
		raw, exists := fields[field]
		if !exists || string(raw) == "null" {
			continue
		}
		var section map[string]string
		if err := json.Unmarshal(raw, &section); err != nil {
			return nil, fmt.Errorf("decode %s: %w", field, err)
		}
		for dependency, version := range section {
			if selected, exists := requirements[dependency]; exists && selected != version {
				return nil, fmt.Errorf("conflicting published npm selections for %s: %s and %s", dependency, selected, version)
			}
			requirements[dependency] = version
		}
	}
	return requirements, nil
}

func latestGoVersion(ctx context.Context, module string, options BumpOptions) (string, error) {
	if options.LatestGoVersion != nil {
		return options.LatestGoVersion(ctx, module)
	}
	output, _, err := runGoCommand(ctx, options.Options, options.GitHubDir, "list", "-m", "-json", module+"@latest")
	if err != nil {
		return "", err
	}
	var result struct{ Version string }
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return "", fmt.Errorf("decode latest Go version for %s: %w", module, err)
	}
	if !semver.IsValid(result.Version) {
		return "", fmt.Errorf("latest Go version for %s is invalid: %q", module, result.Version)
	}
	return result.Version, nil
}

func waitForGoRelease(ctx context.Context, baseline ReleaseObservation, options BumpOptions) (ReleaseObservation, error) {
	observation := baseline
	observation.Status = "awaiting_release"
	observation.Reason = "waiting for a version newer than " + baseline.Before
	deadline := time.Time{}
	if options.Timeout > 0 {
		deadline = time.Now().Add(options.Timeout)
	}
	for {
		version, err := latestReleaseVersion(ctx, baseline.Module, options)
		observation.CheckedAt = bumpNow(options)
		if err != nil {
			observation.Status = "failed"
			observation.Reason = err.Error()
			return observation, err
		}
		if baseline.Before == "" || universalSemverCompare(version, baseline.Before) > 0 {
			observation.After = version
			observation.Status = "released"
			observation.Reason = "new published provider version observed after merge"
			return observation, nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return observation, fmt.Errorf("release for %s did not advance beyond %s before timeout", baseline.Module, baseline.Before)
		}
		select {
		case <-ctx.Done():
			return observation, ctx.Err()
		case <-time.After(options.PollInterval):
		}
	}
}

func releaseEventsFromObservations(observations []ReleaseObservation) []ReleaseEvent {
	var events []ReleaseEvent
	for _, observation := range observations {
		if observation.Status == "released" && observation.After != "" {
			source := "observed_release"
			if len(observation.ExpectedRequirements) > 0 && !observation.RequireNewer {
				source = "existing_release"
			}
			events = append(events, ReleaseEvent{
				Dependency: observation.Module, Version: observation.After, Source: source, CheckedAt: observation.CheckedAt,
			})
		}
	}
	return mergeReleaseEvents(events)
}

func mergeReleaseEvents(groups ...[]ReleaseEvent) []ReleaseEvent {
	byDependency := map[string]ReleaseEvent{}
	for _, events := range groups {
		for _, event := range events {
			previous, exists := byDependency[event.Dependency]
			if !exists || universalSemverCompare(event.Version, previous.Version) > 0 {
				byDependency[event.Dependency] = event
			} else if event.Version == previous.Version && event.CheckedAt.After(previous.CheckedAt) {
				byDependency[event.Dependency] = event
			}
		}
	}
	result := make([]ReleaseEvent, 0, len(byDependency))
	for _, event := range byDependency {
		result = append(result, event)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Dependency < result[j].Dependency })
	return result
}

func mergeReleaseObservations(groups ...[]ReleaseObservation) []ReleaseObservation {
	byModule := map[string]ReleaseObservation{}
	for _, observations := range groups {
		for _, observation := range observations {
			previous, exists := byModule[observation.Module]
			if !exists {
				observation.ExpectedRequirements = cloneStringMap(observation.ExpectedRequirements)
				byModule[observation.Module] = observation
				continue
			}
			for dependency, version := range observation.ExpectedRequirements {
				if previous.ExpectedRequirements == nil {
					previous.ExpectedRequirements = map[string]string{}
				}
				previous.ExpectedRequirements[dependency] = version
			}
			if observation.RequireNewer {
				previous.Before = observation.Before
				previous.After = ""
				previous.Source = observation.Source
				previous.Status = observation.Status
				previous.Reason = observation.Reason
				previous.RequireNewer = true
			} else if !previous.RequireNewer && observation.Status == "released" {
				previous.After = observation.After
				previous.Status = observation.Status
				previous.Reason = observation.Reason
			}
			if observation.CheckedAt.After(previous.CheckedAt) {
				previous.CheckedAt = observation.CheckedAt
			}
			byModule[observation.Module] = previous
		}
	}
	return sortedReleaseObservations(byModule)
}

func sortedReleaseObservations(values map[string]ReleaseObservation) []ReleaseObservation {
	result := make([]ReleaseObservation, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Module < result[j].Module })
	return result
}

func persistBumpReport(options BumpOptions, report BumpReport) error {
	if options.Persist != nil {
		return options.Persist(report)
	}
	return nil
}

func persistBumpFailure(options BumpOptions, report BumpReport, cause error) error {
	if persistErr := persistBumpReport(options, report); persistErr != nil {
		return errors.Join(cause, fmt.Errorf("persist dependency bump state: %w", persistErr))
	}
	return cause
}
