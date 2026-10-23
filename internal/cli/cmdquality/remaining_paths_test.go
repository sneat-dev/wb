package cmdquality

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
	"github.com/spf13/cobra"
	"io"
	"strings"
	"testing"
)

func TestVerifyAndCheckSuccessfulRowsAndLiveCallbacks(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"verify", "check"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies(t)
			deps.Verification = func(_ context.Context, r qualityrun.VerificationRequest) (qualityrun.VerificationResult, error) {
				r.Observer.Started(1)
				r.Observer.Progress(quality.Progress{Repository: "repo", State: quality.ProgressRepositoryCompleted, Status: quality.StatusPassed})
				r.Observer.Finished()
				return qualityrun.VerificationResult{Report: qualityrun.VerificationIndex{Repositories: []quality.VerificationReport{{Repository: "repo", Status: quality.StatusPassed}}}}, nil
			}
			cmd := NewVerify(testRuntime(), deps)
			if kind == "check" {
				cmd = NewCheck(testRuntime(), deps)
			}
			out, _, err := executeTest(t, cmd)
			if err != nil || !strings.Contains(out, "repo") {
				t.Fatalf("output=%s err=%v", out, err)
			}
		})
	}
}
func TestCoverageSummaryFailureStatusWithBoundedRows(t *testing.T) {
	t.Parallel()
	rows := make([]quality.RepositoryCoverage, 500)
	for i := range rows {
		rows[i] = quality.RepositoryCoverage{Repository: "long-repository-name", Status: quality.StatusFailed}
	}
	var out bytes.Buffer
	err := writeCoverageOutputTo(&out, quality.CoverageReport{Repositories: rows}, "summary", qualityrun.CoverageArtifacts{Report: qualityrun.ArtifactReference{Path: "coverage.yaml", SHA256: "digest"}})
	if err != nil || !strings.Contains(out.String(), "WB coverage failed") || out.Len() > 300 || strings.Contains(out.String(), "long-repository-name") {
		t.Fatalf("summary=%s error=%v", &out, err)
	}
}
func TestChangedOutputPropagatesWriterAndFindingsIdentity(t *testing.T) {
	t.Parallel()
	boom := errors.New("write failed")
	deps := testDependencies(t)
	cmd := NewCoverage(testRuntime(), deps)
	cmd.SetArgs([]string{"--changed", "--target", "main", "--format", "json"})
	cmd.SetOut(failingWriter{boom})
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	deps.Changed = func(context.Context, qualityrun.ChangedRequest) (qualityrun.ChangedResult, error) {
		return qualityrun.ChangedResult{HasReport: true, Report: qualityrun.ChangedReport{Packages: []quality.PackageRatchet{{Package: "pkg", Pass: false, NewlyUncoveredChanged: []quality.RatchetFinding{{File: "file.go", Line: 4, Reason: "newly uncovered"}}}}}, Findings: "ratchet sentinel"}, nil
	}
	out, _, err := executeTest(t, NewCoverage(testRuntime(), deps), "--changed", "--target", "main")
	if exitCodeOf(t, err) != exitFindings || err.Error() != "ratchet sentinel" || !strings.Contains(out, "FAIL pkg") || !strings.Contains(out, "file.go:4") {
		t.Fatalf("out=%s err=%v", out, err)
	}
	deps.Changed = func(context.Context, qualityrun.ChangedRequest) (qualityrun.ChangedResult, error) {
		return qualityrun.ChangedResult{HasReport: true}, nil
	}
	_, _, err = executeTest(t, NewCoverage(testRuntime(), deps), "--changed", "--target", "main")
	if err != nil {
		t.Fatal(err)
	}
}
func TestDeadcodeBaselineAnnouncementWriterFailure(t *testing.T) {
	t.Parallel()
	boom := errors.New("writer failed")
	deps := testDependencies(t)
	deps.WriteDeadcodeBaseline = func(string, []quality.DeadcodeFinding) error { return nil }
	cmd := NewDeadcode(testRuntime(), deps)
	cmd.SetArgs([]string{"--update-baseline"})
	cmd.SetOut(failingWriter{boom})
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}
func TestObserverNilSafetyAndZeroTargets(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	cmd.SetErr(io.Discard)
	observer := newObserver(cmd, testRuntime(), "coverage")
	observer.Progress(quality.Progress{})
	observer.Finished()
	observer.Started(0)
	observer.Finished()
}
func TestDeadcodeCleanCommandReturnsSuccess(t *testing.T) {
	t.Parallel()
	_, _, err := executeTest(t, NewDeadcode(testRuntime(), testDependencies(t)))
	if err != nil {
		t.Fatal(err)
	}
}
func TestDeadcodeCommandPropagatesRendererWriteFailure(t *testing.T) {
	t.Parallel()
	boom := errors.New("renderer failed")
	cmd := NewDeadcode(testRuntime(), testDependencies(t))
	cmd.SetOut(failingWriter{boom})
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}
func TestNoRecordsReturnsBoundWriterErrorForBothCommands(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("empty-report stream closed")
	for _, name := range []string{"coverage-ci", "stored-child"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies(t)
			deps.Stored = func(context.Context, qualityrun.StoredRequest) (qualityrun.StoredResult, error) {
				return qualityrun.StoredResult{NoRecords: true}, nil
			}
			cmd := NewStoredCoverage(testRuntime(), deps)
			if name == "coverage-ci" {
				cmd = NewCoverage(testRuntime(), deps)
				cmd.SetArgs([]string{"--ci"})
			}
			cmd.SetOut(failingWriter{sentinel})
			cmd.SetErr(io.Discard)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			if err := cmd.Execute(); err != sentinel {
				t.Fatalf("writer identity=%T %v", err, err)
			}
		})
	}
}
func TestBoundedSummaryKeepsDiagnosticArtifactReference(t *testing.T) {
	t.Parallel()
	rows := make([]quality.RepositoryCoverage, 500)
	for i := range rows {
		rows[i] = quality.RepositoryCoverage{Repository: "very-long-repository", Status: quality.StatusFailed, Diagnostic: &quality.CoverageDiagnostic{Manifest: "private/manifest.json", SHA256: "individual-digest"}}
	}
	refs := qualityrun.CoverageArtifacts{Report: qualityrun.ArtifactReference{Path: "report/coverage.yaml", SHA256: "report-digest"}, Diagnostics: &qualityrun.ArtifactReference{Path: "report/coverage-diagnostics.yaml", SHA256: "index-digest"}}
	var out bytes.Buffer
	if err := writeCoverageOutputTo(&out, quality.CoverageReport{Repositories: rows}, "summary", refs); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"WB coverage failed", "report=report/coverage.yaml; sha256=report-digest", "diagnostics=report/coverage-diagnostics.yaml; diagnostics-sha256=index-digest"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, &out)
		}
	}
	if out.Len() > 400 || strings.Count(out.String(), "\n") != 1 || strings.Contains(out.String(), "private/manifest.json") {
		t.Fatalf("unbounded summary: %s", &out)
	}
}
func TestChangedOnlyFlagRefusalsPreserveUsageCode(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--changed", "--target", "main", "--format", "yaml"}, {"--baseline-timeout", "5m"}, {"--target", "main"}, {"--baseline-file", "baseline.json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			deps := testDependencies(t)
			deps.Coverage = func(context.Context, qualityrun.CoverageRequest) (qualityrun.CoverageResult, error) {
				t.Fatal("rejected flags started coverage")
				return qualityrun.CoverageResult{}, nil
			}
			deps.Changed = func(context.Context, qualityrun.ChangedRequest) (qualityrun.ChangedResult, error) {
				t.Fatal("rejected flags started changed measurement")
				return qualityrun.ChangedResult{}, nil
			}
			_, _, err := executeTest(t, NewCoverage(testRuntime(), deps), args...)
			var coded *testExitError
			if !errors.As(err, &coded) || coded.code != exitUsage {
				t.Fatalf("usage identity=%T %v", err, err)
			}
		})
	}
}
