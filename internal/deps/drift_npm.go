package deps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// inspectNpmDriftRepository reads every package.json and pnpm-workspace.yaml
// in one checked-out repository and records, per reference:
//
//   - Declared — the specifier exactly as written in the manifest.
//   - Selected — the version the governing lockfile actually resolved, which
//     is the number that decides what a build installs. A caret range that
//     *could* accept a newer release still installs whatever the committed
//     lockfile pins, so reporting only the range hides real drift.
//   - Latest   — the registry's published latest, when --online is set.
//
// Nothing is inferred: a missing or unindexable lockfile produces a
// declared-fallback selection carrying the reason, never a guessed version.
func inspectNpmDriftRepository(ctx context.Context, repository Repository, options DriftOptions, observedAt time.Time, latest *npmLatestVersions) (DriftRepository, error) {
	report := DriftRepository{
		Repository: repository.Slug,
		Path:       repository.Path,
		Status:     "ok",
	}
	selector := newDriftDependencySelector(options)
	packageManifests, workspaceManifests, err := npmManifestFiles(repository.Path)
	if err != nil {
		return DriftRepository{}, err
	}
	lockScopes, err := readNpmLockScopes(repository.Path)
	if err != nil {
		return DriftRepository{}, err
	}
	lockDirs := make([]string, 0, len(lockScopes))
	for directory := range lockScopes {
		lockDirs = append(lockDirs, directory)
	}
	sort.Strings(lockDirs)

	for _, relative := range packageManifests {
		contents, readErr := os.ReadFile(filepath.Join(repository.Path, filepath.FromSlash(relative))) // #nosec G304 -- path comes from a walk of the inspected checkout
		if readErr != nil {
			return DriftRepository{}, readErr
		}
		_, requirements, parseErr := parseNpmPackageJSONManifest(repository.Slug, relative, contents)
		if parseErr != nil {
			return DriftRepository{}, fmt.Errorf("parse %s: %w", relative, parseErr)
		}
		for _, requirement := range requirements {
			if !selector.matches(requirement.Dependency) {
				continue
			}
			report.Dependencies = append(report.Dependencies, npmDriftDependency(
				requirement.Dependency, relative, requirement.Field, requirement.Version,
				lockDirs, lockScopes, observedAt,
			))
		}
	}
	for _, relative := range workspaceManifests {
		contents, readErr := os.ReadFile(filepath.Join(repository.Path, filepath.FromSlash(relative))) // #nosec G304 -- path comes from a walk of the inspected checkout
		if readErr != nil {
			return DriftRepository{}, readErr
		}
		for _, ref := range scanPnpmWorkspaceRefs(contents) {
			if !selector.matches(ref.Key) {
				continue
			}
			report.Dependencies = append(report.Dependencies, npmDriftDependency(
				ref.Key, relative, workspaceSelector(ref), ref.Value,
				lockDirs, lockScopes, observedAt,
			))
		}
	}

	for index := range report.Dependencies {
		if options.Online {
			evidence := latest.observe(ctx, report.Dependencies[index].Dependency, options, observedAt)
			report.Dependencies[index].Latest = &evidence
			continue
		}
		report.Dependencies[index].Latest = &VersionEvidence{
			ObservedAt: observedAt,
			Source:     "not_queried_offline",
			Reason:     "latest was not queried; pass --online to consult the npm registry",
		}
	}
	sortDriftDependencies(report.Dependencies)
	return report, nil
}

// npmDriftDependency builds one manifest reference's evidence row.
func npmDriftDependency(dependency, manifest, field, specifier string, lockDirs []string, lockScopes map[string]npmLockScope, observedAt time.Time) DriftDependency {
	row := DriftDependency{
		Dependency: dependency,
		Manifest:   manifest,
		Field:      field,
		Declared: VersionEvidence{
			Value:      specifier,
			ObservedAt: observedAt,
			Source:     manifest + " " + field,
		},
		Edges: []DriftEdge{{
			Dependency: dependency,
			Version:    specifier,
			Manifest:   manifest,
		}},
	}
	row.Selected = npmSelectedVersion(dependency, manifest, specifier, lockDirs, lockScopes, observedAt)
	return row
}

// npmSelectedVersion resolves what the governing lockfile actually installs
// for one manifest reference, falling back to the declared specifier with an
// explicit reason when no lockfile evidence exists.
func npmSelectedVersion(dependency, manifest, specifier string, lockDirs []string, lockScopes map[string]npmLockScope, observedAt time.Time) VersionEvidence {
	directory, found := npmLockfileDirForFile(lockDirs, manifest)
	if !found {
		return VersionEvidence{
			Value: specifier, ObservedAt: observedAt, Source: "declared_fallback",
			Reason: "no lockfile governs " + manifest + "; using the declared specifier",
		}
	}
	scope := lockScopes[directory]
	locked, exists := scope.Versions[dependency]
	if !exists {
		reason := "lockfile in " + lockScopeLabel(directory) + " does not resolve this package; using the declared specifier"
		if scope.Reason != "" {
			reason = "lockfile in " + lockScopeLabel(directory) + " could not be indexed (" + scope.Reason + "); using the declared specifier"
		}
		return VersionEvidence{Value: specifier, ObservedAt: observedAt, Source: "declared_fallback", Reason: reason}
	}
	if conflict := locked.Conflict(); conflict != "" {
		return VersionEvidence{ObservedAt: observedAt, Source: locked.Source, Reason: conflict}
	}
	return VersionEvidence{Value: locked.Version(), ObservedAt: observedAt, Source: locked.Source}
}

func lockScopeLabel(directory string) string {
	if directory == "" {
		return "the repository root"
	}
	return directory
}

func sortDriftDependencies(dependencies []DriftDependency) {
	sort.Slice(dependencies, func(i, j int) bool {
		left, right := dependencies[i], dependencies[j]
		if left.Dependency != right.Dependency {
			return left.Dependency < right.Dependency
		}
		if left.Manifest != right.Manifest {
			return left.Manifest < right.Manifest
		}
		return left.Field < right.Field
	})
}

// npmLatestVersions memoizes one registry lookup per package for the whole
// run. A fleet scan sees the same @scope package declared in dozens of
// manifests; querying the registry once per manifest would multiply a fleet
// run's network cost by an order of magnitude for no extra evidence.
type npmLatestVersions struct {
	mutex sync.Mutex
	byKey map[string]VersionEvidence
}

func newNpmLatestVersions() *npmLatestVersions {
	return &npmLatestVersions{byKey: map[string]VersionEvidence{}}
}

func (cache *npmLatestVersions) observe(ctx context.Context, dependency string, options DriftOptions, observedAt time.Time) VersionEvidence {
	cache.mutex.Lock()
	if evidence, cached := cache.byKey[dependency]; cached {
		cache.mutex.Unlock()
		return evidence
	}
	cache.mutex.Unlock()

	evidence := observeLatestNpmVersion(ctx, dependency, options, observedAt)

	cache.mutex.Lock()
	// A concurrent worker may have resolved the same package first; keep the
	// first recorded observation so the report stays deterministic.
	if existing, cached := cache.byKey[dependency]; cached {
		cache.mutex.Unlock()
		return existing
	}
	cache.byKey[dependency] = evidence
	cache.mutex.Unlock()
	return evidence
}

func observeLatestNpmVersion(ctx context.Context, dependency string, options DriftOptions, observedAt time.Time) VersionEvidence {
	evidence := VersionEvidence{ObservedAt: observedAt, Source: "pnpm view " + dependency + " version"}
	if err := ValidateNpmPackageName(dependency); err != nil {
		evidence.Reason = sanitizeDriftReason(err.Error())
		return evidence
	}
	bump := BumpOptions{Options: Options{
		Timeout: options.Timeout, Retry: options.Retry, GitHubDir: options.GitHubDir,
	}}
	bump.LatestNpmVersion = options.LatestNpmVersion
	version, err := latestNpmVersion(ctx, dependency, bump)
	if err != nil {
		evidence.Reason = sanitizeDriftReason(err.Error())
		return evidence
	}
	evidence.Value = version
	return evidence
}
