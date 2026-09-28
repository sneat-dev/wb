package quality

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCoverageDiscoveryTimeoutAndCallerCancellation(t *testing.T) {
	t.Parallel()
	_, err := runCoverageDiscoveryCommand(context.Background(), time.Nanosecond, "list packages", func(ctx context.Context) ([]string, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "list packages timed out after 1ns") {
		t.Fatalf("discovery timeout = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runCoverageDiscoveryCommand(ctx, time.Second, "list packages", func(ctx context.Context) ([]string, error) {
		return nil, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "timed out") {
		t.Fatalf("caller cancellation was mislabeled: %v", err)
	}
}

func TestDeadcodeFailureEvidenceRejectsMalformedSections(t *testing.T) {
	t.Parallel()
	good := deadcodeFailureOutput("example.test/pkg.A")
	fixed := strings.Replace(good, "error: 1 function(s)", "\nBaseline entries now reachable or gone (1) — rerun with --update-baseline to drop them:\n  example.test/pkg.Gone\nerror: 1 function(s)", 1)
	cases := map[string]string{
		"carriage return":             good + "\r",
		"bad header":                  strings.Replace(good, "New unreachable functions", "Unknown functions", 1),
		"truncated fixed section":     "New unreachable functions (1):\n  internal/example/file.go:1: example.test/pkg.A\n",
		"bad finding":                 strings.Replace(good, "internal/example/file.go:1: example.test/pkg.A", "invalid finding", 1),
		"bad fixed header":            strings.Replace(fixed, "Baseline entries now reachable or gone", "Changed baseline entries", 1),
		"zero fixed count":            strings.Replace(fixed, "gone (1)", "gone (0)", 1),
		"excess fixed count":          strings.Replace(fixed, "gone (1)", "gone (2)", 1),
		"bad fixed identity":          strings.Replace(fixed, "  example.test/pkg.Gone", "  two identities", 1),
		"fixed section without entry": strings.Replace(fixed, "  example.test/pkg.Gone\n", "", 1),
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if evidence := parseDeadcodeFailureEvidence(output); evidence.Valid() {
				t.Fatalf("malformed report accepted: %+v", evidence)
			}
		})
	}
}

func TestValidationCacheRejectsUnusableStorageAndClonesValidators(t *testing.T) {
	t.Parallel()
	validators := map[string]string{"specscore": "first"}
	copied := cloneStringMap(validators)
	validators["specscore"] = "second"
	if copied["specscore"] != "first" || cloneStringMap(nil) != nil {
		t.Fatalf("validator copy aliases input or empty map: %#v", copied)
	}
	key := ValidationCacheKey{Repository: "example/cache", TargetRevision: "revision"}
	report := VerificationReport{Revision: "revision", WorkspaceClean: true, Status: StatusPassed}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveValidationCache(filepath.Join(blocker, "cache"), key, report); err == nil {
		t.Fatal("cache accepted a regular file as its parent directory")
	}
	if _, hit, err := LoadValidationCache(blocker, key); err == nil || hit {
		t.Fatalf("cache read through regular file = hit %v, err %v", hit, err)
	}
	if _, err := NewValidationCacheKey("example/cache", "revision", blocker, "wb", nil, nil, RunOptions{}); err == nil {
		t.Fatal("cache key accepted a regular file as repository root")
	}
}

func TestDeadcodeBaselineStorageErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(blocker, "baseline.txt")
	if _, _, err := LoadDeadcodeBaseline(root); err == nil || !strings.Contains(err.Error(), "read deadcode baseline") {
		t.Fatalf("directory baseline read = %v", err)
	}
	if err := WriteDeadcodeBaseline(nested, nil); err == nil || !strings.Contains(err.Error(), "create deadcode baseline directory") {
		t.Fatalf("baseline parent collision = %v", err)
	}
	if err := WriteDeadcodeBaseline(root, nil); err == nil || !strings.Contains(err.Error(), "write deadcode baseline") {
		t.Fatalf("baseline destination collision = %v", err)
	}
}

func TestDeadcodePassesFilterGeneratedAndPatternOptions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell analyzer fixture requires POSIX")
	}
	root := t.TempDir()
	script := `if [ "$1" != "-json" ] || [ "$2" != "-filter" ] || [ "$3" != "wanted" ] || [ "$4" != "-generated" ] || [ "$5" != "./only" ]; then echo "wrong analyzer arguments" >&2; exit 2; fi
printf '%s\n' '` + twoFindings + `'`
	report, err := Deadcode(context.Background(), root, DeadcodeOptions{
		Tool: []string{"/bin/sh", "-c", script, "deadcode-test"}, Patterns: []string{"./only"}, Filter: "wanted", IncludeGenerated: true,
	})
	if err != nil || len(report.Findings) != 2 {
		t.Fatalf("analyzer options result = %+v, %v", report, err)
	}
}

func TestDeadcodeRejectsUnreadableBaselineAfterAnalysis(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, err := Deadcode(context.Background(), root, DeadcodeOptions{
		Tool: stubAnalyzer(t, twoFindings, 0), BaselinePath: root,
	})
	if err == nil || !strings.Contains(err.Error(), "read deadcode baseline") {
		t.Fatalf("unreadable baseline = %v", err)
	}
}

func TestCoverageDiagnosticAtomicWriterRejectsMissingParent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing", "diagnostic.log")
	if err := writeCoverageDiagnosticFileAtomically(path, []byte("failure")); err == nil {
		t.Fatal("diagnostic was written without a parent directory")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed diagnostic left a published file: %v", err)
	}
}

//nolint:paralleltest // The fake go executable is installed on this test's PATH.
func TestCoverageJobsDistinguishCallerDeadlineAndTestBinaryTimeout(t *testing.T) {
	module := t.TempDir()
	dqCovFakeGo(t, module)
	jobs := []goCoverageJob{{label: "shard", arguments: []string{"test", "./serial"}}}
	dqCovSetGoEnv(t, map[string]string{"DQCOV_GO_SLEEP": "1"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	results := runGoCoverageJobs(ctx, module, jobs, 1, 0, 0, nil)
	if len(results) != 1 || results[0].timeoutSource != "caller" || !errors.Is(results[0].err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline result = %+v", results)
	}
	dqCovSetGoEnv(t, map[string]string{"DQCOV_GO_SLEEP": "", "DQCOV_GO_TEST_FAIL": "1", "DQCOV_GO_TEST_FAIL_OUT": "panic: test timed out after 1s\n"})
	results = runGoCoverageJobs(context.Background(), module, jobs, 1, 0, 0, nil)
	if len(results) != 1 || results[0].timeoutSource != "attempt" || results[0].err == nil {
		t.Fatalf("test binary timeout result = %+v", results)
	}
}
