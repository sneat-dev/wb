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

	"github.com/sneat-dev/wb/internal/envguard"
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
	// CheckTimeout bounds one logical verification check, including all of its
	// command attempts and any process-isolated Go shards. Zero leaves the
	// existing per-command Timeout behavior unchanged.
	CheckTimeout time.Duration
	// ShardAttemptTimeout bounds one process-isolated Go test shard attempt.
	// Zero retains Timeout as the shard-attempt bound when Timeout is set.
	ShardAttemptTimeout time.Duration
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
	// SingleWorker constrains every check to one worker, so a verification
	// run cannot exceed the workstation's concurrency cap on its own. Go tests
	// gain `-p 1` and never `-race`; Node runs gain `--parallel=1` and
	// `--maxWorkers=1` with the Nx daemon and cache disabled.
	//
	// Serialization is deliberately *not* a substitute for per-file
	// isolation: nothing here relaxes an isolation flag, because a serialized
	// leak is worse than a flake — it is reproducible and misattributed.
	SingleWorker bool
	// Env is appended to each check's environment as KEY=VALUE entries. It is
	// how a caller states the environment a run must carry (GOWORK=off,
	// NX_DAEMON=false) rather than leaving it to the shell that invoked wb.
	Env []string
	// Progress receives lifecycle events for external checks. Callers may use it
	// for terminal diagnostics; reports remain the authoritative output.
	Progress func(Progress)
}

// SingleWorkerNodeEnv is the environment a single-worker Node run must carry.
// It is exported so a caller that composes its own command still states the
// same environment the profile does.
func SingleWorkerNodeEnv() []string {
	return []string{"CI=1", "NX_DAEMON=false", "NX_SKIP_NX_CACHE=true"}
}

// ProgressState identifies a visible quality-work transition.
type ProgressState string

const (
	ProgressStarted             ProgressState = "started"
	ProgressRetrying            ProgressState = "retrying"
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
	Detail     string
	State      ProgressState
	Status     Status
	Attempts   int
	Completed  int
	Total      int
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
			command := goCommand(check, options.SingleWorker)
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
				command := nodeCheckCommand(node.PackageManager, check, options.SingleWorker)
				entry := runVerification(ctx, options, "node", node.Module, check, node.Path, command...)
				report.Results = append(report.Results, entry)
			}
		}
	}
	if containsCheck(checks, CheckSpec) {
		specRoot := filepath.Join(path, "spec")
		if _, err := os.Stat(specRoot); err == nil {
			report.Results = append(report.Results, specLintOrSkip(ctx, options, path, specRoot))
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

// specLintOrSkip decides whether spec/ requires specscore spec lint or is an
// external SpecScore Plans store, for which SpecScore lint does not apply.
// sneat-co/workbench is the canonical example: SpecScore's Plan repository
// routing stores other projects' Plans there under
// spec/plans/{host}/{owner}/{repo}/... (specscore/specscore
// spec/features/repo-config "Plan repository routing" and spec/features/plan
// REQ:external-source-namespace). Without a specscore.yaml, specscore spec
// lint cannot run at all; with one, specscore 0.49.0 reports structural
// violations (readme-exists, plan-hierarchy) for that layout. wb therefore
// reports the check skipped and does not validate those Plans. A repository
// with a root specscore.yaml entry always runs lint.
func specLintOrSkip(ctx context.Context, options RunOptions, path, specRoot string) VerificationEntry {
	specConfig := filepath.Join(path, "specscore.yaml")
	if _, configErr := os.Lstat(specConfig); configErr != nil {
		if !os.IsNotExist(configErr) {
			return VerificationEntry{Language: "specscore", Check: CheckSpec, Status: StatusFailed, Detail: fmt.Sprintf("inspect SpecScore config %q: %v", specConfig, configErr)}
		}
		external, externalErr := isExternalPlansStore(path, specRoot)
		if externalErr != nil {
			return VerificationEntry{Language: "specscore", Check: CheckSpec, Status: StatusFailed, Detail: fmt.Sprintf("inspect SpecScore Plans layout %q: %v", specRoot, externalErr)}
		}
		if external {
			return VerificationEntry{Language: "specscore", Check: CheckSpec, Status: StatusSkipped, Detail: "external SpecScore Plans store (no specscore.yaml): SpecScore lint does not apply to the spec/plans/{host}/{owner}/{repo}/ layout, and wb does not validate these Plans"}
		}
	}
	return runVerification(ctx, options, "specscore", ".", CheckSpec, path, "specscore", "spec", "lint")
}

// externalStoreLifecycleLockRule is the anchored ignore rule every external
// Plan-store repository carries (specscore/specscore spec/features/plan
// REQ:external-store-lifecycle-lock).
const externalStoreLifecycleLockRule = "/.specscore-lifecycle.lock"

// isExternalPlansStore reports whether the repository at path is an external
// SpecScore Plans store. All of these must hold:
//   - the root .gitignore is a regular file with the exact line
//     /.specscore-lifecycle.lock;
//   - every non-directory entry under specRoot is a regular file that fits
//     externalPlansStorePath. The walk inspects every non-directory entry,
//     not only regular files, so symlinks fail closed: a symlinked spec/ or
//     spec/plans, a symlink anywhere beneath them (which could escape the
//     namespace, see REQ:external-source-namespace), a FIFO or any other
//     special entry disqualifies the store and lint runs;
//   - at least one of those entries lies inside a
//     spec/plans/{host}/{owner}/{repo}/ namespace, so an empty spec/, one
//     holding only directories, or one holding only spec/plans/README.md
//     still runs lint.
//
// Any other spec/ content -- a SpecScore project's own spec/features or
// spec/ideas, or a same-repository spec/plans/{plan-id} tree -- disqualifies
// the layout, and the caller runs specscore spec lint as usual.
func isExternalPlansStore(path, specRoot string) (bool, error) {
	ignored, err := ignoresLifecycleLock(filepath.Join(path, ".gitignore"))
	if err != nil || !ignored {
		return false, err
	}
	fits := true
	namespaced := 0
	err = filepath.WalkDir(specRoot, func(walkPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(specRoot, walkPath)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !entry.Type().IsRegular() || !externalPlansStorePath(rel) {
			fits = false
			return filepath.SkipAll
		}
		if rel != "plans/README.md" {
			namespaced++
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return fits && namespaced > 0, nil
}

// ignoresLifecycleLock reports whether gitignore is a regular file holding
// the exact line externalStoreLifecycleLockRule. As git does, it drops a
// trailing CR and trailing spaces before comparing. A missing or symlinked
// .gitignore (git does not read an in-tree symlinked .gitignore) does not
// qualify.
func ignoresLifecycleLock(gitignore string) (bool, error) {
	info, err := os.Lstat(gitignore)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	contents, err := os.ReadFile(gitignore)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimRight(strings.TrimSuffix(line, "\r"), " ")
		if line == externalStoreLifecycleLockRule {
			return true, nil
		}
	}
	return false, nil
}

// externalPlansStorePath reports whether rel -- a spec/-relative, slash
// separated path of a non-directory entry -- fits the external Plans-store
// layout:
//   - plans/README.md, the optional aggregate index;
//   - plans/{host}/{owner}/{repo}/README.md, a namespace index (exactly
//     README.md; no other file sits at namespace level);
//   - plans/{host}/{owner}/{repo}/{plan-id}/..., anything at least one
//     directory below the repo segment.
//
// {host} must look like a hostname (see plansStoreHost), and {owner} and
// {repo} must be non-empty without a leading dot. SpecScore Plan slugs cannot
// contain a dot (REQ:plan-slug-format), so the hostname rule separates an
// external namespace from a same-repository nested plan such as
// plans/phase-1/core/loop/task/README.md.
func externalPlansStorePath(rel string) bool {
	segments := strings.Split(rel, "/")
	if len(segments) < 2 || segments[0] != "plans" {
		return false
	}
	if len(segments) == 2 {
		// spec/plans/README.md is the optional aggregate index; any other
		// file directly under spec/plans/ is the same-repository flat layout
		// (spec/plans/{plan-slug}.md), not an external namespace.
		return segments[1] == "README.md"
	}
	if len(segments) < 5 {
		// Shorter than spec/plans/{host}/{owner}/{repo}/{something}: a file
		// at host or owner level.
		return false
	}
	if !plansStoreHost(segments[1]) || !plansStoreOwnerOrRepo(segments[2]) || !plansStoreOwnerOrRepo(segments[3]) {
		return false
	}
	// segments[4:] is either the namespace index README.md or content
	// beneath a plan-id directory.
	remainder := segments[4:]
	if len(remainder) == 1 {
		return remainder[0] == "README.md"
	}
	return true
}

// plansStoreHost reports whether segment looks like a hostname: lowercase
// letters, digits, '-' and '.', at least one dot, and dot-separated labels
// that are non-empty and neither start nor end with '-'. That rules out a
// leading or trailing dot or hyphen, "..", ".git" and "not a host".
func plansStoreHost(segment string) bool {
	labels := strings.Split(segment, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

// plansStoreOwnerOrRepo reports whether segment is a usable owner or
// repository segment: non-empty with no leading dot, which rules out ".",
// ".." and ".git".
func plansStoreOwnerOrRepo(segment string) bool {
	return segment != "" && segment[0] != '.'
}

func goCommand(check Check, singleWorker bool) []string {
	switch check {
	case CheckLint:
		return []string{"go", "vet", "./..."}
	case CheckTest:
		if singleWorker {
			// -p 1 bounds how many packages compile and run at once, which is
			// the knob that keeps a verification run inside the workstation's
			// concurrency cap. -race is deliberately absent: it multiplies
			// wall time and memory, and CI on the stream pull request owns it.
			return []string{"go", "test", "-p", "1", "./..."}
		}
		return []string{"go", "test", "./..."}
	case CheckBuild:
		return []string{"go", "build", "./..."}
	default:
		return nil
	}
}

// nodeCheckCommand appends the single-worker flags after the script separator,
// so they reach the underlying runner rather than the package manager.
func nodeCheckCommand(packageManager string, check Check, singleWorker bool) []string {
	command := []string{packageManager, "run", string(check)}
	if !singleWorker {
		return command
	}
	return append(command, "--parallel=1", "--", "--maxWorkers=1")
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
	checkCtx := ctx
	cancel := func() {}
	if options.CheckTimeout > 0 {
		checkCtx, cancel = context.WithTimeout(ctx, options.CheckTimeout)
	}
	defer cancel()
	var output string
	var attempts int
	var err error
	if shardedGoTest {
		output, attempts, err = runShardedVerification(checkCtx, options, dir)
	} else {
		output, attempts, err = runWithOptions(checkCtx, options, dir, command[0], command[1:]...)
	}
	if checkCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
		err = fmt.Errorf("check timed out after %s", options.CheckTimeout)
	}
	entry.Attempts = attempts
	if err != nil {
		entry.Status = StatusFailed
		entry.Detail = commandError(entry.Command, output, err)
		if ambient := envguard.Inspect(os.Environ(), os.TempDir(), dir); !ambient.Empty() {
			entry.Detail = strings.TrimRight(entry.Detail, "\n") + "\n" + ambient.String()
		}
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
	return runWithEnv(ctx, nil, dir, name, args...)
}

// runStdout is run for commands whose stdout is parsed line by line: stderr
// is kept out of the result so a "go: downloading ..." notice from the Go
// tool never masquerades as a package path, and is surfaced only through
// the error.
func runStdout(ctx context.Context, dir, name string, args ...string) (string, error) {
	command := process.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = commandEnv(dir, name, nil)
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(output), err
}

func runWithEnv(ctx context.Context, env []string, dir, name string, args ...string) (string, error) {
	command := process.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = commandEnv(dir, name, env)
	output, err := command.CombinedOutput()
	return string(output), err
}

// commandEnv derives the environment a validation subprocess runs with: every
// WB_AGENT_* variable stripped (an operating agent's identity has no business
// reaching the subprocess under test), and, for "go" itself, GOWORK=off
// unless the repository being validated tracks its own go.work in HEAD. env
// carries the caller's own overrides (RunOptions.Env, an explicit
// "GOWORK=off" a caller already composed) and always wins over both the
// ambient environment and the automatic Go override.
//
// See internal/envguard for why this cannot be plain
// append(os.Environ(), env...): a duplicate key appended at the end does not
// override an ambient entry earlier in the slice for most subprocesses'
// getenv.
func commandEnv(dir, name string, env []string) []string {
	overrides := env
	if name == "go" {
		overrides = append(append([]string(nil), envguard.GoEnvOverrides(dir)...), env...)
	}
	return envguard.SanitizeEnv(os.Environ(), overrides...)
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
		output, err := runWithEnv(attemptCtx, options.Env, dir, name, args...)
		timedOut := attemptCtx.Err() == context.DeadlineExceeded
		cancel()
		if timedOut {
			err = fmt.Errorf("timed out after %s", options.Timeout)
		}
		if err == nil || attempts > options.Retry || ctx.Err() != nil {
			return output, attempts, err
		}
	}
}

func commandError(command, output string, err error) string {
	detail := strings.TrimSpace(output)
	if detail == "" {
		detail = err.Error()
	}
	if strings.HasPrefix(detail, coverageFailureSummaryHeader) {
		if rawStart := strings.Index(detail, coverageRawOutputHeader); rawStart >= 0 {
			// Keep the complete job/test index even when raw process logs need a
			// transport-safe bound. Those logs are retained separately whenever a
			// coverage diagnostics directory is configured.
			summary := detail[:rawStart]
			return summary + truncateCommandDetailTo(detail[rawStart:], 1000-len(summary))
		}
	}
	return truncateCommandDetailTo(detail, 1000)
}

func truncateCommandDetailTo(detail string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(detail) > max {
		headBytes := max / 4
		if headBytes > 250 {
			headBytes = 250
		}
		// Reserve enough space for the truncation notice itself. Its exact
		// length depends only on the rendered tail count.
		tailBytes := max - headBytes - 64
		if tailBytes < 0 {
			tailBytes = 0
		}
		marker := fmt.Sprintf("\n… output truncated; final %d bytes:\n", tailBytes)
		tailBytes = max - headBytes - len(marker)
		if tailBytes < 0 {
			tailBytes = 0
		}
		marker = fmt.Sprintf("\n… output truncated; final %d bytes:\n", tailBytes)
		detail = detail[:headBytes] + marker + detail[len(detail)-tailBytes:]
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
