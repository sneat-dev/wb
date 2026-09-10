package ciaudit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// CompareCoverageFloors implements
// lesson:l10-coverage-floors-are-raised-with-real-tests-never-lowered-to-fit:
// a numeric coverage threshold is a ratchet, and a floor lowered quietly in a
// branch is exactly the shape the lesson names ("lowering the bar was easier
// than restructuring the test").
//
// It compares every numeric `min_test_coverage_percent` in root's workflow
// files against the same file's value on the fetched target branch, and
// reports any threshold that dropped as a Finding. Unlike Audit, this
// function is not read-only in the no-process sense: it runs `git fetch` and
// `git show` against root, because "the fetched target" is a comparison this
// package cannot make from local files alone — root's own doc comment (see
// audit.go) describes Audit itself as read-only; this sibling function is the
// one exception, confined to this file, and only ever runs on explicit
// request (a non-empty target), never as part of Audit.
//
// It is a deliberate no-op — returning (nil, nil) — when root's current
// branch already equals target: auditing main against itself has nothing to
// compare.
func CompareCoverageFloors(root, target string) ([]Finding, error) {
	if strings.TrimSpace(target) == "" {
		return nil, nil
	}
	currentBranch, err := gitOutput(root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("determine the current branch: %w", err)
	}
	if currentBranch == target {
		return nil, nil
	}
	if _, err := gitOutput(root, "fetch", "--quiet", "origin", target); err != nil {
		return nil, fmt.Errorf("fetch origin/%s: %w", target, err)
	}
	targetRef := "origin/" + target

	localFloors, err := localCoverageFloors(root)
	if err != nil {
		return nil, err
	}

	var findings []Finding
	for relativePath, localValue := range localFloors {
		targetContent, err := gitOutput(root, "show", targetRef+":"+relativePath)
		if err != nil {
			// The workflow does not exist on the target (a new file on this
			// branch) — nothing to compare it against.
			continue
		}
		targetValue, ok := minTestCoveragePercent(targetContent)
		if !ok {
			continue
		}
		if localValue < targetValue {
			findings = append(findings, Finding{
				Code: "coverage-floor-lowered",
				Message: fmt.Sprintf(
					"min_test_coverage_percent dropped from %s (on %s) to %s",
					formatPercent(targetValue), target, formatPercent(localValue),
				),
				File: relativePath,
			})
		}
	}
	return findings, nil
}

// localCoverageFloors reads every workflow file's own
// `min_test_coverage_percent` value, keyed by its path relative to root
// (`.github/workflows/<name>.yml`, matching the path CompareCoverageFloors
// asks `git show <target>:<path>` for).
func localCoverageFloors(root string) (map[string]float64, error) {
	workflowsDir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(workflowsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	floors := map[string]float64{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lower := strings.ToLower(entry.Name())
		if !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(workflowsDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if value, ok := minTestCoveragePercent(string(raw)); ok {
			floors[".github/workflows/"+entry.Name()] = value
		}
	}
	return floors, nil
}

var minTestCoveragePercentPattern = regexp.MustCompile(`(?mi)min_test_coverage_percent\s*:\s*["']?([0-9]+(?:\.[0-9]+)?)`)

// minTestCoveragePercent extracts the first `min_test_coverage_percent`
// value from workflow content. A workflow naming it more than once (a
// sharded/matrix gate with per-job floors) is out of scope for this
// comparison — see the report's trade-off note.
func minTestCoveragePercent(content string) (float64, bool) {
	match := minTestCoveragePercentPattern.FindStringSubmatch(content)
	if match == nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func formatPercent(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// gitOutput runs one read-only git command in dir and returns trimmed
// stdout. It is the one place in this package that spawns a process — see
// CompareCoverageFloors's doc comment.
func gitOutput(dir string, arguments ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", dir}, arguments...)...)
	output, err := command.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(arguments, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}
