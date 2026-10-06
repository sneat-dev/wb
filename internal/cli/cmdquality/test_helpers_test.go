package cmdquality

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
	"github.com/spf13/cobra"
	"path/filepath"
	"testing"
)

const (
	exitOK       = 0
	exitFindings = 1
	exitUsage    = 2
)

type testExitError struct {
	code    int
	message string
}

func (e *testExitError) Error() string { return e.message }
func testRuntime() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "projects", NonInteractive: true} }, ExitError: func(code int, message string) error { return &testExitError{code, message} }}
}
func testDependencies(t *testing.T) Dependencies {
	t.Helper()
	return Dependencies{
		Coverage: func(_ context.Context, r qualityrun.CoverageRequest) (qualityrun.CoverageResult, error) {
			if r.Observer.Started != nil {
				r.Observer.Started(1)
			}
			if r.Observer.Progress != nil {
				r.Observer.Progress(quality.Progress{Repository: "acme/app", State: quality.ProgressRepositoryCompleted, Status: quality.StatusPassed})
			}
			if r.Observer.Finished != nil {
				r.Observer.Finished()
			}
			return qualityrun.CoverageResult{Report: quality.CoverageReport{Percentage: 80, Statements: 10, Covered: 8}}, nil
		},
		Verification: func(_ context.Context, r qualityrun.VerificationRequest) (qualityrun.VerificationResult, error) {
			return qualityrun.VerificationResult{Report: qualityrun.VerificationIndex{SchemaVersion: 1, Profile: r.Profile, Checks: r.Checks}}, nil
		},
		Changed: func(context.Context, qualityrun.ChangedRequest) (qualityrun.ChangedResult, error) {
			return qualityrun.ChangedResult{HasReport: true, Report: qualityrun.ChangedReport{Target: "main", MergeBase: "base"}}, nil
		},
		Stored: func(context.Context, qualityrun.StoredRequest) (qualityrun.StoredResult, error) {
			return qualityrun.StoredResult{Report: quality.CoverageReport{Percentage: 80}}, nil
		},
		Baseline: func(context.Context, qualityrun.BaselineRequest) error { return nil }, Summary: func(context.Context, qualityrun.SummaryRequest) error { return nil },
		Worklist: func(context.Context, qualityrun.WorklistRequest) (quality.Worklist, error) {
			return quality.Worklist{}, nil
		},
		Analyze: func(context.Context, string, quality.DeadcodeOptions) (quality.DeadcodeReport, error) {
			return quality.DeadcodeReport{}, nil
		},
		WriteDeadcodeBaseline: func(string, []quality.DeadcodeFinding) error { return nil }, Abs: filepath.Abs, WorkflowAnnotations: func() bool { return false },
	}
}
func executeTest(t *testing.T, cmd *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), stderr.String(), err
}
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return exitOK
	}
	var coded *testExitError
	if errors.As(err, &coded) {
		return coded.code
	}
	return exitFindings
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
