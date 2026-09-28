package quality

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/envguard"
	"github.com/sneat-dev/wb/internal/filewrite"
	"gopkg.in/yaml.v3"
)

type goCoverageJob struct {
	label       string
	arguments   []string
	profilePath string
}

type goCoverageJobResult struct {
	output        string
	err           error
	diagnosticErr error
	attempts      int
	elapsed       time.Duration
	timeoutSource string
}

var (
	errLogicalCheckTimeout    = errors.New("logical check timeout")
	errCoverageAttemptTimeout = errors.New("coverage attempt timeout")
)

const (
	coverageFailureSummaryHeader = "WB coverage failure index:\n"
	coverageRawOutputHeader      = "WB coverage raw output:\n"
)

// CoverageDiagnosticManifest is intentionally separate from CoverageReport:
// it contains unbounded command output and therefore stays in the private
// report root rather than crossing the bounded hook/session boundary.
type CoverageDiagnosticManifest struct {
	SchemaVersion int    `yaml:"schema_version" json:"schema_version"`
	Repository    string `yaml:"repository" json:"repository"`
	Module        string `yaml:"module" json:"module"`
	// Ambient names the machine-state signals present in the gate's own
	// environment and in the ancestors of TMPDIR and Module when the shard
	// failures below were recorded. Empty when none were observed.
	Ambient envguard.AmbientInputs   `yaml:"ambient,omitempty" json:"ambient,omitempty"`
	Files   []CoverageDiagnosticFile `yaml:"files" json:"files"`
}

type CoverageDiagnosticFile struct {
	Label         string `yaml:"label" json:"label"`
	Path          string `yaml:"path" json:"path"`
	Bytes         int    `yaml:"bytes" json:"bytes"`
	SHA256        string `yaml:"sha256" json:"sha256"`
	ElapsedNS     int64  `yaml:"elapsed_ns,omitempty" json:"elapsed_ns,omitempty"`
	TimeoutSource string `yaml:"timeout_source,omitempty" json:"timeout_source,omitempty"`
}

type plannedGoCoveragePackage struct {
	packagePath string
	shards      [][]string
}

func runCoverageWithOptions(ctx context.Context, options RunOptions, module, profilePath string) (string, int, error) {
	if options.GoTestShards <= 1 && len(options.GoShardPackages) == 0 {
		return runWithOptions(ctx, options, module, "go", "test", "-coverprofile="+profilePath, "./...")
	}
	if options.GoTestShards < 2 {
		return "", 0, fmt.Errorf("go test sharding requires at least 2 shards")
	}
	if len(options.GoShardPackages) == 0 {
		return "", 0, fmt.Errorf("go test sharding requires at least one explicit shard package")
	}

	checkCtx := ctx
	cancel := func() {}
	if options.CheckTimeout > 0 {
		checkCtx, cancel = context.WithTimeoutCause(ctx, options.CheckTimeout, errLogicalCheckTimeout)
	}
	defer cancel()
	shardAttemptTimeout := options.Timeout
	if options.ShardAttemptTimeout > 0 {
		shardAttemptTimeout = options.ShardAttemptTimeout
	}
	discoveryTimeout := options.Timeout
	if discoveryTimeout <= 0 {
		discoveryTimeout = shardAttemptTimeout
	}
	output, attempts, err := runShardedCoverageWithDiagnosticsAndProgressTimeouts(checkCtx, module, profilePath, options.GoShardPackages, options.GoTestShards, options.CoverageDiagnosticsDir, options.CoverageDiagnosticsRepository, discoveryTimeout, shardAttemptTimeout, options.Retry, options.Progress)
	if errors.Is(context.Cause(checkCtx), errLogicalCheckTimeout) {
		return output, attempts, fmt.Errorf("check timed out after %s", options.CheckTimeout)
	}
	return output, attempts, err
}

func runShardedCoverage(ctx context.Context, module, outputProfile string, requestedPackages []string, shardCount int) (string, error) {
	return runShardedCoverageWithDiagnostics(ctx, module, outputProfile, requestedPackages, shardCount, "", "")
}

func runShardedCoverageWithDiagnostics(ctx context.Context, module, outputProfile string, requestedPackages []string, shardCount int, diagnosticsDir, repository string) (string, error) {
	return runShardedCoverageWithDiagnosticsAndProgress(ctx, module, outputProfile, requestedPackages, shardCount, diagnosticsDir, repository, nil)
}

func runShardedCoverageWithDiagnosticsAndProgress(ctx context.Context, module, outputProfile string, requestedPackages []string, shardCount int, diagnosticsDir, repository string, reporter func(Progress)) (string, error) {
	output, _, err := runShardedCoverageWithDiagnosticsAndProgressTimeouts(ctx, module, outputProfile, requestedPackages, shardCount, diagnosticsDir, repository, 0, 0, 0, reporter)
	return output, err
}

func runShardedCoverageWithDiagnosticsAndProgressTimeouts(ctx context.Context, module, outputProfile string, requestedPackages []string, shardCount int, diagnosticsDir, repository string, discoveryTimeout, shardAttemptTimeout time.Duration, retry int, reporter func(Progress)) (string, int, error) {
	allPackages, err := runCoverageDiscoveryCommand(ctx, discoveryTimeout, "list all packages", func(commandCtx context.Context) ([]string, error) {
		return goListPackages(commandCtx, module, "./...")
	})
	if err != nil {
		return "", 0, err
	}
	shardedPackages := make([]string, 0, len(requestedPackages))
	shardedSet := map[string]bool{}
	for _, requested := range requestedPackages {
		packages, err := runCoverageDiscoveryCommand(ctx, discoveryTimeout, "list shard package "+requested, func(commandCtx context.Context) ([]string, error) {
			return goListPackages(commandCtx, module, requested)
		})
		if err != nil {
			return "", 0, err
		}
		if len(packages) != 1 {
			return "", 0, fmt.Errorf("shard package %q resolved to %d packages; name exactly one package", requested, len(packages))
		}
		if shardedSet[packages[0]] {
			return "", 0, fmt.Errorf("duplicate shard package %q", requested)
		}
		shardedSet[packages[0]] = true
		shardedPackages = append(shardedPackages, packages[0])
	}
	sort.Strings(shardedPackages)

	temporaryDirectory, err := os.MkdirTemp("", "wb-go-test-shards-*")
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = os.RemoveAll(temporaryDirectory) }()

	jobs := make([]goCoverageJob, 0, 1+len(shardedPackages)*shardCount)
	unsharded := make([]string, 0, len(allPackages))
	for _, packagePath := range allPackages {
		if !shardedSet[packagePath] {
			unsharded = append(unsharded, packagePath)
		}
	}
	if len(unsharded) > 0 {
		profile := filepath.Join(temporaryDirectory, "unsharded.cov")
		arguments := goCoverageArgumentsWithTimeout(profile, shardAttemptTimeout)
		arguments = append(arguments, unsharded...)
		jobs = append(jobs, goCoverageJob{label: "unsharded packages", arguments: arguments, profilePath: profile})
	}
	plannedPackages := make([]plannedGoCoveragePackage, 0, len(shardedPackages))
	for _, packagePath := range shardedPackages {
		tests, err := runCoverageDiscoveryCommand(ctx, discoveryTimeout, "discover tests in "+packagePath, func(commandCtx context.Context) ([]string, error) {
			return discoverGoTests(commandCtx, module, packagePath)
		})
		if err != nil {
			return "", 0, err
		}
		shards, err := planGoTestShards(tests, shardCount)
		if err != nil {
			return "", 0, fmt.Errorf("plan %s: %w", packagePath, err)
		}
		plannedPackages = append(plannedPackages, plannedGoCoveragePackage{packagePath: packagePath, shards: shards})
	}
	// Interleave packages by shard number. Appending every shard from one slow
	// package first leaves the next package queued behind it and creates a long
	// tail even when both plans are individually balanced.
	for shardIndex := 0; shardIndex < shardCount; shardIndex++ {
		for packageIndex, planned := range plannedPackages {
			if shardIndex >= len(planned.shards) {
				continue
			}
			shard := planned.shards[shardIndex]
			profile := filepath.Join(temporaryDirectory, fmt.Sprintf("package-%d-shard-%d.cov", packageIndex+1, shardIndex+1))
			pattern := "^(" + strings.Join(shard, "|") + ")$"
			jobs = append(jobs, goCoverageJob{
				label:       fmt.Sprintf("%s shard %d/%d", planned.packagePath, shardIndex+1, len(planned.shards)),
				arguments:   goCoverageArgumentsWithTimeout(profile, shardAttemptTimeout, planned.packagePath, "-run", pattern),
				profilePath: profile,
			})
		}
	}

	var diagnostics *coverageDiagnosticsSink
	if diagnosticsDir != "" {
		diagnostics = newCoverageDiagnosticsSink(diagnosticsDir, repository, module)
	}
	persistFailure := func(index int, result goCoverageJobResult) error {
		if diagnostics == nil {
			return nil
		}
		return diagnostics.persist(index, jobs[index], result)
	}
	results := runGoCoverageJobs(ctx, module, jobs, boundedCoverageParallelism(shardCount, len(jobs), runtime.GOMAXPROCS(0)), shardAttemptTimeout, retry, reporter, persistFailure)
	maxAttempts := 1
	for _, result := range results {
		if result.attempts > maxAttempts {
			maxAttempts = result.attempts
		}
	}
	var output strings.Builder
	var failedOutput strings.Builder
	profiles := make([]string, 0, len(jobs))
	var runErr error
	for index, result := range results {
		if result.diagnosticErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("write coverage diagnostics: %w", result.diagnosticErr))
		}
		if result.err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("%s: %w", jobs[index].label, result.err))
		} else {
			if strings.TrimSpace(result.output) != "" {
				fmt.Fprintf(&output, "[%s]\n%s", jobs[index].label, result.output)
				if !strings.HasSuffix(result.output, "\n") {
					output.WriteByte('\n')
				}
			}
			profiles = append(profiles, jobs[index].profilePath)
		}
	}
	if runErr != nil {
		// The command error is deliberately bounded before it crosses a CI or
		// session transport. Put every failing job and Go test name first, so the
		// actionable index survives even when a shard's raw log is enormous.
		failureIndex := summarizeCoverageFailures(jobs, results)
		failedOutput.WriteString(failureIndex)
		failedOutput.WriteString(coverageRawOutputHeader)
		for index, result := range results {
			if result.err == nil {
				continue
			}
			fmt.Fprintf(&failedOutput, "[%s]\n%s", jobs[index].label, result.output)
			if !strings.HasSuffix(result.output, "\n") {
				failedOutput.WriteByte('\n')
			}
			failedOutput.WriteString(result.err.Error())
			failedOutput.WriteByte('\n')
		}
		return failedOutput.String(), maxAttempts, runErr
	}
	if err := mergeCoverageProfiles(profiles, outputProfile); err != nil {
		return output.String(), maxAttempts, err
	}
	return output.String(), maxAttempts, nil
}

// Discovery runs before shard attempts, but it is still external process work.
// Bound each command separately so a zero logical CheckTimeout remains finite
// whenever the caller has configured a per-command timeout.
func runCoverageDiscoveryCommand(ctx context.Context, timeout time.Duration, label string, command func(context.Context) ([]string, error)) ([]string, error) {
	commandCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		commandCtx, cancel = context.WithTimeoutCause(ctx, timeout, errCoverageAttemptTimeout)
	}
	result, err := command(commandCtx)
	timedOut := err != nil && errors.Is(context.Cause(commandCtx), errCoverageAttemptTimeout)
	cancel()
	if timedOut {
		return nil, fmt.Errorf("%s timed out after %s: %w", label, timeout, err)
	}
	return result, err
}

func goCoverageArguments(profile string, arguments ...string) []string {
	result := append([]string{"test"}, arguments...)
	// Do not add -count=1 here: it disables Go's package test-result cache.
	// Use it only when a caller intentionally requires a fresh rerun.
	return append(result, "-coverprofile="+profile)
}

func goCoverageArgumentsWithTimeout(profile string, timeout time.Duration, arguments ...string) []string {
	if timeout > 0 {
		arguments = append([]string{"-timeout", timeout.String()}, arguments...)
	}
	return goCoverageArguments(profile, arguments...)
}

// summarizeCoverageFailures emits the complete compact failure index before
// raw process output. Go reports nested tests as separate `--- FAIL:` lines;
// preserve both the top-level and subtest names because either may identify
// the actual failing journey.
func summarizeCoverageFailures(jobs []goCoverageJob, results []goCoverageJobResult) string {
	var summary strings.Builder
	summary.WriteString(coverageFailureSummaryHeader)
	for index, result := range results {
		if result.err == nil {
			continue
		}
		timing := ""
		if result.timeoutSource != "" {
			kind := "timeout"
			if result.timeoutSource == "caller-cancelled" {
				kind = "cancellation"
			}
			timing = fmt.Sprintf(" (%s %s; elapsed %s)", result.timeoutSource, kind, result.elapsed)
		}
		names := failedGoTestNames(result.output)
		if len(names) == 0 {
			fmt.Fprintf(&summary, "- [%s] command failed without a named Go test%s\n", jobs[index].label, timing)
			continue
		}
		for _, name := range names {
			fmt.Fprintf(&summary, "- [%s] %s%s\n", jobs[index].label, name, timing)
		}
	}
	return summary.String()
}

func failedGoTestNames(output string) []string {
	seen := make(map[string]bool)
	names := make([]string, 0)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "--- FAIL: ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "--- FAIL: "))
		if len(fields) == 0 || seen[fields[0]] {
			continue
		}
		seen[fields[0]] = true
		names = append(names, fields[0])
	}
	return names
}

type coverageDiagnosticsSink struct {
	mu         sync.Mutex
	directory  string
	repository string
	module     string
	stem       string
	manifest   CoverageDiagnosticManifest
	files      map[int]CoverageDiagnosticFile
}

func newCoverageDiagnosticsSink(directory, repository, module string) *coverageDiagnosticsSink {
	return &coverageDiagnosticsSink{
		directory: directory, repository: repository, module: module,
		stem: coverageDiagnosticStem(repository, module),
		manifest: CoverageDiagnosticManifest{
			SchemaVersion: 1, Repository: repository, Module: module,
			Ambient: envguard.Inspect(os.Environ(), os.TempDir(), module),
		},
		files: make(map[int]CoverageDiagnosticFile),
	}
}

func (sink *coverageDiagnosticsSink) persist(index int, job goCoverageJob, result goCoverageJobResult) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if result.err == nil {
		return nil
	}
	if err := os.MkdirAll(sink.directory, 0o700); err != nil {
		return err
	}
	path := filepath.Join(sink.directory, "coverage-raw-"+sink.stem+fmt.Sprintf("-%d.log", index+1))
	raw := []byte(result.output)
	if len(raw) == 0 {
		raw = []byte(result.err.Error() + "\n")
	}
	if err := writeCoverageDiagnosticFileAtomically(path, raw); err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	sink.files[index] = CoverageDiagnosticFile{
		Label: job.label, Path: path, Bytes: len(raw), SHA256: hex.EncodeToString(digest[:]),
		ElapsedNS: int64(result.elapsed), TimeoutSource: result.timeoutSource,
	}
	sink.manifest.Files = sink.manifest.Files[:0]
	indices := make([]int, 0, len(sink.files))
	for jobIndex := range sink.files {
		indices = append(indices, jobIndex)
	}
	sort.Ints(indices)
	for _, jobIndex := range indices {
		sink.manifest.Files = append(sink.manifest.Files, sink.files[jobIndex])
	}
	manifestRaw, err := yaml.Marshal(sink.manifest)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(sink.directory, "coverage-diagnostics-"+sink.stem+".yaml")
	return writeCoverageDiagnosticFileAtomically(manifestPath, manifestRaw)
}

func writeCoverageDiagnosticFileAtomically(path string, data []byte) (err error) {
	directory := filepath.Dir(path)
	temporary, err := filewrite.CreateTemp(directory, ".coverage-diagnostic-*.tmp", nil)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if temporary != nil {
			err = errors.Join(err, filewrite.Close(temporary, temporaryPath, nil))
		}
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryPath, nil); err != nil {
		return err
	}
	if err := filewrite.Write(temporary, data, temporaryPath, nil); err != nil {
		return err
	}
	if err := filewrite.Sync(temporary, temporaryPath, nil); err != nil {
		return err
	}
	if err := filewrite.Close(temporary, temporaryPath, nil); err != nil {
		return err
	}
	temporary = nil
	if err := filewrite.Rename(temporaryPath, path, nil); err != nil {
		return err
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	syncErr := filewrite.SyncDir(directoryFile, nil)
	closeErr := directoryFile.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(syncErr, closeErr)
	}
	return nil
}

func coverageDiagnosticStem(repository, module string) string {
	digest := sha256.Sum256([]byte(repository + "\x00" + module))
	return hex.EncodeToString(digest[:])[:16]
}

func coverageDiagnosticFor(directory, repository, module string) *CoverageDiagnostic {
	manifestPath := filepath.Join(directory, "coverage-diagnostics-"+coverageDiagnosticStem(repository, module)+".yaml")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil
	}
	digest := sha256.Sum256(raw)
	return &CoverageDiagnostic{Manifest: manifestPath, SHA256: hex.EncodeToString(digest[:])}
}

func goListPackages(ctx context.Context, module, pattern string) ([]string, error) {
	output, err := runStdout(ctx, module, "go", "list", "-f", "{{.ImportPath}}", pattern)
	if err != nil {
		return nil, fmt.Errorf("go list %s: %w\n%s", pattern, err, strings.TrimSpace(output))
	}
	var packages []string
	for _, line := range strings.Split(output, "\n") {
		if packagePath := strings.TrimSpace(line); packagePath != "" {
			packages = append(packages, packagePath)
		}
	}
	if len(packages) == 0 {
		return nil, fmt.Errorf("go list %s returned no packages", pattern)
	}
	return packages, nil
}

func discoverGoTests(ctx context.Context, module, packagePath string) ([]string, error) {
	output, err := run(ctx, module, "go", "test", packagePath, "-list", "^(Test|Example|Fuzz)")
	if err != nil {
		return nil, fmt.Errorf("discover tests in %s: %w\n%s", packagePath, err, strings.TrimSpace(output))
	}
	var tests []string
	for _, line := range strings.Split(output, "\n") {
		name := strings.TrimSpace(line)
		if supportedGoTestName.MatchString(name) {
			tests = append(tests, name)
		}
	}
	return tests, nil
}

func runGoCoverageJobs(ctx context.Context, module string, jobs []goCoverageJob, parallel int, timeout time.Duration, retry int, reporter func(Progress), persistFailureCallbacks ...func(int, goCoverageJobResult) error) []goCoverageJobResult {
	var persistFailure func(int, goCoverageJobResult) error
	if len(persistFailureCallbacks) > 0 {
		persistFailure = persistFailureCallbacks[0]
	}
	if parallel > len(jobs) {
		parallel = len(jobs)
	}
	results := make([]goCoverageJobResult, len(jobs))
	indices := make(chan int)
	var wait sync.WaitGroup
	var progressMu sync.Mutex
	completed := 0
	report := func(index int, state ProgressState, status Status, attempts int, detail string) {
		if reporter == nil {
			return
		}
		progressMu.Lock()
		defer progressMu.Unlock()
		if state == ProgressCompleted {
			completed++
		}
		reporter(Progress{
			Language: "go", Module: module, Check: CheckTest,
			Command: "go test", Detail: detail,
			State: state, Status: status, Attempts: attempts, Completed: completed, Total: len(jobs),
		})
	}
	for worker := 0; worker < parallel; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range indices {
				started := time.Now()
				attempts := 0
				var output string
				var err error
				var timeoutSource string
				for {
					attempts++
					timeoutSource = ""
					report(index, ProgressStarted, "", attempts, jobs[index].label)
					jobCtx := ctx
					cancel := func() {}
					if timeout > 0 {
						jobCtx, cancel = context.WithTimeoutCause(ctx, timeout, errCoverageAttemptTimeout)
					}
					output, err = run(jobCtx, module, "go", jobs[index].arguments...)
					cause := context.Cause(jobCtx)
					cancel()
					if err != nil {
						switch {
						case errors.Is(cause, errLogicalCheckTimeout):
							timeoutSource = "check"
							err = fmt.Errorf("logical check deadline exceeded: %w", context.DeadlineExceeded)
						case errors.Is(cause, errCoverageAttemptTimeout):
							timeoutSource = "attempt"
							err = fmt.Errorf("shard attempt timed out after %s", timeout)
						case errors.Is(cause, context.DeadlineExceeded):
							timeoutSource = "caller"
							err = fmt.Errorf("caller deadline exceeded: %w", context.DeadlineExceeded)
						case errors.Is(cause, context.Canceled):
							timeoutSource = "caller-cancelled"
							err = fmt.Errorf("caller canceled: %w", context.Canceled)
						case strings.Contains(output, "panic: test timed out after "):
							// The Go test binary's own -timeout may win the race with
							// this process context and emit its stack trace first.
							timeoutSource = "attempt"
						}
					}
					if err == nil || attempts > retry || ctx.Err() != nil {
						break
					}
					detail := summarizeCoverageFailures([]goCoverageJob{jobs[index]}, []goCoverageJobResult{{output: output, err: err}})
					detail = strings.TrimSpace(strings.TrimPrefix(detail, coverageFailureSummaryHeader))
					if detail == "" {
						detail = "command failed"
					}
					report(index, ProgressRetrying, StatusFailed, attempts, fmt.Sprintf("%s attempt %d failed: %s; retrying", jobs[index].label, attempts, detail))
				}
				result := goCoverageJobResult{output: output, err: err, attempts: attempts, elapsed: time.Since(started), timeoutSource: timeoutSource}
				if err != nil && persistFailure != nil {
					result.diagnosticErr = persistFailure(index, result)
				}
				results[index] = result
				status := StatusPassed
				if err != nil {
					status = StatusFailed
				}
				report(index, ProgressCompleted, status, attempts, jobs[index].label)
			}
		}()
	}
	for index := range jobs {
		indices <- index
	}
	close(indices)
	wait.Wait()
	return results
}

func boundedCoverageParallelism(requested, jobs, effectiveCPU int) int {
	if requested < 1 || jobs < 1 {
		return 0
	}
	limit := effectiveCPU - 1
	if limit < 1 {
		limit = 1
	}
	if requested < limit {
		limit = requested
	}
	if jobs < limit {
		limit = jobs
	}
	return limit
}
