package deps

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

func inspectGoDriftRepository(ctx context.Context, repository Repository, options DriftOptions, observedAt time.Time) (DriftRepository, error) {
	report := DriftRepository{
		Repository: repository.Slug,
		Path:       repository.Path,
		Status:     "ok",
	}
	selector := newDriftDependencySelector(options)
	err := filepath.WalkDir(repository.Path, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "go.mod" || ignoredManifestPath(path) {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative := relativeManifest(repository.Path, path)
		parsed, err := modfile.Parse(relative, contents, nil)
		if err != nil {
			return fmt.Errorf("parse %s: %w", relative, err)
		}
		consumer := ""
		if parsed.Module != nil {
			consumer = parsed.Module.Mod.Path
		}
		replacements := map[string]ReplaceEvidence{}
		for _, replacement := range parsed.Replace {
			evidence := ReplaceEvidence{
				OldPath:    replacement.Old.Path,
				OldVersion: replacement.Old.Version,
				NewPath:    replacement.New.Path,
				NewVersion: replacement.New.Version,
				Local:      modfile.IsDirectoryPath(replacement.New.Path),
				ObservedAt: observedAt,
				Source:     relative,
			}
			replacements[replacement.Old.Path] = evidence
		}
		moduleDir := joinModuleDir(repository.Path, relative)
		for _, requirement := range parsed.Require {
			if !selector.matches(requirement.Mod.Path) {
				continue
			}
			dependency := DriftDependency{
				Dependency: requirement.Mod.Path,
				Manifest:   relative,
				Declared: VersionEvidence{
					Value:      requirement.Mod.Version,
					ObservedAt: observedAt,
					Source:     relative + " require",
				},
				Edges: []DriftEdge{{
					ConsumerModule: consumer,
					Dependency:     requirement.Mod.Path,
					Version:        requirement.Mod.Version,
					Manifest:       relative,
					Indirect:       requirement.Indirect,
				}},
			}
			if replacement, ok := replacements[requirement.Mod.Path]; ok {
				copy := replacement
				dependency.Replacement = &copy
			}
			dependency.Selected = selectedGoModuleVersion(ctx, moduleDir, requirement.Mod.Path, requirement.Mod.Version, options, observedAt)
			if options.Online {
				latest := observeLatestGoVersion(ctx, requirement.Mod.Path, options, observedAt)
				dependency.Latest = &latest
			} else {
				dependency.Latest = &VersionEvidence{
					ObservedAt: observedAt,
					Source:     "not_queried_offline",
					Reason:     "latest was not queried; pass --online to consult the module proxy",
				}
			}
			report.Dependencies = append(report.Dependencies, dependency)
		}
		// Replacements without a matching require still matter for gates.
		for modulePath, replacement := range replacements {
			if !selector.matches(modulePath) {
				continue
			}
			found := false
			for _, dependency := range report.Dependencies {
				if dependency.Dependency == modulePath && dependency.Manifest == relative {
					found = true
					break
				}
			}
			if found {
				continue
			}
			copy := replacement
			report.Dependencies = append(report.Dependencies, DriftDependency{
				Dependency:  modulePath,
				Manifest:    relative,
				Declared:    VersionEvidence{ObservedAt: observedAt, Source: relative + " replace", Reason: "module is replaced without a matching require row"},
				Selected:    VersionEvidence{ObservedAt: observedAt, Source: "replace", Reason: "selection is redirected by replace"},
				Replacement: &copy,
				Latest: &VersionEvidence{
					ObservedAt: observedAt,
					Source:     "not_queried_offline",
					Reason:     "latest was not queried for a replace-only observation",
				},
			})
		}
		return nil
	})
	if err != nil {
		return DriftRepository{}, err
	}
	sort.Slice(report.Dependencies, func(i, j int) bool {
		if report.Dependencies[i].Dependency == report.Dependencies[j].Dependency {
			return report.Dependencies[i].Manifest < report.Dependencies[j].Manifest
		}
		return report.Dependencies[i].Dependency < report.Dependencies[j].Dependency
	})
	return report, nil
}

func selectedGoModuleVersion(ctx context.Context, moduleDir, modulePath, declared string, options DriftOptions, observedAt time.Time) VersionEvidence {
	evidence := VersionEvidence{ObservedAt: observedAt, Source: "go list -m"}
	output, _, err := runGoCommand(ctx, Options{
		Timeout: options.Timeout, Retry: options.Retry, GoPrivate: options.GoPrivate, GitHubDir: options.GitHubDir,
	}, moduleDir, "list", "-m", "-f", "{{.Version}}", modulePath)
	if err != nil {
		evidence.Value = declared
		evidence.Source = "declared_fallback"
		evidence.Reason = "go list -m unavailable; using declared require version (" + sanitizeDriftReason(err.Error()) + ")"
		return evidence
	}
	version := strings.TrimSpace(output)
	if version == "" {
		evidence.Value = declared
		evidence.Source = "declared_fallback"
		evidence.Reason = "go list -m returned an empty version; using declared require version"
		return evidence
	}
	evidence.Value = version
	return evidence
}

func observeLatestGoVersion(ctx context.Context, modulePath string, options DriftOptions, observedAt time.Time) VersionEvidence {
	evidence := VersionEvidence{
		ObservedAt: observedAt,
		Source:     "go list -m -json " + modulePath + "@latest",
	}
	version, err := latestGoVersion(ctx, modulePath, BumpOptions{
		Options: Options{
			Timeout: options.Timeout, Retry: options.Retry, GoPrivate: options.GoPrivate, GitHubDir: options.GitHubDir,
		},
	})
	if err != nil {
		evidence.Reason = sanitizeDriftReason(err.Error())
		return evidence
	}
	if !semver.IsValid(version) {
		evidence.Reason = fmt.Sprintf("latest version %q is not a valid semantic version", version)
		return evidence
	}
	evidence.Value = version
	return evidence
}
