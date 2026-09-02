package deps

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"golang.org/x/mod/module"
)

// AnalyzeDrift builds a read-only Go dependency convergence report for the
// selected repositories. It never mutates manifests or contacts a registry
// unless options.Online is set.
func AnalyzeDrift(ctx context.Context, repositories []Repository, options DriftOptions) (DriftReport, error) {
	progress.Report(options.Progress, progress.Event{Operation: "deps drift", Phase: "inspect", State: progress.Started, Total: len(repositories)})
	if options.Parallel < 1 {
		options.Parallel = 1
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	observedAt := now().UTC()
	mode := "offline"
	if options.Online {
		mode = "online"
	}
	if options.Ecosystem == "" {
		options.Ecosystem = EcosystemGo
	}
	switch options.Ecosystem {
	case EcosystemGo, EcosystemNPM:
	default:
		return DriftReport{}, fmt.Errorf("dependency drift supports the go and npm ecosystems, not %q", options.Ecosystem)
	}
	report := DriftReport{
		SchemaVersion: 1,
		Ecosystem:     options.Ecosystem,
		Mode:          mode,
		BaseRef:       options.Ref,
		ObservedAt:    observedAt,
	}
	if report.BaseRef == "" {
		report.BaseRef = "main"
	}
	repositories, report.Excluded = partitionExcludedRepositories(repositories, options.ExcludeRepositories)

	latest := newNpmLatestVersions()
	type result struct {
		repository DriftRepository
		skip       *GraphDiscoverySkip
	}
	results := make([]result, len(repositories))
	workers := options.Parallel
	if workers > len(repositories) {
		workers = len(repositories)
	}
	jobs := make(chan int)
	var group sync.WaitGroup
	var progressMu sync.Mutex
	completed := 0
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				repository := repositories[index]
				func() {
					defer func() {
						progressMu.Lock()
						completed++
						completedSnapshot := completed
						progressMu.Unlock()
						progress.Report(options.Progress, progress.Event{Operation: "deps drift", Phase: "inspect", Repository: repository.Slug, State: progress.Completed, Completed: completedSnapshot, Total: len(repositories)})
					}()
					if repository.Path == "" {
						results[index].skip = &GraphDiscoverySkip{
							Repository: repository.Slug,
							Reason:     "repository has no local clone path; drift inspects checked-out manifests only",
						}
						return
					}
					inspected, err := inspectDriftRepository(ctx, repository, options, observedAt, latest)
					if err != nil {
						results[index].repository = DriftRepository{
							Repository: repository.Slug,
							Path:       repository.Path,
							Status:     "error",
							Reason:     sanitizeDriftReason(err.Error()),
						}
						return
					}
					results[index].repository = inspected
				}()
			}
		}()
	}
	for index := range repositories {
		jobs <- index
	}
	close(jobs)
	group.Wait()

	for _, item := range results {
		if item.skip != nil {
			report.DiscoverySkips = append(report.DiscoverySkips, *item.skip)
			continue
		}
		if item.repository.Repository == "" {
			continue
		}
		report.Repositories = append(report.Repositories, item.repository)
	}
	sort.Slice(report.Repositories, func(i, j int) bool {
		return report.Repositories[i].Repository < report.Repositories[j].Repository
	})
	sort.Slice(report.DiscoverySkips, func(i, j int) bool {
		return report.DiscoverySkips[i].Repository < report.DiscoverySkips[j].Repository
	})

	report.Groups = classifyDriftGroups(report.Repositories, options, observedAt)
	report.Summary = summarizeDrift(report)
	return report, nil
}

// inspectDriftRepository dispatches one repository to its ecosystem's
// manifest reader. It is the single seam that keeps AnalyzeDrift's
// parallelism, progress reporting, and grouping ecosystem-agnostic.
func inspectDriftRepository(ctx context.Context, repository Repository, options DriftOptions, observedAt time.Time, latest *npmLatestVersions) (DriftRepository, error) {
	if options.Ecosystem == EcosystemNPM {
		return inspectNpmDriftRepository(ctx, repository, options, observedAt, latest)
	}
	return inspectGoDriftRepository(ctx, repository, options, observedAt)
}

// partitionExcludedRepositories removes every repository whose "owner/name"
// matches an --exclude glob and returns the excluded slugs so the report can
// say so out loud. An excluded repository is never inspected, never fetched,
// and never counted — the distinction between "clean" and "not looked at"
// must survive into the report.
func partitionExcludedRepositories(repositories []Repository, patterns []string) (retained []Repository, excluded []string) {
	if len(patterns) == 0 {
		return repositories, nil
	}
	retained = make([]Repository, 0, len(repositories))
	for _, repository := range repositories {
		if matchesAnyGlob(repository.Slug, patterns) {
			excluded = append(excluded, repository.Slug)
			continue
		}
		retained = append(retained, repository)
	}
	sort.Strings(excluded)
	return retained, excluded
}

// DriftFailed reports whether the complete report should exit non-zero.
func DriftFailed(report DriftReport, failOnDrift bool) bool {
	return DriftFailedWith(report, failOnDrift, false)
}

// DriftFailedWith adds the behind-latest gate to DriftFailed. Behind-latest
// is a separate opt-in because it can only be observed with --online, and a
// fleet that has deliberately not yet adopted a release is not the same
// finding as one whose repositories disagree with each other.
func DriftFailedWith(report DriftReport, failOnDrift, failOnBehind bool) bool {
	if report.Summary.Error > 0 {
		return true
	}
	for _, repository := range report.Repositories {
		if repository.Status == "error" {
			return true
		}
	}
	if failOnBehind && report.Summary.Behind > 0 {
		return true
	}
	if !failOnDrift {
		return false
	}
	return report.Summary.Divergent > 0 || report.Summary.Replaced > 0 || report.Summary.MajorSplit > 0
}

func summarizeDrift(report DriftReport) DriftSummary {
	summary := DriftSummary{
		Repositories: len(report.Repositories),
		Dependencies: len(report.Groups),
	}
	for _, group := range report.Groups {
		switch group.Classification {
		case DriftConverged:
			summary.Converged++
		case DriftDivergent:
			summary.Divergent++
		case DriftReplaced:
			summary.Replaced++
		case DriftMajorPathSplit:
			summary.MajorSplit++
		case DriftBehindLatest:
			// behind_latest is also counted below through group.Behind; the
			// classification only records that nothing more urgent applied.
		case DriftUnavailable:
			summary.Unavailable++
		case DriftError:
			summary.Error++
		}
		if group.Behind {
			summary.Behind++
		}
	}
	for _, repository := range report.Repositories {
		if repository.Status == "error" {
			summary.Error++
		}
	}
	return summary
}

func classifyDriftGroups(repositories []DriftRepository, options DriftOptions, observedAt time.Time) []DriftVersionGroup {
	type observation struct {
		dependency string
		version    string
		kind       string
		declared   string
		repository string
		replaced   bool
		latest     *VersionEvidence
		err        bool
	}
	byDependency := map[string][]observation{}
	families := map[string]map[string]struct{}{}
	selector := newDriftDependencySelector(options)

	for _, repository := range repositories {
		if repository.Status == "error" {
			continue
		}
		for _, dependency := range repository.Dependencies {
			if !selector.matches(dependency.Dependency) {
				continue
			}
			version := dependency.Selected.Value
			kind := "selected"
			if version == "" {
				version = dependency.Declared.Value
				kind = "declared"
			}
			if version == "" {
				version = "(unknown)"
				kind = "declared"
			}
			obs := observation{
				dependency: dependency.Dependency,
				version:    version,
				kind:       kind,
				declared:   dependency.Declared.Value,
				repository: repository.Repository,
				replaced:   dependency.Replacement != nil,
				latest:     dependency.Latest,
				err:        dependency.Selected.Reason != "" && strings.Contains(dependency.Selected.Reason, "inspection failed"),
			}
			byDependency[dependency.Dependency] = append(byDependency[dependency.Dependency], obs)
			// Major-path families are a Go module convention (`/v2` suffixes).
			// npm encodes a major bump in the version, never in the package
			// name, so grouping npm names into families would invent a split
			// that cannot exist.
			if options.Ecosystem == EcosystemNPM {
				continue
			}
			family := modulePathFamily(dependency.Dependency)
			if families[family] == nil {
				families[family] = map[string]struct{}{}
			}
			families[family][dependency.Dependency] = struct{}{}
		}
	}

	splitFamilies := map[string][]string{}
	for family, paths := range families {
		if len(paths) < 2 {
			continue
		}
		list := make([]string, 0, len(paths))
		for path := range paths {
			list = append(list, path)
		}
		sort.Strings(list)
		splitFamilies[family] = list
	}

	groups := make([]DriftVersionGroup, 0, len(byDependency))
	for dependency, observations := range byDependency {
		group := DriftVersionGroup{Dependency: dependency}
		if options.Ecosystem != EcosystemNPM {
			group.Family = modulePathFamily(dependency)
		}
		versionUses := map[string]*DriftVersionUse{}
		replaced := false
		unavailable := false
		hadError := false
		var latest *VersionEvidence
		for _, observation := range observations {
			key := observation.kind + ":" + observation.version
			use := versionUses[key]
			if use == nil {
				use = &DriftVersionUse{Version: observation.version, Kind: observation.kind}
				versionUses[key] = use
			}
			// One repository can declare the same dependency in several
			// manifests (an npm monorepo routinely does). The group answers
			// "which repositories use this version", so list each once.
			use.Repositories = appendUniqueSorted(use.Repositories, observation.repository)
			if observation.replaced {
				replaced = true
			}
			if observation.err {
				hadError = true
			}
			if observation.latest != nil {
				latest = observation.latest
				if observation.latest.Value == "" && observation.latest.Reason != "" && options.Online {
					unavailable = true
				}
			}
		}
		group.Versions = make([]DriftVersionUse, 0, len(versionUses))
		for _, use := range versionUses {
			sort.Strings(use.Repositories)
			group.Versions = append(group.Versions, *use)
		}
		sort.Slice(group.Versions, func(i, j int) bool {
			if group.Versions[i].Version == group.Versions[j].Version {
				return group.Versions[i].Kind < group.Versions[j].Kind
			}
			return group.Versions[i].Version < group.Versions[j].Version
		})
		group.Latest = latest
		if latest != nil && latest.Value != "" {
			for _, observation := range observations {
				if !observationLagsLatest(options.Ecosystem, observation.version, observation.kind, observation.declared, latest.Value) {
					continue
				}
				group.Behind = true
				group.BehindRepositories = appendUniqueSorted(group.BehindRepositories, observation.repository)
			}
			if group.Behind {
				group.BehindReason = "at least one repository resolves or admits only versions older than the published latest " + latest.Value
			}
		}
		if paths := splitFamilies[group.Family]; len(paths) > 1 {
			group.Classification = DriftMajorPathSplit
			group.MajorPaths = paths
			group.Reason = "multiple major module paths for the same family are present in the selected fleet"
		} else if hadError {
			group.Classification = DriftError
			group.Reason = "one or more repositories failed while inspecting this dependency"
		} else if unavailable {
			group.Classification = DriftUnavailable
			group.Reason = "latest version could not be observed"
		} else if replaced {
			group.Classification = DriftReplaced
			group.Reason = "at least one repository replaces this module path"
		} else if distinctSelectedVersions(group.Versions) > 1 {
			group.Classification = DriftDivergent
			group.Reason = "repositories select different versions of this module path"
		} else if group.Behind {
			group.Classification = DriftBehindLatest
			group.Reason = group.BehindReason
		} else {
			group.Classification = DriftConverged
			group.Reason = "selected repositories agree on one version"
		}
		if group.Latest == nil && !options.Online {
			group.Latest = &VersionEvidence{
				ObservedAt: observedAt,
				Source:     "not_queried_offline",
				Reason:     "latest was not queried; pass --online to consult the module proxy",
			}
		}
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Dependency == groups[j].Dependency {
			return groups[i].Classification < groups[j].Classification
		}
		return groups[i].Dependency < groups[j].Dependency
	})
	return groups
}

func distinctSelectedVersions(versions []DriftVersionUse) int {
	seen := map[string]struct{}{}
	for _, version := range versions {
		if version.Kind != "selected" && version.Kind != "declared" {
			continue
		}
		if version.Version == "" || version.Version == "(unknown)" {
			continue
		}
		seen[version.Version] = struct{}{}
	}
	return len(seen)
}

func dependencyFilterSet(dependencies []string) map[string]bool {
	if len(dependencies) == 0 {
		return nil
	}
	set := make(map[string]bool, len(dependencies))
	for _, dependency := range dependencies {
		dependency = strings.TrimSpace(dependency)
		if dependency != "" {
			set[dependency] = true
		}
	}
	return set
}

// driftDependencySelector retains a dependency when it is named exactly by
// --dependency or matched by a --scope glob. With neither flag set every
// dependency is retained, which is the historical behaviour.
type driftDependencySelector struct {
	exact  map[string]bool
	scopes []string
}

func newDriftDependencySelector(options DriftOptions) driftDependencySelector {
	selector := driftDependencySelector{exact: dependencyFilterSet(options.Dependencies)}
	for _, scope := range options.Scopes {
		if scope = strings.TrimSpace(scope); scope != "" {
			selector.scopes = append(selector.scopes, scope)
		}
	}
	return selector
}

func (selector driftDependencySelector) matches(dependency string) bool {
	if len(selector.exact) == 0 && len(selector.scopes) == 0 {
		return true
	}
	if selector.exact[dependency] {
		return true
	}
	return matchesAnyGlob(dependency, selector.scopes)
}

// matchesAnyGlob reports whether value matches any pattern under path.Match
// semantics, where "*" never crosses a "/". A pattern that is not a valid
// glob is compared literally rather than silently matching nothing.
func matchesAnyGlob(value string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if pattern == value {
			return true
		}
		if matched, err := path.Match(pattern, value); err == nil && matched {
			return true
		}
	}
	return false
}

// observationLagsLatest reports whether one repository's observation provably
// resolves or admits only versions older than the published latest.
//
// A locked/selected exact version is compared directly. An npm declaration
// with no lockfile evidence is judged by whether its range could admit the
// latest release at all: `"0.14.0"` cannot, `^0.14.0` cannot reach 0.15.x,
// and a range WB does not evaluate is never reported as behind. Nothing is
// inferred from a specifier WB could not read.
func observationLagsLatest(ecosystem Ecosystem, version, kind, declared, latest string) bool {
	if kind == "selected" && universalSemverValid(version) {
		return universalSemverCompare(version, latest) < 0
	}
	if ecosystem != EcosystemNPM {
		if universalSemverValid(version) {
			return universalSemverCompare(version, latest) < 0
		}
		return false
	}
	if universalSemverValid(version) {
		return universalSemverCompare(version, latest) < 0
	}
	verdict := npmRangeAdmits(declared, latest)
	return verdict.Evaluated && !verdict.Admits
}

func appendUniqueSorted(values []string, addition string) []string {
	for _, value := range values {
		if value == addition {
			return values
		}
	}
	values = append(values, addition)
	sort.Strings(values)
	return values
}

func modulePathFamily(modulePath string) string {
	prefix, _, ok := module.SplitPathVersion(modulePath)
	if !ok || prefix == "" {
		return modulePath
	}
	return strings.TrimSuffix(prefix, "/")
}

func sanitizeDriftReason(reason string) string {
	reason = strings.TrimSpace(reason)
	replacements := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(token|password|authorization|credential)[=:]\S+`),
		regexp.MustCompile(`(?i)://[^/@\s]+:[^@/\s]+@`),
		regexp.MustCompile(`git@[^\s:]+:`),
	}
	for _, pattern := range replacements {
		reason = pattern.ReplaceAllString(reason, "[redacted]")
	}
	return reason
}

func relativeManifest(repositoryPath, manifestPath string) string {
	relative, err := filepath.Rel(repositoryPath, manifestPath)
	if err != nil {
		return filepath.ToSlash(manifestPath)
	}
	return filepath.ToSlash(relative)
}

func joinModuleDir(repositoryPath, manifest string) string {
	dir := path.Dir(manifest)
	if dir == "." {
		return repositoryPath
	}
	return filepath.Join(repositoryPath, filepath.FromSlash(dir))
}
