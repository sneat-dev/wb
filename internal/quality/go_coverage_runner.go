package quality

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type goCoverageJob struct {
	label       string
	arguments   []string
	profilePath string
}

type goCoverageJobResult struct {
	output string
	err    error
}

const (
	coverageFailureSummaryHeader = "WB coverage failure index:\n"
	coverageRawOutputHeader      = "WB coverage raw output:\n"
)

// CoverageDiagnosticManifest is intentionally separate from CoverageReport:
// it contains unbounded command output and therefore stays in the private
// report root rather than crossing the bounded hook/session boundary.
type CoverageDiagnosticManifest struct {
	SchemaVersion int                      `yaml:"schema_version" json:"schema_version"`
	Repository    string                   `yaml:"repository" json:"repository"`
	Module        string                   `yaml:"module" json:"module"`
	Files         []CoverageDiagnosticFile `yaml:"files" json:"files"`
}

type CoverageDiagnosticFile struct {
	Label  string `yaml:"label" json:"label"`
	Path   string `yaml:"path" json:"path"`
	Bytes  int    `yaml:"bytes" json:"bytes"`
	SHA256 string `yaml:"sha256" json:"sha256"`
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

	attempts := 0
	for {
		attempts++
		attemptCtx := ctx
		cancel := func() {}
		if options.Timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, options.Timeout)
		}
		output, err := runShardedCoverageWithDiagnostics(attemptCtx, module, profilePath, options.GoShardPackages, options.GoTestShards, options.CoverageDiagnosticsDir, options.CoverageDiagnosticsRepository)
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

func runShardedCoverage(ctx context.Context, module, outputProfile string, requestedPackages []string, shardCount int) (string, error) {
	return runShardedCoverageWithDiagnostics(ctx, module, outputProfile, requestedPackages, shardCount, "", "")
}

func runShardedCoverageWithDiagnostics(ctx context.Context, module, outputProfile string, requestedPackages []string, shardCount int, diagnosticsDir, repository string) (string, error) {
	allPackages, err := goListPackages(ctx, module, "./...")
	if err != nil {
		return "", err
	}
	shardedPackages := make([]string, 0, len(requestedPackages))
	shardedSet := map[string]bool{}
	for _, requested := range requestedPackages {
		packages, err := goListPackages(ctx, module, requested)
		if err != nil {
			return "", err
		}
		if len(packages) != 1 {
			return "", fmt.Errorf("shard package %q resolved to %d packages; name exactly one package", requested, len(packages))
		}
		if shardedSet[packages[0]] {
			return "", fmt.Errorf("duplicate shard package %q", requested)
		}
		shardedSet[packages[0]] = true
		shardedPackages = append(shardedPackages, packages[0])
	}
	sort.Strings(shardedPackages)

	temporaryDirectory, err := os.MkdirTemp("", "wb-go-test-shards-*")
	if err != nil {
		return "", err
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
		arguments := []string{"test", "-count=1", "-coverprofile=" + profile}
		arguments = append(arguments, unsharded...)
		jobs = append(jobs, goCoverageJob{label: "unsharded packages", arguments: arguments, profilePath: profile})
	}
	plannedPackages := make([]plannedGoCoveragePackage, 0, len(shardedPackages))
	for _, packagePath := range shardedPackages {
		tests, err := discoverGoTests(ctx, module, packagePath)
		if err != nil {
			return "", err
		}
		shards, err := planGoTestShards(tests, shardCount)
		if err != nil {
			return "", fmt.Errorf("plan %s: %w", packagePath, err)
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
				arguments:   []string{"test", planned.packagePath, "-run", pattern, "-count=1", "-coverprofile=" + profile},
				profilePath: profile,
			})
		}
	}

	results := runGoCoverageJobs(ctx, module, jobs, shardCount)
	var output strings.Builder
	var failedOutput strings.Builder
	profiles := make([]string, 0, len(jobs))
	var runErr error
	for index, result := range results {
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
		if diagnosticsDir != "" {
			if err := writeCoverageDiagnostics(diagnosticsDir, repository, module, jobs, results); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("write coverage diagnostics: %w", err))
			}
		}
		return failedOutput.String(), runErr
	}
	if err := mergeCoverageProfiles(profiles, outputProfile); err != nil {
		return output.String(), err
	}
	return output.String(), nil
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
		names := failedGoTestNames(result.output)
		if len(names) == 0 {
			fmt.Fprintf(&summary, "- [%s] command failed without a named Go test\n", jobs[index].label)
			continue
		}
		for _, name := range names {
			fmt.Fprintf(&summary, "- [%s] %s\n", jobs[index].label, name)
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

func writeCoverageDiagnostics(directory, repository, module string, jobs []goCoverageJob, results []goCoverageJobResult) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	stem := coverageDiagnosticStem(repository, module)
	manifest := CoverageDiagnosticManifest{SchemaVersion: 1, Repository: repository, Module: module}
	for index, result := range results {
		if result.err == nil {
			continue
		}
		path := filepath.Join(directory, "coverage-raw-"+stem+fmt.Sprintf("-%d.log", index+1))
		raw := []byte(result.output)
		if len(raw) == 0 {
			raw = []byte(result.err.Error() + "\n")
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		manifest.Files = append(manifest.Files, CoverageDiagnosticFile{
			Label: jobs[index].label, Path: path, Bytes: len(raw), SHA256: hex.EncodeToString(digest[:]),
		})
	}
	if len(manifest.Files) == 0 {
		return nil
	}
	manifestRaw, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(directory, "coverage-diagnostics-"+stem+".yaml")
	if err := os.WriteFile(manifestPath, manifestRaw, 0o600); err != nil {
		return err
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
	output, err := run(ctx, module, "go", "list", "-f", "{{.ImportPath}}", pattern)
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

func runGoCoverageJobs(ctx context.Context, module string, jobs []goCoverageJob, parallel int) []goCoverageJobResult {
	if parallel > len(jobs) {
		parallel = len(jobs)
	}
	results := make([]goCoverageJobResult, len(jobs))
	indices := make(chan int)
	var wait sync.WaitGroup
	for worker := 0; worker < parallel; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range indices {
				output, err := run(ctx, module, "go", jobs[index].arguments...)
				results[index] = goCoverageJobResult{output: output, err: err}
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
