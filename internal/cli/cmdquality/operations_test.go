package cmdquality

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
	"github.com/spf13/cobra"
	"io"
	"strings"
	"testing"
	"time"
)

func TestCoverageTranslatesParsedOptionsWithLazyInheritedFlags(t *testing.T) {
	t.Parallel()
	deps := testDependencies(t)
	var captured qualityrun.CoverageRequest
	deps.Coverage = func(ctx context.Context, r qualityrun.CoverageRequest) (qualityrun.CoverageResult, error) {
		if ctx != context.Background() {
			t.Fatal("ordinary context behavior changed")
		}
		captured = r
		return qualityrun.CoverageResult{}, nil
	}
	var projects, filter string
	reads := 0
	runtime := testRuntime()
	runtime.Flags = func() shared.Flags {
		reads++
		return shared.Flags{ProjectsRoot: projects, Filter: filter, NonInteractive: true}
	}
	root := &cobra.Command{Use: "wb"}
	root.PersistentFlags().StringVar(&projects, "projects-root", "before", "root")
	root.PersistentFlags().StringVar(&filter, "filter", "before", "filter")
	root.AddCommand(NewCoverage(runtime, deps))
	if reads != 0 {
		t.Fatal("flags read at registration")
	}
	_, _, err := executeTest(t, root, "coverage", "--fleet", "--projects-root", "parsed", "--filter", "acme", "--parallel", "3", "--retry", "2", "--timeout", "11s", "--match", "acme/*", "--regex", "core$", "--include-e2e", "--report-dir", "reports")
	if err != nil {
		t.Fatal(err)
	}
	if captured.Selection.ProjectsRoot != "parsed" || captured.Selection.Filter != "acme" || !captured.Selection.Fleet || captured.Selection.Parallel != 3 || captured.Selection.Retry != 2 || captured.Selection.Timeout != 11*time.Second || captured.Selection.Match != "acme/*" || captured.Selection.Regex != "core$" || !captured.Run.IncludeE2E || captured.Run.CoverageDiagnosticsDir != "reports" {
		t.Fatalf("request=%+v run=%+v", captured, captured.Run)
	}
}
func TestCoverageExplicitPackagesAndShardingAreCopied(t *testing.T) {
	t.Parallel()
	deps := testDependencies(t)
	deps.Coverage = func(_ context.Context, r qualityrun.CoverageRequest) (qualityrun.CoverageResult, error) {
		if r.Run.GoTestShards != 4 || strings.Join(r.Run.GoShardPackages, ",") != "./internal/worktrees" || strings.Join(r.Run.GoTestPackages, ",") != "./internal/worktrees" || !r.Run.ExplicitGoTestSharding || r.Run.CoverageProfile != "exact.cov" {
			t.Fatalf("run=%+v", r.Run)
		}
		return qualityrun.CoverageResult{}, nil
	}
	_, _, err := executeTest(t, NewCoverage(testRuntime(), deps), "repo", "--package", "./internal/worktrees", "--test-shards", "4", "--shard-package", "./internal/worktrees", "--coverage-profile", "exact.cov")
	if err != nil {
		t.Fatal(err)
	}
}
func TestCoverageFormatsAndLateAdmissionUseActualArtifactReferences(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"markdown", "yaml", "json", "summary", "unknown"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies(t)
			calls := 0
			deps.Coverage = func(_ context.Context, r qualityrun.CoverageRequest) (qualityrun.CoverageResult, error) {
				calls++
				if r.ReportDir != "reports" {
					t.Fatalf("report dir=%s", r.ReportDir)
				}
				return qualityrun.CoverageResult{Report: quality.CoverageReport{Percentage: 80, Statements: 10, Covered: 8}, Artifacts: qualityrun.CoverageArtifacts{Report: qualityrun.ArtifactReference{Path: "reports/coverage.yaml", SHA256: "actual-digest"}}}, nil
			}
			out, _, err := executeTest(t, NewCoverage(testRuntime(), deps), "--format", format, "--report-dir", "reports")
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			if format == "unknown" {
				if err == nil || !strings.Contains(err.Error(), "unknown --format") {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if format == "summary" && out != "WB coverage passed: 80.00% (8/10 statements); report=reports/coverage.yaml; sha256=actual-digest\n" {
				t.Fatalf("summary=%q", out)
			}
		})
	}
}
func TestCoverageNoWorkOperationFailureAndFindings(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("read resume failed")
	for _, tc := range []struct {
		name     string
		result   qualityrun.CoverageResult
		err      error
		args     []string
		wantCode int
		want     string
	}{{name: "no work", result: qualityrun.CoverageResult{NoWork: true}, args: []string{"--resume", "--report-dir", "reports", "--format", "bad"}, want: "no failed repositories to resume; nothing to do"}, {name: "failure", err: sentinel}, {name: "failed report", result: qualityrun.CoverageResult{Report: quality.CoverageReport{Repositories: []quality.RepositoryCoverage{{Status: quality.StatusFailed}}}}, wantCode: 1, want: "could not be measured"}, {name: "minimum", result: qualityrun.CoverageResult{Report: quality.CoverageReport{Percentage: 80}}, args: []string{"--minimum", "90"}, wantCode: 1, want: "is below required 90.00%"}, {name: "summary missing reference", args: []string{"--format", "summary"}, want: "requires --report-dir"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies(t)
			deps.Coverage = func(context.Context, qualityrun.CoverageRequest) (qualityrun.CoverageResult, error) {
				return tc.result, tc.err
			}
			out, stderr, err := executeTest(t, NewCoverage(testRuntime(), deps), tc.args...)
			if tc.err != nil {
				if err != tc.err || out != "" {
					t.Fatalf("error=%v out=%q", err, out)
				}
				return
			}
			if tc.result.NoWork {
				if err != nil || out != "" || !strings.Contains(stderr, tc.want) {
					t.Fatalf("noWork error=%v out=%q stderr=%q", err, out, stderr)
				}
				return
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error=%v want=%s", err, tc.want)
			}
			if tc.wantCode != 0 {
				var code *testExitError
				if !errors.As(err, &code) || code.code != tc.wantCode {
					t.Fatalf("identity=%v", err)
				}
			}
		})
	}
}
func TestVerifyAndCheckTranslateProfilesAndHandleResults(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"verify", "check"} {
		for _, format := range []string{"markdown", "yaml", "json", "bad"} {
			t.Run(name+format, func(t *testing.T) {
				t.Parallel()
				deps := testDependencies(t)
				calls := 0
				deps.Verification = func(ctx context.Context, r qualityrun.VerificationRequest) (qualityrun.VerificationResult, error) {
					calls++
					if ctx != context.Background() || r.Name != name || r.Selection.Path != "repo" || r.ReportDir != "reports" {
						t.Fatalf("request=%+v", r)
					}
					if name == "verify" && len(r.Checks) != 1 {
						t.Fatal("explicit checks lost")
					}
					if name == "check" && (r.Profile != "full" || len(r.Checks) != 3) {
						t.Fatal("profile lost")
					}
					return qualityrun.VerificationResult{Report: qualityrun.VerificationIndex{SchemaVersion: 1, Profile: r.Profile, Checks: r.Checks, Repositories: []quality.VerificationReport{{Repository: "acme/app", Status: quality.StatusFailed, Results: []quality.VerificationEntry{{Language: "go", Check: quality.CheckTest, Status: quality.StatusFailed, Command: "go test", Detail: "boom"}}}}}}, nil
				}
				var cmd *cobra.Command
				args := []string{"repo", "--format", format, "--report-dir", "reports"}
				if name == "verify" {
					cmd = NewVerify(testRuntime(), deps)
					args = append(args, "--checks", "test")
				} else {
					cmd = NewCheck(testRuntime(), deps)
					args = append(args, "--profile", "full")
				}
				out, _, err := executeTest(t, cmd, args...)
				if calls != 1 || err == nil {
					t.Fatalf("calls=%d error=%v", calls, err)
				}
				if format == "bad" {
					if !strings.Contains(err.Error(), "unknown --format") {
						t.Fatal(err)
					}
				} else {
					var code *testExitError
					if !errors.As(err, &code) || code.code != 1 || out == "" {
						t.Fatalf("out=%q error=%v", out, err)
					}
				}
			})
		}
	}
}
func TestVerifyAndCheckNoWorkAndOperationErrors(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"verify", "check"} {
		for _, noWork := range []bool{false, true} {
			t.Run(fmt.Sprint(name, noWork), func(t *testing.T) {
				t.Parallel()
				sentinel := errors.New("verification unavailable")
				deps := testDependencies(t)
				deps.Verification = func(context.Context, qualityrun.VerificationRequest) (qualityrun.VerificationResult, error) {
					if noWork {
						return qualityrun.VerificationResult{NoWork: true}, nil
					}
					return qualityrun.VerificationResult{}, sentinel
				}
				cmd := NewVerify(testRuntime(), deps)
				args := []string{"--resume", "--report-dir", "reports"}
				if name == "check" {
					cmd = NewCheck(testRuntime(), deps)
					args = append(args, "--profile", "fast")
				}
				out, stderr, err := executeTest(t, cmd, args...)
				if noWork {
					if err != nil || out != "" || stderr != "no failed repositories to resume; nothing to do\n" {
						t.Fatalf("err=%v out=%q stderr=%q", err, out, stderr)
					}
				} else if err != sentinel || out != "" {
					t.Fatalf("error=%v out=%q", err, out)
				}
			})
		}
	}
}
func TestCommandsRefuseAmbiguousArgumentsBeforeOperations(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"coverage", "verify", "check"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies(t)
			deps.Coverage = func(context.Context, qualityrun.CoverageRequest) (qualityrun.CoverageResult, error) {
				t.Fatal("coverage ran")
				return qualityrun.CoverageResult{}, nil
			}
			deps.Verification = func(context.Context, qualityrun.VerificationRequest) (qualityrun.VerificationResult, error) {
				t.Fatal("verification ran")
				return qualityrun.VerificationResult{}, nil
			}
			cmd := NewCoverage(testRuntime(), deps)
			if name == "verify" {
				cmd = NewVerify(testRuntime(), deps)
			}
			if name == "check" {
				cmd = NewCheck(testRuntime(), deps)
			}
			_, _, err := executeTest(t, cmd, "repo", "--fleet")
			if err == nil || !strings.Contains(err.Error(), "cannot be used with --fleet") {
				t.Fatal(err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		args []string
	}{{"verify", []string{"--checks", "lint,bogus"}}, {"check", []string{"--profile", "unknown"}}, {"coverage", []string{"--baseline-timeout", "1s"}}, {"coverage", []string{"one", "two"}}} {
		cmd := NewCoverage(testRuntime(), testDependencies(t))
		if tc.name == "verify" {
			cmd = NewVerify(testRuntime(), testDependencies(t))
		}
		if tc.name == "check" {
			cmd = NewCheck(testRuntime(), testDependencies(t))
		}
		if _, _, err := executeTest(t, cmd, tc.args...); err == nil {
			t.Fatalf("accepted %s %v", tc.name, tc.args)
		}
	}
}
func TestStoredCoverageCommandsTranslateRequestsAndPreserveEmptyOrder(t *testing.T) {
	t.Parallel()
	for _, fleetChild := range []bool{false, true} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprint(fleetChild, empty), func(t *testing.T) {
				t.Parallel()
				deps := testDependencies(t)
				deps.Stored = func(ctx context.Context, r qualityrun.StoredRequest) (qualityrun.StoredResult, error) {
					if ctx.Value(contextKey{}) != "context" || r.Fleet != fleetChild || r.Match != "acme/*" || r.Regex != "core$" {
						t.Fatalf("request=%+v context=%v", r, ctx)
					}
					return qualityrun.StoredResult{NoRecords: empty, Report: quality.CoverageReport{Percentage: 80}}, nil
				}
				cmd := NewCoverage(testRuntime(), deps)
				args := []string{"repo", "--ci", "--match", "acme/*", "--regex", "core$", "--format", "bad"}
				if fleetChild {
					cmd = NewStoredCoverage(testRuntime(), deps)
					args = []string{"--match", "acme/*", "--regex", "core$", "--format", "bad"}
				}
				cmd.SetContext(context.WithValue(context.Background(), contextKey{}, "context"))
				out, _, err := executeTest(t, cmd, args...)
				if empty {
					if err != nil || out != "no CI coverage reports collected yet\n" {
						t.Fatalf("empty error=%v out=%q", err, out)
					}
				} else if err == nil || !strings.Contains(err.Error(), "unknown --format") {
					t.Fatal(err)
				}
			})
		}
	}
}

type contextKey struct{}

func TestStoredFailuresAndMinimumFindingsPassThrough(t *testing.T) {
	t.Parallel()
	for _, fails := range []bool{false, true} {
		deps := testDependencies(t)
		sentinel := errors.New("store failed")
		deps.Stored = func(context.Context, qualityrun.StoredRequest) (qualityrun.StoredResult, error) {
			if fails {
				return qualityrun.StoredResult{}, sentinel
			}
			return qualityrun.StoredResult{Report: quality.CoverageReport{Percentage: 80}}, nil
		}
		_, _, err := executeTest(t, NewCoverage(testRuntime(), deps), "repo", "--ci", "--minimum", "90")
		if fails {
			if err != sentinel {
				t.Fatal(err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "is below required 90.00%") {
			t.Fatal(err)
		}
	}
}
func TestChangedCoverageWarningsPrecedePersistenceFailure(t *testing.T) {
	t.Parallel()
	deps := testDependencies(t)
	deps.WorkflowAnnotations = func() bool { return true }
	sentinel := errors.New("write report denied")
	deps.Changed = func(ctx context.Context, r qualityrun.ChangedRequest) (qualityrun.ChangedResult, error) {
		if ctx.Value(contextKey{}) != "context" || r.Path != "repo" || r.Target != "main" || !r.AffectedPackages || !r.Run.IncludeE2E || r.BaselineFile != "baseline.json" || r.BaselineTimeout != 3*time.Minute || r.ReportDir != "reports" {
			t.Fatalf("request=%+v", r)
		}
		r.BeforePersist(qualityrun.ChangedReport{RedBase: &quality.RedBaseline{SHA: "base", FailedTests: []string{"TestOne"}}, Packages: toleratedPackageResults()})
		return qualityrun.ChangedResult{}, sentinel
	}
	cmd := NewCoverage(testRuntime(), deps)
	cmd.SetContext(context.WithValue(context.Background(), contextKey{}, "context"))
	out, stderr, err := executeTest(t, cmd, "repo", "--changed", "--target", "main", "--affected-packages", "--include-e2e", "--baseline-file", "baseline.json", "--baseline-timeout", "3m", "--report-dir", "reports")
	if err != sentinel || out != "" || !strings.Contains(stderr, "::warning") || !strings.Contains(stderr, "RED merge base") || !strings.Contains(stderr, "tolerance used") {
		t.Fatalf("error=%v out=%q stderr=%q", err, out, stderr)
	}
}
func TestChangedCoverageReportlessMeasurementAndReportedFindings(t *testing.T) {
	t.Parallel()
	for _, hasReport := range []bool{false, true} {
		t.Run(fmt.Sprint(hasReport), func(t *testing.T) {
			t.Parallel()
			deps := testDependencies(t)
			deps.Changed = func(context.Context, qualityrun.ChangedRequest) (qualityrun.ChangedResult, error) {
				return qualityrun.ChangedResult{HasReport: hasReport, Report: qualityrun.ChangedReport{Target: "main"}, Findings: "coverage could not be measured (diagnostic manifest manifest.json)"}, nil
			}
			out, _, err := executeTest(t, NewCoverage(testRuntime(), deps), "--changed", "--target", "main")
			var code *testExitError
			if !errors.As(err, &code) || code.code != 1 || !strings.Contains(err.Error(), "manifest.json") || (out != "") != hasReport {
				t.Fatalf("error=%v out=%q", err, out)
			}
		})
	}
}
func TestArtifactsTranslateAllMetadataAndErrorsWithoutFixtures(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("profile unavailable")
	for _, name := range []string{"baseline", "summary", "worklist"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies(t)
			calls := 0
			args := []string{name, "profile.cov", "--module", "module"}
			switch name {
			case "baseline":
				args = append(args, "--sha", "sha", "--out", "baseline.json", "--include-e2e")
				deps.Baseline = func(ctx context.Context, r qualityrun.BaselineRequest) error {
					calls++
					if r.Profile != "profile.cov" || r.Module != "module" || r.SHA != "sha" || r.Out != "baseline.json" || !r.IncludeE2E {
						t.Fatalf("request=%+v", r)
					}
					return sentinel
				}
			case "summary":
				args = append(args, "--repo", "acme/app", "--sha", "sha", "--ref", "ref", "--workflow-run-id", "123", "--workflow-run-url", "url", "--out", "summary.json")
				deps.Summary = func(ctx context.Context, r qualityrun.SummaryRequest) error {
					calls++
					if r.Profile != "profile.cov" || r.Module != "module" || r.Out != "summary.json" || r.Meta.Repository != "acme/app" || r.Meta.SHA != "sha" || r.Meta.Ref != "ref" || r.Meta.WorkflowRunID != 123 || r.Meta.WorkflowRunURL != "url" {
						t.Fatalf("request=%+v", r)
					}
					return sentinel
				}
			case "worklist":
				args = append(args, "--unit-size", "12")
				deps.Worklist = func(ctx context.Context, r qualityrun.WorklistRequest) (quality.Worklist, error) {
					calls++
					if r.Profile != "profile.cov" || r.Module != "module" || r.UnitSize != 12 {
						t.Fatalf("request=%+v", r)
					}
					return quality.Worklist{}, sentinel
				}
			}
			_, _, err := executeTest(t, NewCoverage(testRuntime(), deps), args...)
			if err != sentinel || calls != 1 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
	deps := testDependencies(t)
	deps.Worklist = func(context.Context, qualityrun.WorklistRequest) (quality.Worklist, error) {
		t.Fatal("invalid format called Worklist")
		return quality.Worklist{}, nil
	}
	_, _, err := executeTest(t, NewCoverage(testRuntime(), deps), "worklist", "profile", "--format", "yaml")
	var code *testExitError
	if !errors.As(err, &code) || code.code != 2 {
		t.Fatal(err)
	}
}
func TestOutputWritersFailBeforeFindings(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("output closed")
	for _, format := range []string{"markdown", "yaml", "json", "summary"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			artifacts := qualityrun.CoverageArtifacts{Report: qualityrun.ArtifactReference{Path: "coverage.yaml", SHA256: "digest"}}
			if err := writeCoverageOutputTo(failingWriter{sentinel}, quality.CoverageReport{}, format, artifacts); err != sentinel {
				t.Fatal(err)
			}
			if format != "summary" {
				if err := writeVerificationOutput(failingWriter{sentinel}, qualityrun.VerificationIndex{}, format); err != sentinel {
					t.Fatal(err)
				}
			}
		})
	}
	deps := testDependencies(t)
	deps.Coverage = func(context.Context, qualityrun.CoverageRequest) (qualityrun.CoverageResult, error) {
		return qualityrun.CoverageResult{NoWork: true}, nil
	}
	cmd := NewCoverage(testRuntime(), deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(failingWriter{sentinel})
	cmd.SetArgs([]string{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.Execute(); err != sentinel {
		t.Fatal(err)
	}
	for _, name := range []string{"verify", "check"} {
		deps.Verification = func(context.Context, qualityrun.VerificationRequest) (qualityrun.VerificationResult, error) {
			return qualityrun.VerificationResult{NoWork: true}, nil
		}
		cmd = NewVerify(testRuntime(), deps)
		if name == "check" {
			cmd = NewCheck(testRuntime(), deps)
		}
		cmd.SetOut(io.Discard)
		cmd.SetErr(failingWriter{sentinel})
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		cmd.SetArgs(nil)
		if err := cmd.Execute(); err != sentinel {
			t.Fatal(err)
		}
	}
}
func TestArtifactSuccessfulFormatsAndWriters(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "json"} {
		deps := testDependencies(t)
		deps.Worklist = func(_ context.Context, request qualityrun.WorklistRequest) (quality.Worklist, error) {
			if request.UnitSize != 300 {
				t.Fatalf("default unit size=%d", request.UnitSize)
			}
			return quality.Worklist{UnitSize: 300}, nil
		}
		out, _, err := executeTest(t, NewCoverage(testRuntime(), deps), "worklist", "profile", "--format", format)
		if err != nil || out == "" {
			t.Fatalf("out=%q error=%v", out, err)
		}
		sentinel := errors.New("broken pipe")
		if err := writeWorklistOutput(failingWriter{sentinel}, quality.Worklist{}, format); err != sentinel {
			t.Fatal(err)
		}
	}
}
func TestDeadcodeAbsAndBaselineFailuresPreserveIdentity(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("operation failed")
	deps := testDependencies(t)
	deps.Abs = func(string) (string, error) { return "", sentinel }
	_, _, err := executeTest(t, NewDeadcode(testRuntime(), deps), "repo")
	var code *testExitError
	if !errors.As(err, &code) || code.code != 2 || !strings.Contains(err.Error(), "resolve repository path") {
		t.Fatal(err)
	}
	deps = testDependencies(t)
	deps.WriteDeadcodeBaseline = func(string, []quality.DeadcodeFinding) error { return sentinel }
	if _, _, err := executeTest(t, NewDeadcode(testRuntime(), deps), "repo", "--update-baseline"); err != sentinel {
		t.Fatal(err)
	}
}
func TestVerificationRenderIncludesEmptyRowsAndProfile(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	report := qualityrun.VerificationIndex{Profile: "ci", Checks: []quality.Check{quality.CheckSpec}, Repositories: []quality.VerificationReport{{Repository: "empty", Status: quality.StatusSkipped}}}
	if err := writeVerificationOutput(&out, report, "markdown"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "empty") || !strings.Contains(out.String(), "Profile: `ci`") {
		t.Fatal(out.String())
	}
}
