package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

// The Go dependency-pair proof lives here so the recovery flow can opt into it
// explicitly without widening any existing blob or JSONL proof.

// proveGoDependencyUpgradePair proves that the only source-to-target content
// change is a paired root go.mod/go.sum dependency upgrade. It deliberately
// accepts neither arbitrary semantic Go files nor a lone module file.
func proveGoDependencyUpgradePair(ctx context.Context, gitRoot, sourceSHA, targetSHA string) (int, error) {
	sourceMod, sourceModPresent := gitFileContentsAtRevision(ctx, gitRoot, sourceSHA, "go.mod")
	targetMod, targetModPresent := gitFileContentsAtRevision(ctx, gitRoot, targetSHA, "go.mod")
	sourceSum, sourceSumPresent := gitFileContentsAtRevision(ctx, gitRoot, sourceSHA, "go.sum")
	targetSum, targetSumPresent := gitFileContentsAtRevision(ctx, gitRoot, targetSHA, "go.sum")
	if !sourceModPresent || !targetModPresent || !sourceSumPresent || !targetSumPresent {
		return 0, errors.New("go dependency upgrade proof requires go.mod and go.sum at both revisions")
	}
	upgrades, err := goModDependencyUpgrades(sourceMod, targetMod)
	if err != nil {
		return 0, err
	}
	if err := goSumChangesAreOnlyDependencyUpgrades(sourceSum, targetSum, upgrades); err != nil {
		return 0, err
	}
	return len(upgrades), nil
}

type goRequirement struct {
	version  string
	indirect bool
}

// goModDependencyUpgrades returns each upgraded module's old and new version.
// Reversing those version tokens must exactly reconstruct source go.mod; that
// byte-level check preserves comments, directives, replacement rules, and
// require ordering/directness instead of treating an arbitrary modfile edit as
// semantically harmless.
func goModDependencyUpgrades(source, target string) (map[string][2]string, error) {
	sourceFile, err := modfile.Parse("source go.mod", []byte(source), nil)
	if err != nil {
		return nil, fmt.Errorf("parse source go.mod: %w", err)
	}
	targetFile, err := modfile.Parse("target go.mod", []byte(target), nil)
	if err != nil {
		return nil, fmt.Errorf("parse target go.mod: %w", err)
	}
	sourceRequirements, err := goRequirements(sourceFile)
	if err != nil {
		return nil, err
	}
	targetRequirements, err := goRequirements(targetFile)
	if err != nil {
		return nil, err
	}
	if len(sourceRequirements) != len(targetRequirements) {
		return nil, errors.New("go.mod require path set changed")
	}

	upgrades := make(map[string][2]string)
	reversedTargetRequirements := make([]*modfile.Require, 0, len(targetFile.Require))
	for _, targetRequirement := range targetFile.Require {
		sourceRequirement, exists := sourceRequirements[targetRequirement.Mod.Path]
		if !exists || sourceRequirement.indirect != targetRequirement.Indirect {
			return nil, errors.New("go.mod require path set or directness changed")
		}
		if !semver.IsValid(sourceRequirement.version) || !semver.IsValid(targetRequirement.Mod.Version) {
			return nil, fmt.Errorf("go.mod dependency %s has a non-semver version", targetRequirement.Mod.Path)
		}
		comparison := semver.Compare(targetRequirement.Mod.Version, sourceRequirement.version)
		if comparison < 0 {
			return nil, fmt.Errorf("go.mod dependency %s is a downgrade", targetRequirement.Mod.Path)
		}
		if comparison > 0 {
			upgrades[targetRequirement.Mod.Path] = [2]string{sourceRequirement.version, targetRequirement.Mod.Version}
		}
		reversed := *targetRequirement
		reversed.Mod.Version = sourceRequirement.version
		reversedTargetRequirements = append(reversedTargetRequirements, &reversed)
	}
	if len(upgrades) == 0 {
		return nil, errors.New("go.mod has no dependency version upgrade")
	}
	targetFile.SetRequire(reversedTargetRequirements)
	if !bytes.Equal(modfile.Format(sourceFile.Syntax), modfile.Format(targetFile.Syntax)) {
		return nil, errors.New("go.mod differs outside require dependency versions")
	}
	return upgrades, nil
}

func goRequirements(file *modfile.File) (map[string]goRequirement, error) {
	requirements := make(map[string]goRequirement, len(file.Require))
	for _, requirement := range file.Require {
		if _, exists := requirements[requirement.Mod.Path]; exists {
			return nil, fmt.Errorf("go.mod repeats require path %s", requirement.Mod.Path)
		}
		requirements[requirement.Mod.Path] = goRequirement{version: requirement.Mod.Version, indirect: requirement.Indirect}
	}
	return requirements, nil
}

func goSumChangesAreOnlyDependencyUpgrades(source, target string, upgrades map[string][2]string) error {
	sourceLines := goSumLineCounts(source)
	targetLines := goSumLineCounts(target)
	for line, sourceCount := range sourceLines {
		for count := targetLines[line]; count < sourceCount; count++ {
			if err := requireGoSumUpgradeLine(line, upgrades, false); err != nil {
				return err
			}
		}
	}
	for line, targetCount := range targetLines {
		for count := sourceLines[line]; count < targetCount; count++ {
			if err := requireGoSumUpgradeLine(line, upgrades, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func goSumLineCounts(contents string) map[string]int {
	counts := make(map[string]int)
	for _, line := range strings.Split(strings.TrimSpace(contents), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			counts[trimmed]++
		}
	}
	return counts
}

func requireGoSumUpgradeLine(line string, upgrades map[string][2]string, added bool) error {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return fmt.Errorf("go.sum changed outside upgraded dependencies: malformed line %q", line)
	}
	upgrade, exists := upgrades[fields[0]]
	if !exists {
		return fmt.Errorf("go.sum changed outside upgraded dependencies: %s", fields[0])
	}
	version := strings.TrimSuffix(fields[1], "/go.mod")
	expectedVersion := upgrade[0]
	if added {
		expectedVersion = upgrade[1]
	}
	if version != expectedVersion {
		return fmt.Errorf("go.sum changed outside upgraded dependency versions: %s %s", fields[0], fields[1])
	}
	return nil
}

func gitFileContentsAtRevision(ctx context.Context, gitRoot, revision, path string) (string, bool) {
	output, _, err := runCommand(ctx, 0, 0, gitRoot, "git", "show", revision+":"+path)
	if err != nil {
		return "", false
	}
	return output, true
}
