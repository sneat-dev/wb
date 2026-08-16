package deps

import (
	"context"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/module"
)

// AnalyzeDrift builds a read-only Go dependency convergence report for the
// selected repositories. It never mutates manifests or contacts a registry
// unless options.Online is set.
func AnalyzeDrift(ctx context.Context, repositories []Repository, options DriftOptions) (DriftReport, error) {
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
	report := DriftReport{
		SchemaVersion: 1,
		Ecosystem:     EcosystemGo,
		Mode:          mode,
		BaseRef:       options.Ref,
		ObservedAt:    observedAt,
	}
	if report.BaseRef == "" {
		report.BaseRef = "main"
	}

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
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				repository := repositories[index]
				if repository.Path == "" {
					results[index].skip = &GraphDiscoverySkip{
						Repository: repository.Slug,
						Reason:     "repository has no local clone path; drift inspects checked-out go.mod files only",
					}
					continue
				}
				inspected, err := inspectGoDriftRepository(ctx, repository, options, observedAt)
				if err != nil {
					results[index].repository = DriftRepository{
						Repository: repository.Slug,
						Path:       repository.Path,
						Status:     "error",
						Reason:     sanitizeDriftReason(err.Error()),
					}
					continue
				}
				results[index].repository = inspected
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

// DriftFailed reports whether the complete report should exit non-zero.
func DriftFailed(report DriftReport, failOnDrift bool) bool {
	if report.Summary.Error > 0 {
		return true
	}
	for _, repository := range report.Repositories {
		if repository.Status == "error" {
			return true
		}
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
		case DriftUnavailable:
			summary.Unavailable++
		case DriftError:
			summary.Error++
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
		repository string
		replaced   bool
		latest     *VersionEvidence
		err        bool
	}
	byDependency := map[string][]observation{}
	families := map[string]map[string]struct{}{}
	filter := dependencyFilterSet(options.Dependencies)

	for _, repository := range repositories {
		if repository.Status == "error" {
			continue
		}
		for _, dependency := range repository.Dependencies {
			if len(filter) > 0 && !filter[dependency.Dependency] {
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
				repository: repository.Repository,
				replaced:   dependency.Replacement != nil,
				latest:     dependency.Latest,
				err:        dependency.Selected.Reason != "" && strings.Contains(dependency.Selected.Reason, "inspection failed"),
			}
			byDependency[dependency.Dependency] = append(byDependency[dependency.Dependency], obs)
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
		group := DriftVersionGroup{
			Dependency: dependency,
			Family:     modulePathFamily(dependency),
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
			use.Repositories = append(use.Repositories, observation.repository)
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

func matchesDriftDependency(modulePath string, filter map[string]bool) bool {
	if len(filter) == 0 {
		return true
	}
	return filter[modulePath]
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
