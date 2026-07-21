package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Check selects a conventional verification class.
type Check string

const (
	CheckLint  Check = "lint"
	CheckTest  Check = "test"
	CheckBuild Check = "build"
	CheckSpec  Check = "spec"
)

// RunOptions bounds a single external command and retries only failed
// attempts. Zero Timeout disables the per-command deadline.
type RunOptions struct {
	Timeout time.Duration
	Retry   int
}

// VerificationReport records all conventional checks applicable to a
// repository. Unsupported stacks and missing optional Node scripts are skipped
// rather than treated as failures.
type VerificationReport struct {
	Repository string              `yaml:"repository" json:"repository"`
	Path       string              `yaml:"path" json:"path"`
	Status     Status              `yaml:"status" json:"status"`
	Results    []VerificationEntry `yaml:"results" json:"results"`
}

// VerificationEntry is one command WB attempted or intentionally skipped.
type VerificationEntry struct {
	Language string `yaml:"language" json:"language"`
	Module   string `yaml:"module,omitempty" json:"module,omitempty"`
	Check    Check  `yaml:"check" json:"check"`
	Command  string `yaml:"command,omitempty" json:"command,omitempty"`
	Status   Status `yaml:"status" json:"status"`
	Detail   string `yaml:"detail,omitempty" json:"detail,omitempty"`
	Attempts int    `yaml:"attempts,omitempty" json:"attempts,omitempty"`
}

// Verify runs the requested conventional Go and Node checks. The caller owns
// cross-repository parallelism; checks within one module run in the requested
// order to keep output and failures clear.
func Verify(ctx context.Context, repository, path string, checks []Check) VerificationReport {
	return VerifyWithOptions(ctx, repository, path, checks, RunOptions{})
}

// VerifyWithOptions runs the requested checks with per-command reliability
// controls. The returned report includes every attempted, skipped, passed, or
// failed command.
func VerifyWithOptions(ctx context.Context, repository, path string, checks []Check, options RunOptions) VerificationReport {
	report := VerificationReport{Repository: repository, Path: path, Status: StatusSkipped}
	modules, err := goModules(path)
	if err != nil {
		return VerificationReport{Repository: repository, Path: path, Status: StatusFailed, Results: []VerificationEntry{{Language: "go", Status: StatusFailed, Detail: err.Error()}}}
	}
	for _, module := range modules {
		for _, check := range checks {
			if check == CheckSpec {
				continue
			}
			command := goCommand(check)
			entry := runVerification(ctx, options, "go", relativePath(path, module), check, module, command...)
			report.Results = append(report.Results, entry)
		}
	}
	if node, ok, err := nodeProject(path); err != nil {
		report.Results = append(report.Results, VerificationEntry{Language: "node", Status: StatusFailed, Detail: err.Error()})
	} else if ok {
		for _, check := range checks {
			if check == CheckSpec {
				continue
			}
			if !node.Scripts[string(check)] {
				report.Results = append(report.Results, VerificationEntry{Language: "node", Check: check, Status: StatusSkipped, Detail: "script is not defined"})
				continue
			}
			command := []string{node.PackageManager, "run", string(check)}
			entry := runVerification(ctx, options, "node", ".", check, path, command...)
			report.Results = append(report.Results, entry)
		}
	}
	if containsCheck(checks, CheckSpec) {
		if _, err := os.Stat(filepath.Join(path, "spec")); err == nil {
			entry := runVerification(ctx, options, "specscore", ".", CheckSpec, path, "specscore", "spec", "lint")
			report.Results = append(report.Results, entry)
		} else if !os.IsNotExist(err) {
			report.Results = append(report.Results, VerificationEntry{Language: "specscore", Check: CheckSpec, Status: StatusFailed, Detail: err.Error()})
		} else {
			report.Results = append(report.Results, VerificationEntry{Language: "specscore", Check: CheckSpec, Status: StatusSkipped, Detail: "spec directory is not present"})
		}
	}
	if len(report.Results) == 0 {
		return report
	}
	report.Status = StatusPassed
	for _, result := range report.Results {
		if result.Status == StatusFailed {
			report.Status = StatusFailed
			break
		}
	}
	return report
}

func containsCheck(checks []Check, want Check) bool {
	for _, check := range checks {
		if check == want {
			return true
		}
	}
	return false
}

func goCommand(check Check) []string {
	switch check {
	case CheckLint:
		return []string{"go", "vet", "./..."}
	case CheckTest:
		return []string{"go", "test", "./..."}
	case CheckBuild:
		return []string{"go", "build", "./..."}
	default:
		return nil
	}
}

func runVerification(ctx context.Context, options RunOptions, language, module string, check Check, dir string, command ...string) VerificationEntry {
	entry := VerificationEntry{Language: language, Module: module, Check: check, Command: strings.Join(command, " ")}
	if len(command) == 0 {
		entry.Status = StatusSkipped
		entry.Detail = "unsupported check"
		return entry
	}
	output, attempts, err := runWithOptions(ctx, options, dir, command[0], command[1:]...)
	entry.Attempts = attempts
	if err != nil {
		entry.Status = StatusFailed
		entry.Detail = commandError(entry.Command, output, err)
		return entry
	}
	entry.Status = StatusPassed
	return entry
}

type nodeManifest struct {
	Scripts        map[string]string `json:"scripts"`
	PackageManager string            `json:"packageManager"`
}

type nodeProjectInfo struct {
	Scripts        map[string]bool
	PackageManager string
}

func nodeProject(root string) (nodeProjectInfo, bool, error) {
	path := filepath.Join(root, "package.json")
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nodeProjectInfo{}, false, nil
	}
	if err != nil {
		return nodeProjectInfo{}, false, err
	}
	var manifest nodeManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return nodeProjectInfo{}, false, fmt.Errorf("parse package.json: %w", err)
	}
	project := nodeProjectInfo{Scripts: map[string]bool{}, PackageManager: detectPackageManager(root, manifest.PackageManager)}
	for name := range manifest.Scripts {
		project.Scripts[name] = true
	}
	return project, true, nil
}

func detectPackageManager(root, declared string) string {
	if at := strings.IndexByte(declared, '@'); at > 0 {
		declared = declared[:at]
	}
	switch declared {
	case "npm", "pnpm", "yarn", "bun":
		return declared
	}
	for _, candidate := range []struct {
		file, command string
	}{
		{"pnpm-lock.yaml", "pnpm"},
		{"yarn.lock", "yarn"},
		{"bun.lock", "bun"},
		{"bun.lockb", "bun"},
		{"package-lock.json", "npm"},
	} {
		if _, err := os.Stat(filepath.Join(root, candidate.file)); err == nil {
			return candidate.command
		}
	}
	return "npm"
}

func run(ctx context.Context, dir, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	return string(output), err
}

func runWithOptions(ctx context.Context, options RunOptions, dir, name string, args ...string) (string, int, error) {
	attempts := 0
	for {
		attempts++
		attemptCtx := ctx
		cancel := func() {}
		if options.Timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, options.Timeout)
		}
		output, err := run(attemptCtx, dir, name, args...)
		timedOut := attemptCtx.Err() == context.DeadlineExceeded
		cancel()
		if timedOut {
			err = fmt.Errorf("timed out after %s", options.Timeout)
		}
		if err == nil || attempts > options.Retry {
			return output, attempts, err
		}
	}
}

func commandError(command, output string, err error) string {
	detail := strings.TrimSpace(output)
	if detail == "" {
		detail = err.Error()
	}
	const max = 1000
	if len(detail) > max {
		detail = detail[:max] + "…"
	}
	return detail
}

// ParseChecks validates the explicit --checks list. A missing list defaults to
// the conventional lint, test, build sequence.
func ParseChecks(value string) ([]Check, error) {
	if strings.TrimSpace(value) == "" {
		return []Check{CheckLint, CheckTest, CheckBuild}, nil
	}
	seen := map[Check]bool{}
	var checks []Check
	for _, raw := range strings.Split(value, ",") {
		check := Check(strings.TrimSpace(raw))
		switch check {
		case CheckLint, CheckTest, CheckBuild, CheckSpec:
		default:
			return nil, fmt.Errorf("unknown check %q (want lint, test, build, or spec)", raw)
		}
		if !seen[check] {
			checks = append(checks, check)
			seen[check] = true
		}
	}
	if len(checks) == 0 {
		return nil, fmt.Errorf("requires at least one check")
	}
	return checks, nil
}

// SortVerificationReports orders reports for deterministic output.
func SortVerificationReports(reports []VerificationReport) {
	sort.Slice(reports, func(i, j int) bool { return reports[i].Repository < reports[j].Repository })
}
