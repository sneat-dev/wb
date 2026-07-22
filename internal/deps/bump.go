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
		lock, lockErr := orchestrate.AcquireOperationLock(lifecycle.GitHubDir, lifecycle.Operation)
		if lockErr != nil {
			return BumpReport{}, lockErr
		}
		defer lock.Release()
	}
	report := BumpReport{
		SchemaVersion: 1, Operation: lifecycle.Operation, Status: "running", Phase: BumpPhasePreparing,
		Ecosystem: EcosystemGo, SeedEvents: append([]ReleaseEvent(nil), events...),
		GitHubDir: lifecycle.GitHubDir, BaseRef: lifecycle.Ref, Parallel: lifecycle.Parallel,
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
	for waveIndex := startWave; waveIndex <= options.MaxWaves; waveIndex++ {
		report.Phase = BumpPhaseDiscoveringGraph
		report.Progress = BumpProgress{Wave: waveIndex, RepositoriesTotal: len(repositories)}
		if err := persistBumpReport(options, report); err != nil {
			return report, err
		}
		var progressMu sync.Mutex
		var progressErr error
		graph, err := discoverGoFleetGraph(ctx, repositories, lifecycle, func(progress graphDiscoveryProgress) {
			progressMu.Lock()
			defer progressMu.Unlock()
			if progressErr != nil {
				return
			}
			report.Progress = BumpProgress{
				Wave:                  waveIndex,
				RepositoriesTotal:     progress.RepositoriesTotal,
				RepositoriesCompleted: progress.RepositoriesCompleted,
				LastRepository:        progress.LastRepository,
			}
			progressErr = persistBumpReport(options, report)
		})
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
		if err := graph.validateAcyclicPropagation(events); err != nil {
			report.Status = "failed"
			return report, persistBumpFailure(options, report, err)
		}
		report.Phase = BumpPhasePlanningWave
		if err := persistBumpReport(options, report); err != nil {
			return report, err
		}
		carriers, carrierErr := discoverExistingReleaseCarriers(ctx, graph, events, options)
		targetsByRepository := graph.repositoriesForEvents(events)
		if len(targetsByRepository) == 0 {
			if len(carriers) > 0 {
				wave := BumpWaveReport{Index: waveIndex, Status: "completed", Events: append([]ReleaseEvent(nil), events...), Releases: carriers}
				if lifecycle.DryRun {
					if carrierErr != nil {
						wave.Status = "planned"
						report.Waves = append(report.Waves, wave)
						report.Status = "planned"
						report.Phase = BumpPhasePlanned
						if persistErr := persistBumpReport(options, report); persistErr != nil {
							return report, persistErr
						}
						return report, nil
					}
					report.Waves = append(report.Waves, wave)
					events = releaseEventsFromObservations(carriers)
					if len(events) > 0 {
						continue
					}
				}
				if carrierErr != nil {
					carriers, carrierErr = resumeReleaseObservations(ctx, carriers, options)
					wave.Releases = carriers
				}
				if carrierErr != nil {
					wave.Status = "awaiting_release"
					report.Waves = append(report.Waves, wave)
					report.Status = "awaiting_release"
					report.Phase = BumpPhaseAwaitingRelease
					return report, persistBumpFailure(options, report, carrierErr)
				}
				report.Waves = append(report.Waves, wave)
				events = releaseEventsFromObservations(carriers)
				if persistErr := persistBumpReport(options, report); persistErr != nil {
					return report, persistErr
				}
				if len(events) > 0 {
					continue
				}
			}
			report.Status = "completed"
			report.Phase = BumpPhaseCompleted
			if persistErr := persistBumpReport(options, report); persistErr != nil {
				return report, persistErr
			}
			return report, nil
		}
		wave := BumpWaveReport{Index: waveIndex, Status: "running", Events: append([]ReleaseEvent(nil), events...), Releases: carriers}
		affectedRepositories := selectWaveRepositories(repositories, targetsByRepository)
		affectedModules := graph.affectedModules(events)
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
		report.Progress = BumpProgress{Wave: waveIndex, RepositoriesTotal: len(affectedRepositories)}
		if persistErr := persistBumpReport(options, report); persistErr != nil {
			return report, persistErr
		}
		waveLifecycle := lifecycle
		waveLifecycle.Operation = fmt.Sprintf("%s-wave-%02d", lifecycle.Operation, waveIndex)
		waveLifecycle.Branch = fmt.Sprintf("wb/deps/bump-%s-wave-%02d", strings.TrimPrefix(lifecycle.Operation, "deps-bump-go-"), waveIndex)
		handler := goWaveHandler{targetsByRepository: targetsByRepository, options: options.Options}
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
		events = releaseEventsFromObservations(waveReport.Releases)
		if len(events) == 0 {
			report.Status = "completed"
			report.Phase = BumpPhaseCompleted
			if persistErr := persistBumpReport(options, report); persistErr != nil {
				return report, persistErr
			}
			return report, nil
		}
	}
	report.Status = "failed"
	return report, persistBumpFailure(options, report, fmt.Errorf("dependency bump exceeded --max-waves=%d", options.MaxWaves))
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
		events := releaseEventsFromObservations(observations)
		if len(events) == 0 {
			previous.Status = "completed"
		}
		return previous, events, last.Index + 1, persistBumpReport(options, previous)
	case "completed":
		events := releaseEventsFromObservations(last.Releases)
		if len(events) == 0 {
			previous.Status = "completed"
		}
		return previous, events, last.Index + 1, nil
	default:
		events := append([]ReleaseEvent(nil), last.Events...)
		previous.Waves = previous.Waves[:lastIndex]
		return previous, events, last.Index, nil
	}
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
	byDependency := map[string]ReleaseEvent{}
	for _, event := range events {
		event.Dependency = strings.TrimSpace(event.Dependency)
		event.Version = strings.TrimSpace(event.Version)
		if err := modmodule.CheckPath(event.Dependency); err != nil {
			return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("invalid Go module %q: %w", event.Dependency, err)
		}
		if !semver.IsValid(event.Version) {
			return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("invalid Go module version %q for %s", event.Version, event.Dependency)
		}
		if event.Source == "" {
			event.Source = "explicit"
		}
		if previous, exists := byDependency[event.Dependency]; exists && previous.Version != event.Version {
			return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("conflicting changed versions for %s: %s and %s", event.Dependency, previous.Version, event.Version)
		}
		byDependency[event.Dependency] = event
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
		options.PollInterval = 10 * time.Second
	}
	if options.PollInterval < 0 {
		return BumpOptions{}, orchestrate.Options{}, nil, fmt.Errorf("release poll interval must not be negative")
	}
	operation := BumpOperationID(events)
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
	lifecycle.Branch = "wb/deps/bump-" + strings.TrimPrefix(operation, "deps-bump-go-")
	return options, lifecycle, events, nil
}

// BumpOperationID returns the stable campaign identity for a sorted seed set.
func BumpOperationID(events []ReleaseEvent) string {
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
	return "deps-bump-go-" + hex.EncodeToString(digest[:6])
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

type goWaveHandler struct {
	targetsByRepository map[string][]Target
	options             Options
}

func (handler goWaveHandler) Inspect(ctx context.Context, canonical, base string, repository orchestrate.Repository) (orchestrate.Assessment[[]Decision], error) {
	assessment := orchestrate.Assessment[[]Decision]{}
	adapter := goAdapter{}
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

func (handler goWaveHandler) Apply(ctx context.Context, worktree string, repository orchestrate.Repository) ([]Decision, error) {
	adapter := goAdapter{}
	var decisions []Decision
	var updateErrors []error
	for _, target := range handler.targetsByRepository[repository.Slug] {
		updated, err := adapter.apply(ctx, worktree, target, handler.options)
		decisions = append(decisions, updated...)
		if err != nil {
			updateErrors = append(updateErrors, err)
		}
	}
	if validationErrors := validateGoWaveSelections(ctx, worktree, decisions, handler.options); validationErrors != nil {
		updateErrors = append(updateErrors, validationErrors)
	}
	sortDecisions(decisions)
	return decisions, errors.Join(updateErrors...)
}

func (goWaveHandler) ValidatePublishable(_ context.Context, worktree string, _ orchestrate.Repository) error {
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
		selected, _, err := runCommand(ctx, options.Timeout, options.Retry, moduleDir, "go", "list", "-m", "-f", "{{.Version}}", decision.Dependency)
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

func (handler goWaveHandler) CommitMessage(repository orchestrate.Repository) string {
	targets := handler.targetsByRepository[repository.Slug]
	if len(targets) == 1 {
		return fmt.Sprintf("chore(deps): bump %s to %s", targets[0].Dependency, targets[0].Version)
	}
	return "chore(deps): apply dependency release wave"
}

func (handler goWaveHandler) PullRequest(repository orchestrate.Repository) (string, string) {
	title := handler.CommitMessage(repository)
	return title, "Automated by `wb deps bump`. Published provider versions were applied with Go tooling and local verification completed before this pull request was opened."
}

func captureReleaseBaselines(ctx context.Context, graph goFleetGraph, affected map[string]map[string]bool, options BumpOptions) (map[string]ReleaseObservation, error) {
	observations := map[string]ReleaseObservation{}
	var observationErrors []error
	for repository, modules := range affected {
		for module := range modules {
			if !graph.hasExternalConsumers(module, repository) {
				continue
			}
			version, err := latestGoVersion(ctx, module, options)
			observation := ReleaseObservation{Module: module, Repository: repository, Before: version, Source: "go list -m " + module + "@latest", Status: "baseline", RequireNewer: true}
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
func discoverExistingReleaseCarriers(ctx context.Context, graph goFleetGraph, events []ReleaseEvent, options BumpOptions) ([]ReleaseObservation, error) {
	expectedByModule := map[string]map[string]string{}
	repositoryByModule := map[string]string{}
	for _, event := range events {
		for _, requirement := range graph.requirements[event.Dependency] {
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
				release, err := latestPublishedGoRelease(ctx, module, options)
				observation := ReleaseObservation{
					Module: module, Repository: repositoryByModule[module], Source: release.Source,
					ExpectedRequirements: cloneStringMap(expected), Status: "awaiting_release",
				}
				if observation.Source == "" {
					observation.Source = "go mod download " + module + "@latest"
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
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, options.GitHubDir, "go", "mod", "download", "-json", module+"@latest")
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
		release, err := latestPublishedGoRelease(ctx, baseline.Module, options)
		if err != nil {
			observation.Status = "failed"
			observation.Reason = err.Error()
			return observation, err
		}
		if release.Source != "" {
			observation.Source = release.Source
		}
		versionReady := baseline.Before == "" || semver.Compare(release.Version, baseline.Before) >= 0
		if baseline.RequireNewer && baseline.Before != "" {
			versionReady = semver.Compare(release.Version, baseline.Before) > 0
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

func latestGoVersion(ctx context.Context, module string, options BumpOptions) (string, error) {
	if options.LatestGoVersion != nil {
		return options.LatestGoVersion(ctx, module)
	}
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, options.GitHubDir, "go", "list", "-m", "-json", module+"@latest")
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
		version, err := latestGoVersion(ctx, baseline.Module, options)
		if err != nil {
			observation.Status = "failed"
			observation.Reason = err.Error()
			return observation, err
		}
		if baseline.Before == "" || semver.Compare(version, baseline.Before) > 0 {
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
			events = append(events, ReleaseEvent{Dependency: observation.Module, Version: observation.After, Source: source})
		}
	}
	return mergeReleaseEvents(events)
}

func mergeReleaseEvents(groups ...[]ReleaseEvent) []ReleaseEvent {
	byDependency := map[string]ReleaseEvent{}
	for _, events := range groups {
		for _, event := range events {
			previous, exists := byDependency[event.Dependency]
			if !exists || semver.Compare(event.Version, previous.Version) > 0 {
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
