package quality

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDqCovRunCoverageWithOptionsRejectsImpossibleSharding covers the two
// callers' mistakes that must fail before any subprocess starts.
func TestDqCovRunCoverageWithOptionsRejectsImpossibleSharding(t *testing.T) {
	t.Parallel()
	module := t.TempDir()
	if _, _, err := runCoverageWithOptions(context.Background(), RunOptions{GoTestShards: 1, GoShardPackages: []string{"./serial"}}, module, filepath.Join(module, "p.out")); err == nil || !strings.Contains(err.Error(), "at least 2 shards") {
		t.Fatalf("single-shard error = %v, want the shard minimum", err)
	}
	if _, _, err := runCoverageWithOptions(context.Background(), RunOptions{GoTestShards: 2}, module, filepath.Join(module, "p.out")); err == nil || !strings.Contains(err.Error(), "at least one explicit shard package") {
		t.Fatalf("missing-package error = %v, want the package requirement", err)
	}
}

// TestDqCovRunCoverageWithOptionsBoundsTheWholeShardedRun proves the overall
// deadline (not only the per-shard attempt deadline) terminates the run and is
// named in the returned error.
func TestDqCovRunCoverageWithOptionsBoundsTheWholeShardedRun(t *testing.T) {
	module := t.TempDir()
	dqCovFakeGo(t, module)
	dqCovSetGoEnv(t, map[string]string{
		"DQCOV_GO_LIST_MAIN":  "./serial",
		"DQCOV_GO_LIST_OTHER": "./serial",
		"DQCOV_GO_TEST_LIST":  "TestAlpha",
		"DQCOV_GO_SLEEP":      "5",
	})
	started := time.Now()
	_, _, err := runCoverageWithOptions(context.Background(), RunOptions{
		Timeout: 300 * time.Millisecond, ShardAttemptTimeout: 20 * time.Second,
		GoTestShards: 2, GoShardPackages: []string{"./serial"},
	}, module, filepath.Join(module, "merged.out"))
	if err == nil || !strings.Contains(err.Error(), "timed out after 300ms") {
		t.Fatalf("overall deadline error = %v, want the overall timeout named", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("overall deadline took %s, want the shard processes terminated promptly", elapsed)
	}
}

// TestDqCovRunShardedCoverageWithDiagnosticsDelegates pins the non-options
// wrapper used by callers that have no progress or retry policy.
func TestDqCovRunShardedCoverageWithDiagnosticsDelegates(t *testing.T) {
	module := t.TempDir()
	dqCovFakeGo(t, module)
	dqCovSetGoEnv(t, map[string]string{
		"DQCOV_GO_LIST_MAIN":      "./serial",
		"DQCOV_GO_LIST_OTHER":     "./serial",
		"DQCOV_GO_TEST_LIST":      "TestAlpha",
		"DQCOV_GO_WRITE_PROFILE":  "1",
		"DQCOV_GO_TEST_OUT":       "shard output",
		"DQCOV_GO_PROFILE_SHARD2": "",
	})
	merged := filepath.Join(module, "merged.out")
	_, err := runShardedCoverageWithDiagnostics(context.Background(), module, merged, []string{"./serial"}, 2, "", "")
	if err != nil {
		t.Fatalf("delegated sharded coverage: %v", err)
	}
	statements, covered, err := profileTotals(merged)
	if err != nil || statements == 0 || covered == 0 {
		t.Fatalf("merged totals = %d/%d err=%v, want a non-zero union", covered, statements, err)
	}
}

// TestDqCovRunShardedCoverageOptionsRejectsBadPlans drives every discovery
// rejection through the injected shim: an unusable module listing, a pattern
// that resolves to zero or several packages, a duplicate package, a package
// whose tests cannot be discovered, and one with no tests at all.
func TestDqCovRunShardedCoverageOptionsRejectsBadPlans(t *testing.T) {
	run := func(t *testing.T, env map[string]string, packages []string) error {
		t.Helper()
		module := t.TempDir()
		dqCovFakeGo(t, module)
		dqCovSetGoEnv(t, env)
		_, _, err := runShardedCoverageWithDiagnosticsAndProgressOptions(context.Background(), module, filepath.Join(module, "m.out"), packages, 2, "", "", 0, 0, nil)
		return err
	}

	tests := []struct {
		name     string
		env      map[string]string
		packages []string
		want     string
	}{
		{
			name: "module listing fails",
			env:  map[string]string{"DQCOV_GO_LIST_FAIL": "1", "DQCOV_GO_LIST_STDERR": "listing exploded"},
			want: "go list ./...",
		},
		{
			name:     "requested pattern resolves to nothing",
			env:      map[string]string{"DQCOV_GO_LIST_MAIN": "./serial", "DQCOV_GO_LIST_OTHER": ""},
			packages: []string{"./serial"},
			want:     "returned no packages",
		},
		{
			name:     "requested pattern resolves to several packages",
			env:      map[string]string{"DQCOV_GO_LIST_MAIN": "./serial", "DQCOV_GO_LIST_OTHER": "./serial\n./serial/child"},
			packages: []string{"./serial"},
			want:     "resolved to 2 packages",
		},
		{
			name:     "duplicate requested package",
			env:      map[string]string{"DQCOV_GO_LIST_MAIN": "./serial", "DQCOV_GO_LIST_OTHER": "./serial"},
			packages: []string{"./serial", "./serial"},
			want:     "duplicate shard package",
		},
		{
			name:     "test discovery fails",
			env:      map[string]string{"DQCOV_GO_LIST_MAIN": "./serial", "DQCOV_GO_LIST_OTHER": "./serial", "DQCOV_GO_DISCOVER_FAIL": "discovery exploded"},
			packages: []string{"./serial"},
			want:     "discover tests in ./serial",
		},
		{
			name:     "package without tests",
			env:      map[string]string{"DQCOV_GO_LIST_MAIN": "./serial", "DQCOV_GO_LIST_OTHER": "./serial", "DQCOV_GO_TEST_LIST": ""},
			packages: []string{"./serial"},
			want:     "plan ./serial: no tests",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := run(t, test.env, test.packages)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestDqCovRunShardedCoverageOptionsFailsWhenTemporaryRootIsUnusable covers the
// scratch-directory failure that must stop the run before any shard starts.
func TestDqCovRunShardedCoverageOptionsFailsWhenTemporaryRootIsUnusable(t *testing.T) {
	module := t.TempDir()
	dqCovFakeGo(t, module)
	dqCovSetGoEnv(t, map[string]string{"DQCOV_GO_LIST_MAIN": "./serial", "DQCOV_GO_LIST_OTHER": "./serial"})
	t.Setenv("TMPDIR", filepath.Join(module, "missing"))
	_, _, err := runShardedCoverageWithDiagnosticsAndProgressOptions(context.Background(), module, filepath.Join(module, "m.out"), []string{"./serial"}, 2, "", "", 0, 0, nil)
	if err == nil {
		t.Fatal("an unusable temporary root was accepted")
	}
}

// TestDqCovRunShardedCoverageMergesEverySuccessfulJobAndReportsProgress records
// the full success path: the unsharded job and both process-isolated shards run,
// their output is labelled even without a trailing newline, and the merged
// profile is published.
func TestDqCovRunShardedCoverageMergesEverySuccessfulJobAndReportsProgress(t *testing.T) {
	module := t.TempDir()
	dqCovFakeGo(t, module)
	dqCovSetGoEnv(t, map[string]string{
		"DQCOV_GO_LIST_MAIN":     "./serial\n./other",
		"DQCOV_GO_LIST_OTHER":    "./serial",
		"DQCOV_GO_TEST_LIST":     "TestAlpha\nTestBeta",
		"DQCOV_GO_WRITE_PROFILE": "1",
		"DQCOV_GO_TEST_OUT":      "shard finished without newline",
	})
	merged := filepath.Join(module, "merged.out")
	var progress []Progress
	output, attempts, err := runShardedCoverageWithDiagnosticsAndProgressOptions(context.Background(), module, merged, []string{"./serial"}, 2, "", "", 0, 0, func(event Progress) {
		progress = append(progress, event)
	})
	if err != nil {
		t.Fatalf("sharded coverage: %v\n%s", err, output)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	for _, want := range []string{"[unsharded packages]", "[./serial shard 1/2]", "[./serial shard 2/2]"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if !strings.HasSuffix(output, "shard finished without newline\n") {
		t.Fatalf("labelled job output was not newline-terminated: %q", output)
	}
	statements, covered, err := profileTotals(merged)
	if err != nil || statements == 0 || covered == 0 {
		t.Fatalf("merged totals = %d/%d err=%v, want a non-zero union", covered, statements, err)
	}
	completed := 0
	for _, event := range progress {
		if event.Total != 3 {
			t.Fatalf("progress total = %d, want 3 jobs: %+v", event.Total, event)
		}
		if event.State == ProgressCompleted {
			completed++
		}
	}
	if completed != 3 {
		t.Fatalf("completed progress events = %d, want 3", completed)
	}
}

// TestDqCovRunShardedCoverageSurfacesMergeAndDiagnosticFailures covers the two
// terminal error joins: incompatible shard profiles and an unwritable
// diagnostics directory.
func TestDqCovRunShardedCoverageSurfacesMergeAndDiagnosticFailures(t *testing.T) {
	t.Parallel()
	t.Run("incompatible shard profiles", func(t *testing.T) {
		module := t.TempDir()
		dqCovFakeGo(t, module)
		dqCovSetGoEnv(t, map[string]string{
			"DQCOV_GO_LIST_MAIN":      "./serial",
			"DQCOV_GO_LIST_OTHER":     "./serial",
			"DQCOV_GO_TEST_LIST":      "TestAlpha\nTestBeta",
			"DQCOV_GO_WRITE_PROFILE":  "1",
			"DQCOV_GO_TEST_OUT":       "ok",
			"DQCOV_GO_PROFILE_SHARD2": "mode: count\npkg/a.go:1.1,2.2 2 1",
		})
		_, _, err := runShardedCoverageWithDiagnosticsAndProgressOptions(context.Background(), module, filepath.Join(module, "m.out"), []string{"./serial"}, 2, "", "", 0, 0, nil)
		if err == nil || !strings.Contains(err.Error(), "mode mismatch") {
			t.Fatalf("merge error = %v, want the profile mode mismatch", err)
		}
	})

	t.Run("unwritable diagnostics directory", func(t *testing.T) {
		module := t.TempDir()
		dqCovFakeGo(t, module)
		dqCovSetGoEnv(t, map[string]string{
			"DQCOV_GO_LIST_MAIN":     "./serial",
			"DQCOV_GO_LIST_OTHER":    "./serial",
			"DQCOV_GO_TEST_LIST":     "TestAlpha",
			"DQCOV_GO_TEST_FAIL":     "1",
			"DQCOV_GO_TEST_FAIL_OUT": "--- FAIL: TestAlpha",
		})
		blocker := filepath.Join(module, "not-a-directory")
		writeQualityFile(t, blocker, "x")
		output, _, err := runShardedCoverageWithDiagnosticsAndProgressOptions(context.Background(), module, filepath.Join(module, "m.out"), []string{"./serial"}, 2, filepath.Join(blocker, "reports"), "example/repo", 0, 0, nil)
		if err == nil || !strings.Contains(err.Error(), "write coverage diagnostics") {
			t.Fatalf("diagnostics error = %v, want the write failure surfaced", err)
		}
		if !strings.Contains(output, "TestAlpha") {
			t.Fatalf("failed shard index was lost:\n%s", output)
		}
	})
}

func TestDqCovFailedGoTestNamesDeduplicatesAndSkipsEmpty(t *testing.T) {
	t.Parallel()
	output := strings.Join([]string{
		"=== RUN   TestAlpha",
		"--- FAIL: TestAlpha (0.01s)",
		"    --- FAIL: TestAlpha/remote (0.00s)",
		"--- FAIL: TestAlpha (0.01s)",
		"--- FAIL: ",
		"--- FAIL: TestBeta",
	}, "\n")
	got := failedGoTestNames(output)
	want := []string{"TestAlpha", "TestAlpha/remote", "TestBeta"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("failedGoTestNames = %v, want %v", got, want)
	}
	if names := failedGoTestNames("no failures here"); len(names) != 0 {
		t.Fatalf("names = %v, want none", names)
	}

	summary := summarizeCoverageFailures(
		[]goCoverageJob{{label: "passing shard"}, {label: "failing shard"}},
		[]goCoverageJobResult{{output: "ok", attempts: 1}, {output: output, err: errors.New("exit status 1")}},
	)
	for _, want := range []string{
		"WB coverage failure index:",
		"[failing shard] TestAlpha",
		"[failing shard] TestAlpha/remote",
		"[failing shard] TestBeta",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "passing shard") {
		t.Fatalf("summary indexed a successful job:\n%s", summary)
	}
}

// TestDqCovGoCoverageArgumentsWithTimeoutOmitsDisabledDeadline pins that a zero
// attempt deadline contributes no -timeout flag at all.
func TestDqCovGoCoverageArgumentsWithTimeoutOmitsDisabledDeadline(t *testing.T) {
	t.Parallel()
	arguments := goCoverageArgumentsWithTimeout(filepath.Join("tmp", "coverage.out"), 0, "./serial", "-run", "^(TestOne)$")
	if joined := strings.Join(arguments, " "); strings.Contains(joined, "-timeout") {
		t.Fatalf("disabled deadline leaked a flag: %s", joined)
	}
}

// TestDqCovWriteCoverageDiagnosticsRetainsRawOutput covers the retention
// contract: only failed jobs are written, an empty failure records its error,
// and a manifest without files is not published.
func TestDqCovWriteCoverageDiagnosticsRetainsRawOutput(t *testing.T) {
	t.Parallel()
	repository := "example/diagnostics"
	module := "/modules/app"

	t.Run("no failures writes no manifest", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		if err := writeCoverageDiagnostics(directory, repository, module, []goCoverageJob{{label: "ok", profilePath: "p"}}, []goCoverageJobResult{{output: "fine", attempts: 1}}); err != nil {
			t.Fatal(err)
		}
		manifestPath := filepath.Join(directory, "coverage-diagnostics-"+coverageDiagnosticStem(repository, module)+".yaml")
		if _, err := os.Stat(manifestPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("manifest stat = %v, want no manifest for a clean run", err)
		}
	})

	t.Run("empty failure output records the error", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		if err := writeCoverageDiagnostics(directory, repository, module, []goCoverageJob{{label: "shard 1"}}, []goCoverageJobResult{{err: errors.New("exit status 1")}}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(directory, "coverage-raw-"+coverageDiagnosticStem(repository, module)+"-1.log"))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != "exit status 1\n" {
			t.Fatalf("raw artifact = %q, want the command error", raw)
		}
	})

	t.Run("unusable directory fails", func(t *testing.T) {
		t.Parallel()
		blocker := filepath.Join(t.TempDir(), "file")
		writeQualityFile(t, blocker, "x")
		err := writeCoverageDiagnostics(filepath.Join(blocker, "reports"), repository, module, []goCoverageJob{{label: "shard 1"}}, []goCoverageJobResult{{err: errors.New("boom")}})
		if err == nil {
			t.Fatal("an unwritable diagnostics directory was accepted")
		}
	})

	t.Run("raw artifact collision fails", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		stem := coverageDiagnosticStem(repository, module)
		if err := os.Mkdir(filepath.Join(directory, "coverage-raw-"+stem+"-1.log"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := writeCoverageDiagnostics(directory, repository, module, []goCoverageJob{{label: "shard 1"}}, []goCoverageJobResult{{output: "boom", err: errors.New("exit status 1")}})
		if err == nil {
			t.Fatal("a colliding raw artifact path was accepted")
		}
	})

	t.Run("manifest collision fails", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		stem := coverageDiagnosticStem(repository, module)
		if err := os.Mkdir(filepath.Join(directory, "coverage-diagnostics-"+stem+".yaml"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := writeCoverageDiagnostics(directory, repository, module, []goCoverageJob{{label: "shard 1"}}, []goCoverageJobResult{{output: "boom", err: errors.New("exit status 1")}})
		if err == nil {
			t.Fatal("a colliding manifest path was accepted")
		}
	})
}

func TestDqCovCoverageDiagnosticForMissingManifestIsNil(t *testing.T) {
	t.Parallel()
	if diagnostic := coverageDiagnosticFor(t.TempDir(), "example/repo", "/modules/app"); diagnostic != nil {
		t.Fatalf("diagnostic = %+v, want nil when no manifest was retained", diagnostic)
	}
}

// TestDqCovGoListPackagesHandlesEveryListingShape asserts the line parsing and
// both failure modes of the package lister.
func TestDqCovGoListPackagesHandlesEveryListingShape(t *testing.T) {
	module := t.TempDir()
	dqCovFakeGo(t, module)

	dqCovSetGoEnv(t, map[string]string{"DQCOV_GO_LIST_MAIN": "./a\n\n  ./b  \n"})
	packages, err := goListPackages(context.Background(), module, "./...")
	if err != nil || strings.Join(packages, ",") != "./a,./b" {
		t.Fatalf("packages = %v err = %v, want trimmed non-empty lines", packages, err)
	}

	dqCovSetGoEnv(t, map[string]string{"DQCOV_GO_LIST_MAIN": ""})
	if _, err := goListPackages(context.Background(), module, "./..."); err == nil || !strings.Contains(err.Error(), "returned no packages") {
		t.Fatalf("empty listing error = %v, want a no-packages failure", err)
	}

	dqCovSetGoEnv(t, map[string]string{"DQCOV_GO_LIST_MAIN": "", "DQCOV_GO_LIST_FAIL": "1", "DQCOV_GO_LIST_STDERR": "listing exploded"})
	if _, err := goListPackages(context.Background(), module, "./..."); err == nil || !strings.Contains(err.Error(), "listing exploded") {
		t.Fatalf("failing listing error = %v, want the stderr surfaced", err)
	}
}

func TestDqCovDiscoverGoTestsFiltersAndReportsFailure(t *testing.T) {
	module := t.TempDir()
	dqCovFakeGo(t, module)

	dqCovSetGoEnv(t, map[string]string{"DQCOV_GO_TEST_LIST": "TestAlpha\nBenchmarkSkipped\nExampleUsage\nFuzzSeed\nnot a test"})
	tests, err := discoverGoTests(context.Background(), module, "./serial")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tests, ",") != "TestAlpha,ExampleUsage,FuzzSeed" {
		t.Fatalf("discovered = %v, want only supported top-level names", tests)
	}

	dqCovSetGoEnv(t, map[string]string{"DQCOV_GO_DISCOVER_FAIL": "discovery exploded"})
	if _, err := discoverGoTests(context.Background(), module, "./serial"); err == nil || !strings.Contains(err.Error(), "discover tests in ./serial") {
		t.Fatalf("discovery error = %v, want the package named", err)
	}
}

// TestDqCovRunGoCoverageJobsClampsParallelismAndRecordsAttempts runs more
// workers than jobs and checks that each job still runs exactly once.
func TestDqCovRunGoCoverageJobsClampsParallelismAndRecordsAttempts(t *testing.T) {
	module := t.TempDir()
	dqCovFakeGo(t, module)
	dqCovSetGoEnv(t, map[string]string{"DQCOV_GO_TEST_OUT": "ok"})
	jobs := []goCoverageJob{{label: "only job", arguments: []string{"test", "./serial"}}}
	results := runGoCoverageJobs(context.Background(), module, jobs, 5, 0, 0, nil)
	if len(results) != 1 || results[0].err != nil || results[0].attempts != 1 {
		t.Fatalf("results = %+v, want one successful single attempt", results)
	}
}

func TestDqCovBoundedCoverageParallelismRejectsEmptyInputs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ requested, jobs, cpu int }{{0, 5, 8}, {8, 0, 8}, {-1, 5, 8}, {8, -1, 8}} {
		if got := boundedCoverageParallelism(tc.requested, tc.jobs, tc.cpu); got != 0 {
			t.Fatalf("boundedCoverageParallelism(%d, %d, %d) = %d, want 0", tc.requested, tc.jobs, tc.cpu, got)
		}
	}
}

// dqCovSetGoEnv applies the shim's control variables for the calling test.
func dqCovSetGoEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for name, value := range env {
		t.Setenv(name, value)
	}
}
