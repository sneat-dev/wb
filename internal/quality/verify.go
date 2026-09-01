package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/process"
)

// Check selects a conventional verification class.
type Check string

const (
	CheckLint    Check = "lint"
	CheckTest    Check = "test"
	CheckBuild   Check = "build"
	CheckSpec    Check = "spec"
	checkInstall Check = "install"
)

// RunOptions bounds a single external command and retries only failed
// attempts. Zero Timeout disables the per-command deadline.
type RunOptions struct {
	Timeout time.Duration
	Retry   int
	// GoTestShards runs each explicitly named Go package in this many
	// process-isolated shards. It is opt-in because TestMain and process-global
	// fixtures run once per shard; callers must name packages whose contract
	// permits that isolation. Discovery invokes TestMain once before each shard
	// process invokes it again.
	GoTestShards int
	// GoShardPackages are module-relative package patterns such as
	// ./internal/worktrees. Packages not named here still run exactly once.
	GoShardPackages []string
	// CoverageProfile retains the exact merged Go profile for one module.
	// Fleet and multi-module adapters reject it rather than inventing names.
	CoverageProfile string
	// CoverageDiagnosticsDir retains raw output from failed process-isolated
	// coverage jobs beside the durable coverage report. The human-facing error
	// remains bounded; this private artifact is the lossless recovery path.
	CoverageDiagnosticsDir string
	// CoverageDiagnosticsRepository identifies the owning repository in the
	// private manifest when a fleet runner executes several repositories.
	CoverageDiagnosticsRepository string
	// Progress receives lifecycle events for external checks. Callers may use it
	// for terminal diagnostics; reports remain the authoritative output.
	Progress func(Progress)
}

// ProgressState identifies a visible quality-work transition.
type ProgressState string

const (
	ProgressStarted             ProgressState = "started"
	ProgressCompleted           ProgressState = "completed"
	ProgressRepositoryCompleted ProgressState = "repository_completed"
)

// Progress describes one external check or a completed repository. Repository
// is filled by the fleet runner, which owns cross-repository scheduling.
type Progress struct {
	Repository string
	Language   string
	Module     string
	Check      Check
	Command    string
	State      ProgressState
	Status     Status
	Attempts   int
}

// VerificationReport records all conventional checks applicable to a
// repository. Unsupported stacks and missing optional Node scripts are skipped
// rather than treated as failures.
type VerificationReport struct {
	Repository string `yaml:"repository" json:"repository"`
	Path       string `yaml:"path" json:"path"`
	// Revision and WorkspaceClean are populated by the WB command adapter
	// around the complete verification run. They let a downstream receipt bind
	// successful mechanisms to the exact clean Git tree they exercised.
	Revision       string              `yaml:"revision,omitempty" json:"revision,omitempty"`
	WorkspaceClean bool                `yaml:"workspace_clean,omitempty" json:"workspace_clean,omitempty"`
	Status         Status              `yaml:"status" json:"status"`
	Results        []VerificationEntry `yaml:"results" json:"results"`
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
	if nodes, ok, err := nodeProjects(path); err != nil {
		report.Results = append(report.Results, VerificationEntry{Language: "node", Status: StatusFailed, Detail: err.Error()})
	} else if ok {
		for _, node := range nodes {
			hasScript := false
			for _, check := range checks {
				if check != CheckSpec && node.Scripts[string(check)] {
					hasScript = true
					break
				}
			}
			if hasScript && node.Locked {
				command := nodeInstallCommand(node.PackageManager)
				report.Results = append(report.Results, runVerification(ctx, options, "node", node.Module, checkInstall, node.Path, command...))
			}
			for _, check := range checks {
				if check == CheckSpec {
					continue
				}
				if !node.Scripts[string(check)] {
					report.Results = append(report.Results, VerificationEntry{Language: "node", Module: node.Module, Check: check, Status: StatusSkipped, Detail: "script is not defined"})
					continue
				}
				command := []string{node.PackageManager, "run", string(check)}
				entry := runVerification(ctx, options, "node", node.Module, check, node.Path, command...)
				report.Results = append(report.Results, entry)
			}
		}
	}
	if containsCheck(checks, CheckSpec) {
		specRoot := filepath.Join(path, "spec")
		if _, err := os.Stat(specRoot); err == nil {
			entry := runVerification(ctx, options, "specscore", ".", CheckSpec, path, "specscore", "spec", "lint")
			report.Results = append(report.Results, entry)
		} else if !os.IsNotExist(err) {
			report.Results = append(report.Results, VerificationEntry{Language: "specscore", Check: CheckSpec, Status: StatusFailed, Detail: fmt.Sprintf("inspect SpecScore root %q: %v", specRoot, err)})
		} else {
			specConfig := filepath.Join(path, "specscore.yaml")
			if _, configErr := os.Lstat(specConfig); configErr == nil {
				report.Results = append(report.Results, VerificationEntry{
					Language: "specscore",
					Check:    CheckSpec,
					Status:   StatusFailed,
					Detail:   fmt.Sprintf("SpecScore config %q requires root %q, but the root is missing", specConfig, specRoot),
				})
			} else if !os.IsNotExist(configErr) {
				report.Results = append(report.Results, VerificationEntry{Language: "specscore", Check: CheckSpec, Status: StatusFailed, Detail: fmt.Sprintf("inspect SpecScore config %q: %v", specConfig, configErr)})
			} else {
				report.Results = append(report.Results, VerificationEntry{Language: "specscore", Check: CheckSpec, Status: StatusSkipped, Detail: "spec directory is not present"})
			}
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
	shardedGoTest := language == "go" && check == CheckTest && options.GoTestShards > 1
	if shardedGoTest {
		entry.Command = coverageCommandDescription(options)
	}
	reportQualityProgress(options, Progress{
		Language: language, Module: module, Check: check, Command: entry.Command, State: ProgressStarted,
	})
	var output string
	var attempts int
	var err error
	if shardedGoTest {
		output, attempts, err = runShardedVerification(ctx, options, dir)
	} else {
		output, attempts, err = runWithOptions(ctx, options, dir, command[0], command[1:]...)
	}
	entry.Attempts = attempts
	if err != nil {
		entry.Status = StatusFailed
		entry.Detail = commandError(entry.Command, output, err)
		reportQualityProgress(options, Progress{
			Language: language, Module: module, Check: check, Command: entry.Command,
			State: ProgressCompleted, Status: entry.Status, Attempts: attempts,
		})
		return entry
	}
	entry.Status = StatusPassed
	reportQualityProgress(options, Progress{
		Language: language, Module: module, Check: check, Command: entry.Command,
		State: ProgressCompleted, Status: entry.Status, Attempts: attempts,
	})
	return entry
}

func runShardedVerification(ctx context.Context, options RunOptions, module string) (string, int, error) {
	profile, err := os.CreateTemp("", "wb-verify-coverage-*.out")
	if err != nil {
		return "", 0, err
	}
	profilePath := profile.Name()
	if err := profile.Close(); err != nil {
		_ = os.Remove(profilePath)
		return "", 0, err
	}
	defer func() { _ = os.Remove(profilePath) }()
	return runCoverageWithOptions(ctx, options, module, profilePath)
}

func reportQualityProgress(options RunOptions, progress Progress) {
	if options.Progress != nil {
		options.Progress(progress)
	}
}

type nodeManifest struct {
	Scripts        map[string]string `json:"scripts"`
	PackageManager string            `json:"packageManager"`
}

type nodeProjectInfo struct {
	Scripts        map[string]bool
	PackageManager string
	Path           string
	Module         string
	Locked         bool
}

func nodeProject(root, path string, locked bool) (nodeProjectInfo, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nodeProjectInfo{}, err
	}
	var manifest nodeManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return nodeProjectInfo{}, fmt.Errorf("parse package.json: %w", err)
	}
	project := nodeProjectInfo{Scripts: map[string]bool{}, PackageManager: detectPackageManager(root, manifest.PackageManager), Path: root, Locked: locked}
	for name := range manifest.Scripts {
		project.Scripts[name] = true
	}
	return project, nil
}

// nodeProjects selects the package.json at each independent lockfile scope.
// A workspace member without its own lockfile is verified by its workspace
// root, while an independent nested workspace gets its own frozen install and
// scripts. A root package without a lockfile retains the historical script-only
// behavior because there is no deterministic install command to run.
func nodeProjects(root string) ([]nodeProjectInfo, bool, error) {
	packages := map[string]string{}
	locked := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		switch entry.Name() {
		case "package.json":
			packages[filepath.Dir(path)] = path
		case "pnpm-lock.yaml", "package-lock.json", "yarn.lock", "bun.lock", "bun.lockb":
			locked[filepath.Dir(path)] = true
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if len(packages) == 0 {
		return nil, false, nil
	}
	scopes := make([]string, 0, len(locked)+1)
	for scope := range locked {
		if _, ok := packages[scope]; ok {
			scopes = append(scopes, scope)
		}
	}
	if _, ok := packages[root]; ok {
		selected := false
		for _, scope := range scopes {
			if scope == root {
				selected = true
				break
			}
		}
		if !selected {
			scopes = append(scopes, root)
		}
	}
	sort.Strings(scopes)
	projects := make([]nodeProjectInfo, 0, len(scopes))
	for _, scope := range scopes {
		project, projectErr := nodeProject(scope, packages[scope], locked[scope])
		if projectErr != nil {
			return nil, false, projectErr
		}
		project.Module = relativePath(root, scope)
		projects = append(projects, project)
	}
	return projects, true, nil
}

func nodeInstallCommand(packageManager string) []string {
	switch packageManager {
	case "pnpm", "bun":
		return []string{packageManager, "install", "--frozen-lockfile"}
	case "yarn":
		return []string{"yarn", "install", "--frozen-lockfile"}
	default:
		return []string{"npm", "ci"}
	}
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
	command := process.CommandContext(ctx, name, args...)
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
	const (
		max       = 1000
		headBytes = 250
	)
	if len(detail) > max {
		tailBytes := max - headBytes
		detail = detail[:headBytes] + fmt.Sprintf("\n… output truncated; final %d bytes:\n", tailBytes) + detail[len(detail)-tailBytes:]
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
