package migrate

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
)

type goManifestUpdate struct {
	Changed             bool
	DependencyDecisions []GoDependencyDecision
}

type goManifestDependency struct {
	version     string
	replacement string
}

func auditGoDependencyDecisions(
	moduleRoot string,
	phase string,
	beforeContents, afterContents []byte,
	targets, campaignRoots map[string]string,
	includeAllRequirements bool,
) ([]GoDependencyDecision, error) {
	before, err := goManifestDependencies(filepath.Join(moduleRoot, "go.mod"), beforeContents)
	if err != nil {
		return nil, err
	}
	after, err := goManifestDependencies(filepath.Join(moduleRoot, "go.mod"), afterContents)
	if err != nil {
		return nil, err
	}

	candidates := map[string]bool{}
	if includeAllRequirements {
		for path := range before {
			candidates[path] = true
		}
		for path := range after {
			candidates[path] = true
		}
		for path := range targets {
			candidates[path] = true
		}
	} else {
		for path := range campaignRoots {
			if before[path].replacement != "" || after[path].replacement != "" {
				candidates[path] = true
			}
		}
	}

	paths := make([]string, 0, len(candidates))
	for path := range candidates {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	decisions := make([]GoDependencyDecision, 0, len(paths))
	for _, path := range paths {
		checked, requiredAtCheck := before[path]
		result, requiredAfter := after[path]
		decision := GoDependencyDecision{
			Phase:              phase,
			Path:               path,
			RequiredAtCheck:    requiredAtCheck,
			VersionAtCheck:     checked.version,
			TargetVersion:      targets[path],
			RequiredAfter:      requiredAfter,
			VersionAfter:       result.version,
			VersionAction:      dependencyVersionAction(requiredAtCheck, requiredAfter, checked.version, result.version),
			ReplacementAtCheck: checked.replacement,
			ReplacementAfter:   result.replacement,
			ReplacementAction:  dependencyReplacementAction(checked.replacement, result.replacement),
		}
		decision.Reason = dependencyDecisionReason(decision, campaignRoots[path] != "")
		decisions = append(decisions, decision)
	}
	return decisions, nil
}

func goManifestDependencies(filename string, contents []byte) (map[string]goManifestDependency, error) {
	parsed, err := modfile.Parse(filename, contents, nil)
	if err != nil {
		return nil, err
	}
	dependencies := make(map[string]goManifestDependency, len(parsed.Require))
	for _, requirement := range parsed.Require {
		dependencies[requirement.Mod.Path] = goManifestDependency{version: requirement.Mod.Version}
	}
	for _, replacement := range parsed.Replace {
		dependency, required := dependencies[replacement.Old.Path]
		if !required {
			continue
		}
		dependency.replacement = replacement.New.Path
		if replacement.New.Version != "" {
			dependency.replacement += "@" + replacement.New.Version
		}
		dependencies[replacement.Old.Path] = dependency
	}
	return dependencies, nil
}

func dependencyVersionAction(requiredAtCheck, requiredAfter bool, versionAtCheck, versionAfter string) string {
	switch {
	case !requiredAtCheck && !requiredAfter:
		return "not_required"
	case !requiredAtCheck:
		return "added"
	case !requiredAfter:
		return "removed"
	case versionAtCheck != versionAfter:
		return "updated"
	default:
		return "unchanged"
	}
}

func dependencyReplacementAction(replacementAtCheck, replacementAfter string) string {
	switch {
	case replacementAtCheck == replacementAfter:
		return "unchanged"
	case replacementAtCheck == "":
		return "added"
	case replacementAfter == "":
		return "removed"
	default:
		return "updated"
	}
}

func dependencyDecisionReason(decision GoDependencyDecision, campaignDependency bool) string {
	reasons := make([]string, 0, 2)
	switch decision.VersionAction {
	case "not_required":
		if decision.TargetVersion != "" {
			reasons = append(reasons, "go mod tidy found no source use for the configured migration requirement")
		} else {
			reasons = append(reasons, "dependency was not required before or after normalization")
		}
	case "added":
		if decision.TargetVersion != "" {
			reasons = append(reasons, "migration configured this dependency")
		} else {
			reasons = append(reasons, "go mod tidy added the dependency required by source code")
		}
	case "removed":
		reasons = append(reasons, "go mod tidy removed the unused dependency")
	case "updated":
		if decision.TargetVersion == "" {
			reasons = append(reasons, "Go module selection changed the dependency during normalization")
		} else if decision.VersionAfter == decision.TargetVersion {
			reasons = append(reasons, "configured target version applied")
		} else {
			reasons = append(reasons, fmt.Sprintf("Go module selection resolved target %s to %s", decision.TargetVersion, decision.VersionAfter))
		}
	case "unchanged":
		switch {
		case decision.TargetVersion == "":
			reasons = append(reasons, "no target version configured; WB preserved the selected version")
		case decision.VersionAfter == decision.TargetVersion:
			reasons = append(reasons, "already at the configured target version")
		default:
			reasons = append(reasons, fmt.Sprintf("Go module selection kept %s instead of configured target %s", decision.VersionAfter, decision.TargetVersion))
		}
	}

	switch decision.ReplacementAction {
	case "added":
		if campaignDependency {
			reasons = append(reasons, "campaign worktree selected for local verification")
		} else {
			reasons = append(reasons, "replacement added by Go tooling")
		}
	case "removed":
		if campaignDependency && decision.Phase == "publishable" {
			reasons = append(reasons, "temporary campaign worktree replacement removed for publication")
		} else if campaignDependency {
			reasons = append(reasons, "unused campaign worktree replacement removed")
		} else {
			reasons = append(reasons, "replacement removed during normalization")
		}
	case "updated":
		reasons = append(reasons, "replacement target changed during normalization")
	case "unchanged":
		if decision.ReplacementAfter != "" {
			if campaignDependency {
				reasons = append(reasons, "campaign worktree replacement preserved")
			} else {
				reasons = append(reasons, "existing replacement preserved")
			}
		}
	}
	return strings.Join(reasons, "; ")
}
