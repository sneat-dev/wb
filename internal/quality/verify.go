package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sneat-dev/wb/internal/envguard"
	"github.com/sneat-dev/wb/internal/filewrite"
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
	// GoLintCommands replaces the default `go vet ./...` lint step with the
	// repository-owned argv sequences from .wb/quality.yaml. Structured argv
	// keeps exact tool pins reproducible without invoking a shell.
	GoLintCommands [][]string
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
	// gain `-p 1` and never `-race`; package-script Node runs gain
	// `--parallel=1` and `--maxWorkers=1`, while mixed Nx target runs gain
	// only Nx's executor-neutral `--parallel=1`. The Nx daemon and cache are
	// disabled in either case.
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
	// Deadcode retains complete machine-comparable findings separately from
	// the bounded human diagnostic. A non-nil incomplete value fails closed.
	Deadcode *DeadcodeFailureEvidence `yaml:"deadcode,omitempty" json:"deadcode,omitempty"`
	Attempts int                      `yaml:"attempts,omitempty" json:"attempts,omitempty"`
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
			for _, command := range goCommands(check, options) {
				entry := runVerification(ctx, options, "go", relativePath(path, module), check, module, command...)
				report.Results = append(report.Results, entry)
			}
		}
	}
	if nodes, ok, err := nodeProjects(path); err != nil {
		report.Results = append(report.Results, VerificationEntry{Language: "node", Status: StatusFailed, Detail: err.Error()})
	} else if ok {
		for _, node := range nodes {
			hasScript := false
			for _, check := range checks {
				if check != CheckSpec && (node.Scripts[string(check)] || node.Nx) {
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
				if !node.Scripts[string(check)] && !node.Nx {
					report.Results = append(report.Results, VerificationEntry{Language: "node", Module: node.Module, Check: check, Status: StatusSkipped, Detail: "script is not defined"})
					continue
				}
				command := nodeCheckCommand(node.PackageManager, check, node.Nx && !node.Scripts[string(check)], options.SingleWorker)
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

func goCommand(check Check, singleWorker bool, timeout time.Duration) []string {
	switch check {
	case CheckLint:
		return []string{"go", "vet", "./..."}
	case CheckTest:
		testTimeout := timeout.String()
		if timeout == 0 {
			testTimeout = "0"
		}
		if singleWorker {
			// -p 1 bounds how many packages compile and run at once, which is
			// the knob that keeps a verification run inside the workstation's
			// concurrency cap. -race is deliberately absent: it multiplies
			// wall time and memory, and CI on the stream pull request owns it.
			return []string{"go", "test", "-timeout", testTimeout, "-p", "1", "./..."}
		}
		return []string{"go", "test", "-timeout", testTimeout, "./..."}
	case CheckBuild:
		return []string{"go", "build", "./..."}
	default:
		return nil
	}
}

func goCommands(check Check, options RunOptions) [][]string {
	if check == CheckLint && len(options.GoLintCommands) > 0 {
		commands := make([][]string, len(options.GoLintCommands))
		for i := range options.GoLintCommands {
			commands[i] = append([]string(nil), options.GoLintCommands[i]...)
		}
		return commands
	}
	return [][]string{goCommand(check, options.SingleWorker, options.Timeout)}
}

// nodeCheckCommand runs an explicit package script when one exists. An Nx
// workspace need not duplicate every project target as a root script, so WB
// falls back to Nx run-many and executes the target across applicable projects.
func nodeCheckCommand(packageManager string, check Check, nxTarget, singleWorker bool) []string {
	if nxTarget {
		// The locked install above has already prepared this scope. Invoke its
		// local Nx entrypoint directly so package-manager "exec" hooks cannot
		// start a second dependency-status install before the actual check.
		command := []string{"node", filepath.FromSlash("node_modules/nx/dist/bin/nx.js")}
		command = append(command, "run-many", "--target="+string(check), "--all", "--skip-nx-cache")
		if singleWorker {
			command = append(command, "--parallel=1")
		}
		return command
	}
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
		if isDeadcodeVerificationCommand(language, check, command) {
			entry.Deadcode = &DeadcodeFailureEvidence{}
			// A timeout or another execution failure must not become an inherited
			// finding merely because the subprocess emitted a complete report.
			if err.Error() == "exit status 1" && checkCtx.Err() == nil {
				entry.Deadcode = parseDeadcodeFailureEvidence(output)
			}
		}
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
	return runShardedVerificationInjected(ctx, options, module, nil)
}

// runShardedVerificationInjected is runShardedVerification's test seam
// (task-9 PR-9): every production call site reaches it only through
// runShardedVerification, which always passes a nil *filewrite.Injector, so
// production behaviour is unchanged. A test passes its own Injector to
// reach the scratch reservation's create/close failure branches
// deterministically.
func runShardedVerificationInjected(ctx context.Context, options RunOptions, module string, inj *filewrite.Injector) (string, int, error) {
	profilePath, err := filewrite.CreateScratch("", "wb-verify-coverage-*.out", 0, nil, inj)
	if err != nil {
		if profilePath != "" {
			_ = os.Remove(profilePath)
		}
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
	Nx             bool
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
	if info, statErr := os.Lstat(filepath.Join(root, "nx.json")); statErr == nil {
		project.Nx = info.Mode().IsRegular()
	} else if !os.IsNotExist(statErr) {
		return nodeProjectInfo{}, fmt.Errorf("inspect nx.json: %w", statErr)
	}
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
	// A failing command usually explains itself in its own output, and that
	// output is the more useful report. But it is not always the same failure:
	// a coverage run whose tests all pass and whose profile merge then fails
	// produces successful-looking test output plus an error describing the real
	// cause, and preferring the output alone discarded that cause entirely.
	// .wb/quality.yaml records the consequence — internal/orchestrate could not
	// be added to the shard list because the failure it produced was unreadable.
	//
	// The error is appended rather than substituted, and appended at the end
	// so the tail-preserving truncation below keeps it whenever the tail
	// survives at all. Heavy failure evidence recovered from the dropped
	// middle (sneat-dev/wb#582) can still consume the whole budget and
	// collapse the tail to nothing, taking this line with it — that is
	// task-6's own stated trade-off ("spending only the remaining budget on
	// head and tail"), not a bug this comment used to rule out.
	if err != nil {
		if failure := strings.TrimSpace(err.Error()); failure != "" {
			switch {
			case detail == "":
				detail = failure
			case !strings.Contains(detail, failure):
				detail = detail + "\n" + failure
			}
		}
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

// truncationMarkerFormat renders the "final N bytes" notice truncated
// output carries. It must always be rendered from the FINAL tail size —
// never a provisional one — so the count it states matches what actually
// follows it.
const truncationMarkerFormat = "\n… output truncated; final %d bytes:\n"

// evidenceHeader introduces the failure evidence truncateCommandDetailTo
// recovers from a truncated command's dropped middle (sneat-dev/wb#582).
// evidenceTruncatedNotice replaces it when even the evidence itself had to
// be cut to fit the budget.
const (
	evidenceHeader          = "\n… output truncated; kept failure evidence from the dropped middle:\n"
	evidenceTruncatedNotice = "\n… (further failure evidence omitted)\n"
)

func truncateCommandDetailTo(detail string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(detail) <= max {
		return detail
	}
	headBytes := max / 4
	if headBytes > 250 {
		headBytes = 250
	}

	// sneat-dev/wb#582: `go test` output is sorted by package, and the
	// overwhelming majority pass, so the failing package (and the
	// assertion that names the real cause) almost never sits in the head
	// or tail a bare head+tail bound keeps — it sits in the middle, which
	// is exactly what got dropped, and a FAIL block can also sit inside
	// what the old bound would have kept as tail, only to be squeezed out
	// once evidence elsewhere shrinks that tail. Scan everything from the
	// start of the line headBytes falls in (so a "--- FAIL" line
	// straddling the head boundary is still seen whole, keeping its
	// indented continuation even when the parent line is itself in the
	// head) all the way to the true end of detail — never only up to some
	// provisional tail boundary — so nothing between the head and the end
	// is ever missed regardless of how much the tail must shrink.
	scanStart := 0
	if idx := strings.LastIndexByte(detail[:headBytes], '\n'); idx >= 0 {
		scanStart = idx + 1
	}
	// Review finding N3: a "--- FAIL"/"panic:" trigger line can end
	// entirely before headBytes while its indented continuation (a
	// sub-test line, an assertion, a stack frame) extends past it.
	// failureEvidenceIn only recognizes a continuation line as evidence
	// when it can see the trigger line that introduced it, so walk
	// scanStart back over any run of indented continuation lines until a
	// non-continuation line is reached — that line, if it is itself the
	// trigger, is included in the scanned region too.
	for scanStart > 0 {
		lineEnd := strings.IndexByte(detail[scanStart:], '\n')
		var line string
		if lineEnd < 0 {
			line = detail[scanStart:]
		} else {
			line = detail[scanStart : scanStart+lineEnd]
		}
		if !isIndentedContinuationLine(line) {
			break
		}
		prevNewline := strings.LastIndexByte(detail[:scanStart-1], '\n')
		if prevNewline < 0 {
			scanStart = 0
			break
		}
		scanStart = prevNewline + 1
	}
	// legacyTailBytes and legacyMarker are the historical head+tail-only
	// shape (unchanged formula). When nothing worth keeping was found,
	// this is returned byte-for-byte as before this fix.
	legacyTailBytes, legacyMarker := sizeTailAndMarker(max - headBytes)
	legacy := detail[:headBytes] + legacyMarker + detail[len(detail)-legacyTailBytes:]

	evidenceLines := failureEvidenceIn(detail, scanStart)
	// Review finding N3 (continued): the walk-back above can pull a
	// trigger line, or other lines, that are already fully reproduced
	// verbatim in the head into the evidence scan. Drop any evidence line
	// whose own source bytes lie entirely inside the head window — a
	// straddling line's own bytes only partially overlap that window, so
	// it is left alone — rather than showing it twice.
	evidenceLines = excludeLinesInWindow(evidenceLines, 0, headBytes)
	if len(evidenceLines) == 0 {
		return legacy
	}

	available := max - headBytes
	evidenceBlock := fitEvidenceBlock(evidenceLines, available)

	// evidenceCollapsedFallback is used once evidence was actually found but
	// ends up empty anyway — either every candidate line sat inside the
	// tail window, or fitEvidenceBlock itself kept nothing (review finding
	// N3: an over-long trigger line whose only possible fragment fell below
	// the minimum meaningful length). Unlike legacy above, this must never
	// exceed max (review finding B1: the evidence path's own contract), so
	// it fits the tail and marker to budget rather than reusing legacy's
	// byte-for-byte historical formula, which is allowed to overflow at a
	// tiny budget only when no evidence was ever found at all.
	evidenceCollapsedFallback := func() string {
		tailBytes, marker := fitTailAndMarkerToBudget(available)
		var tail string
		if tailBytes > 0 {
			tail = detail[len(detail)-tailBytes:]
		}
		return detail[:headBytes] + marker + tail
	}

	// Review finding N2: an evidence line whose own source bytes lie
	// entirely inside the kept tail was never actually dropped — showing
	// it again under the "dropped middle" header both misstates what
	// happened and wastes bytes on a duplicate. Filtering those out
	// shrinks evidenceBlock, which frees budget the tail can grow into;
	// growing the tail can in turn newly swallow an evidence line that
	// survived the smaller tail, but never the reverse (the tail only
	// ever extends further back from the end). Every non-stable pass
	// below either returns directly (once excludeLinesInWindow empties
	// evidenceLines) or strictly shrinks evidenceLines by at least one
	// line before looping again, so a stable pass — which always returns
	// too — is reached in at most the starting len(evidenceLines) passes:
	// this loop always returns from inside its own body and never falls
	// out the bottom.
	for {
		remaining := available - len(evidenceBlock)
		// Unlike the legacy fallback above (which reproduces the
		// historical formula byte-for-byte, including its own
		// long-standing imprecision at a budget too small to fit even a
		// zero-byte marker), the evidence path must never exceed max
		// (review finding B1): evidenceBlock has already consumed part
		// of the head's own remaining room, so the same imprecision here
		// would push the total over budget. fitTailAndMarkerToBudget
		// shrinks the tail by exactly the overflow at a marker
		// digit-count boundary instead of dropping the whole tail over
		// one byte (review finding B1, round 5).
		tailBytes, marker := fitTailAndMarkerToBudget(remaining)
		var tail string
		if tailBytes > 0 {
			tail = detail[len(detail)-tailBytes:]
		}
		filtered := excludeLinesInWindow(evidenceLines, len(detail)-tailBytes, len(detail))
		if len(filtered) == len(evidenceLines) {
			// Stable: this tail's size swallowed nothing new. Review
			// finding N3 (continued): fitEvidenceBlock can itself decide
			// nothing survives (e.g. the one over-long trigger line
			// could not meet the minimum meaningful fragment length) —
			// that is exactly the "nothing worth keeping" case the
			// legacy shape covers, so fall back to it rather than leave
			// output shorter than the budget for no benefit.
			if evidenceBlock == "" {
				return evidenceCollapsedFallback()
			}
			return detail[:headBytes] + evidenceBlock + marker + tail
		}
		evidenceLines = filtered
		if len(evidenceLines) == 0 {
			return evidenceCollapsedFallback()
		}
		evidenceBlock = fitEvidenceBlock(evidenceLines, available)
	}
}

// excludeLinesInWindow drops every evidence line whose own source bytes lie
// entirely within [start, end) — the actually emitted head or tail window —
// leaving every other line (including one that only partially overlaps the
// window, e.g. a "--- FAIL" line straddling the head boundary) untouched, in
// its original order. Filtering by an evidence line's own recorded position,
// rather than by matching its text against what the window contains, is
// what review finding B (round 4 of #582) requires: two different failing
// tests can legitimately share identical assertion text (a shared helper, a
// table-driven case, "context deadline exceeded"), and a text-based filter
// silently discarded whichever occurrence sat in the middle even though its
// own bytes were never actually shown anywhere in the output.
func excludeLinesInWindow(lines []evidenceLine, start, end int) []evidenceLine {
	if start >= end {
		return lines
	}
	kept := make([]evidenceLine, 0, len(lines))
	for _, line := range lines {
		lineEnd := line.offset + len(line.text)
		if line.offset >= start && lineEnd <= end {
			continue
		}
		kept = append(kept, line)
	}
	return kept
}

// sizeTailAndMarker renders the truncation marker from the tail size it
// actually leaves room for — recomputing once the marker's own rendered
// length is known, exactly as the historical algorithm did. It is a
// byte-for-byte reproduction of that historical formula, including its
// own long-standing imprecision when budget is too small to fit even a
// zero-byte marker (the legacy fallback above relies on this exact
// parity); a caller that must not exceed budget corrects for that itself.
func sizeTailAndMarker(budget int) (tailBytes int, marker string) {
	if budget <= 0 {
		return 0, ""
	}
	tailBytes = budget - 64
	if tailBytes < 0 {
		tailBytes = 0
	}
	marker = fmt.Sprintf(truncationMarkerFormat, tailBytes)
	tailBytes = budget - len(marker)
	if tailBytes < 0 {
		tailBytes = 0
	}
	marker = fmt.Sprintf(truncationMarkerFormat, tailBytes)
	return tailBytes, marker
}

// fitTailAndMarkerToBudget wraps sizeTailAndMarker for a caller that must
// never exceed budget (review finding B1, round 5 of #582): at a marker
// digit-count boundary (9→10, 99→100, 999→1000), sizeTailAndMarker's own
// second rendering pass can grow the marker by one byte, one byte past what
// its first pass already sized the tail for. The evidence path cannot reuse
// the legacy fallback's tolerance for that historical imprecision — it has
// already spent part of the head's own room on evidence, so the same
// one-byte slip pushes the total over max. Rather than drop the marker and
// the whole tail over that single byte, shrink the tail by exactly the
// overflow and re-render: shrinking tailBytes can only shorten or hold its
// digit count, never lengthen it, so this one correction cannot overflow
// again. Only when budget cannot fit even a zero-byte-tail marker does the
// tail (and its marker) drop entirely, exactly as sizeTailAndMarker's own
// last resort already does.
func fitTailAndMarkerToBudget(budget int) (tailBytes int, marker string) {
	tailBytes, marker = sizeTailAndMarker(budget)
	if overflow := len(marker) + tailBytes - budget; overflow > 0 {
		tailBytes -= overflow
		if tailBytes < 0 {
			tailBytes = 0
		}
		marker = fmt.Sprintf(truncationMarkerFormat, tailBytes)
		if len(marker)+tailBytes > budget {
			marker, tailBytes = "", 0
		}
	}
	return tailBytes, marker
}

// minPartialEvidenceLineBytes is the shortest rune-safe prefix of an
// over-long evidence line worth keeping on its own (review finding N3,
// round 4 of #582): a fragment shorter than this — a handful of bytes of a
// test name with no assertion text — conveys nothing a reader could act on.
// Below this length, fitEvidenceBlock omits the line entirely rather than
// keeping a fragment that would misstate what evidence exists.
const minPartialEvidenceLineBytes = 40

// fitEvidenceBlock renders every kept evidence line under evidenceHeader,
// never returning more than available bytes. When the full block does not
// fit, it keeps as many whole lines (in the order found) as fit alongside
// evidenceTruncatedNotice, so a cut always lands on a line boundary and
// discloses that it happened. A single line too long to fit whole still
// contributes its own rune-safe prefix rather than being dropped entirely,
// but only when that prefix is at least minPartialEvidenceLineBytes long
// (review finding N5, tightened by finding N3 in round 4) — a shorter
// fragment is omitted instead. When nothing at all fits — including when
// available is too small to fit even the header and the omission notice
// together (review finding N4), or when the only candidate line's prefix
// falls below the minimum — fitEvidenceBlock returns "" rather than a
// header promising evidence that never follows it.
func fitEvidenceBlock(lines []evidenceLine, available int) string {
	full := evidenceHeader + joinEvidenceLines(lines) + "\n"
	if len(full) <= available {
		return full
	}
	budget := available - len(evidenceHeader) - len(evidenceTruncatedNotice)
	if budget < 0 {
		return ""
	}
	var kept []string
	used := 0
	for _, line := range lines {
		text := line.text
		separator := 0
		if len(kept) > 0 {
			separator = 1 // the "\n" strings.Join would place before it
		}
		room := budget - used - separator
		if room <= 0 {
			break
		}
		if len(text) <= room {
			kept = append(kept, text)
			used += separator + len(text)
			continue
		}
		if room >= minPartialEvidenceLineBytes {
			if partial := truncateRuneSafe(text, room); partial != "" {
				kept = append(kept, partial)
			}
		}
		break
	}
	if len(kept) == 0 {
		return ""
	}
	return evidenceHeader + strings.Join(kept, "\n") + evidenceTruncatedNotice
}

// joinEvidenceLines renders lines' own text, one per line, exactly as
// strings.Join(lines, "\n") would for a []string.
func joinEvidenceLines(lines []evidenceLine) string {
	texts := make([]string, len(lines))
	for i, line := range lines {
		texts[i] = line.text
	}
	return strings.Join(texts, "\n")
}

// truncateRuneSafe returns the longest prefix of s that is at most max
// bytes and never splits a multibyte UTF-8 rune.
func truncateRuneSafe(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

var (
	failBlockLinePattern = regexp.MustCompile(`^--- FAIL\b`)
	bareFailLinePattern  = regexp.MustCompile(`^FAIL\b`)
	panicLinePattern     = regexp.MustCompile(`^panic:`)
	// goroutineHeaderPattern matches the line the Go runtime always prints
	// to introduce a goroutine's frames in a panic dump, e.g. "goroutine 6
	// [running]:". A real panic's stack is always followed by exactly one
	// blank line before this header (review finding B, round 2 of #582):
	// isEndOfPanicStack must not treat that blank line as the end of the
	// stack before the header — and the frames after it — have been seen.
	goroutineHeaderPattern = regexp.MustCompile(`^goroutine \d+ \[`)
	// packageSummaryLinePattern matches the per-package result and status
	// lines `go test` output is otherwise made of — "ok  \t<pkg>\t...",
	// "--- PASS: ...", "=== RUN  ...", "PASS", "?   \t<pkg>\t[no test
	// files]", "coverage: ...", and a shard-index label such as
	// "[unsharded packages]" (internal/quality's own coverage-diagnostics
	// output). A panic's stack trace never looks like one of these, so
	// seeing one again ends the stack even when none of its lines are
	// blank or indented (sneat-dev/wb#582's own example interleaves
	// plain, unindented stack frames with file:line frames that are
	// themselves unindented too).
	packageSummaryLinePattern = regexp.MustCompile(`^(ok\s|--- PASS\b|=== |PASS$|\?\s|coverage:|\[.*\])`)
)

// evidenceLine is one line failureEvidenceIn kept, together with offset —
// its own absolute byte position within the full command detail
// truncateCommandDetailTo was called with (never a position relative to
// region alone). Carrying offset is what lets truncateCommandDetailTo tell
// two occurrences of identical text apart (review finding B, round 4 of
// #582): de-duplication against the head or tail window must drop a line
// only when its own source bytes sit inside that window, never merely
// because its text happens to match something found there.
type evidenceLine struct {
	text   string
	offset int
}

// failureEvidenceIn returns every "^--- FAIL" block (its own line plus any
// indented continuation lines, e.g. a sub-test line and its assertion
// message), every bare "^FAIL" line, and every "^panic:" line together
// with its stack trace, found in detail[scanStart:], as the whole lines
// they were found in — never a partial line — so a caller can join, cap, or
// inspect them without ever risking a partial-line false match, and each
// tagged with its own absolute offset in detail. It is used only on the
// slice of a command's output a head+tail bound would otherwise discard
// whole (sneat-dev/wb#582).
func failureEvidenceIn(detail string, scanStart int) []evidenceLine {
	region := detail[scanStart:]
	lines := strings.Split(region, "\n")
	offsets := make([]int, len(lines))
	pos := scanStart
	for i, line := range lines {
		offsets[i] = pos
		pos += len(line) + 1 // +1 for the '\n' strings.Split consumed
	}
	var kept []evidenceLine
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		switch {
		case failBlockLinePattern.MatchString(line):
			kept = append(kept, evidenceLine{line, offsets[index]})
			index++
			for index < len(lines) && isIndentedContinuationLine(lines[index]) {
				kept = append(kept, evidenceLine{lines[index], offsets[index]})
				index++
			}
			index--
		case panicLinePattern.MatchString(line):
			kept = append(kept, evidenceLine{line, offsets[index]})
			index++
			for index < len(lines) {
				var next string
				hasNext := index+1 < len(lines)
				if hasNext {
					next = lines[index+1]
				}
				if isEndOfPanicStack(lines[index], next, hasNext) {
					break
				}
				kept = append(kept, evidenceLine{lines[index], offsets[index]})
				index++
			}
			index--
		case bareFailLinePattern.MatchString(line):
			kept = append(kept, evidenceLine{line, offsets[index]})
		}
	}
	return kept
}

// isEndOfPanicStack reports whether line ends a panic's stack trace: any go
// test result line that means normal package output has resumed, or a
// blank line that is not immediately followed by another goroutine header.
// The Go runtime always prints exactly one blank line right after "panic:
// ..." and before "goroutine N [running]:" (review finding B, round 2 of
// #582), and again between each goroutine's own frames in a multi-goroutine
// dump such as a test-timeout panic (review finding N5, round 4) — treating
// either of those blank lines as the end of the stack drops a goroutine
// header, and every frame under it, that the budget had room to keep. A
// blank line whose next line is itself a goroutine header is therefore never
// the end; every other blank line is, including the very last line of the
// scanned region (hasNext is false and so nothing can follow it).
func isEndOfPanicStack(line string, nextLine string, hasNext bool) bool {
	if strings.TrimSpace(line) == "" {
		return !hasNext || !goroutineHeaderPattern.MatchString(nextLine)
	}
	return failBlockLinePattern.MatchString(line) ||
		bareFailLinePattern.MatchString(line) ||
		packageSummaryLinePattern.MatchString(line)
}

// isIndentedContinuationLine reports whether line is part of the indented
// block a "--- FAIL" line introduces (a sub-test's own "--- FAIL" line, or
// the assertion message under it), rather than the next unrelated line of
// `go test` output.
func isIndentedContinuationLine(line string) bool {
	return line != "" && (line[0] == ' ' || line[0] == '\t')
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
