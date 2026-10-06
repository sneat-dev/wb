package cmdci

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/ciaudit"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/githubchecks"
	progresspkg "github.com/sneat-dev/wb/internal/progress"
	"github.com/spf13/cobra"
)

const testHead = "0123456789012345678901234567890123456789"

func runtime(root func() string) shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root(), Filter: "app", NonInteractive: true} }, ExitError: func(code int, message string) error { return &codedError{code, message} }}
}

type codedError struct {
	code    int
	message string
}

func (e *codedError) Error() string { return e.message }
func dependencies() Dependencies {
	return Dependencies{WaitChecks: func(context.Context, githubchecks.PullRequestWaitOptions) (githubchecks.PullRequestWaitResult, error) {
		return githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPassed}, nil
	}, Audit: func(ciaudit.BatchOptions) ([]ciaudit.Report, error) { return []ciaudit.Report{}, nil }, ValidateBranch: func(string) error { return nil }, Now: func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.FixedZone("test", 3600)) }}
}
func execute(cmd *cobra.Command, args ...string) (string, string, error) {
	var out, errout bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errout)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errout.String(), err
}
func waitArgs() []string { return []string{"--repo=acme/app", "--target=main", "--head=" + testHead} }
func TestWaitRequestAndAliasesUseOneOperation(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{"wait", "checks", "ci"} {
		t.Run(spelling, func(t *testing.T) {
			t.Parallel()
			deps := dependencies()
			calls := 0
			ctx := context.Background()
			deps.WaitChecks = func(got context.Context, o githubchecks.PullRequestWaitOptions) (githubchecks.PullRequestWaitResult, error) {
				calls++
				if got != ctx || o.Repository != "acme/app" || o.PullRequest != "17" || o.Head != testHead || o.Target != "main" || o.Slice != shared.DefaultCIWaitSlice || o.CheckPollInterval != githubchecks.DefaultCheckPollInterval {
					t.Fatalf("request=%+v", o)
				}
				o.Progress(githubchecks.PullRequestWaitProgress{Observation: 1})
				o.OperationProgress(progresspkg.Event{Detail: "callback wired"})
				return githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPassed, Repository: o.Repository, Target: o.Target, Head: o.Head}, nil
			}
			var cmd *cobra.Command
			args := append(waitArgs(), "--pr=17", "--format=json")
			if spelling == "wait" {
				cmd = New(runtime(func() string { return "root" }), deps)
				args = append([]string{"wait"}, args...)
			} else {
				cmd = &cobra.Command{Use: "wait"}
				cmd.AddCommand(NewChecks(runtime(func() string { return "root" }), deps))
				args = append([]string{spelling}, args...)
			}
			cmd.SetContext(ctx)
			out, stderr, err := execute(cmd, args...)
			if err != nil || calls != 1 || !strings.Contains(out, `"observed_at": "2026-10-03T11:00:00Z"`) || !strings.Contains(stderr, "callback wired") {
				t.Fatalf("calls=%d out=%s stderr=%s err=%v", calls, out, stderr, err)
			}
		})
	}
}
func TestWaitValidationPreventsOperationCalls(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		args        []string
		branchError bool
		want        string
	}{{"extra", []string{"extra"}, false, "unknown command"}, {"repository", []string{"--repo=bad", "--target=main", "--head=" + testHead}, false, "owner/repository"}, {"blank target", []string{"--repo=acme/app", "--head=" + testHead}, false, "--target is required"}, {"branch", waitArgs(), true, "branch refused"}, {"head", []string{"--repo=acme/app", "--target=main", "--head=bad"}, false, "exact 40- or 64-hex"}, {"slice", append(waitArgs(), "--slice=10m"), false, "--slice must"}, {"interval", append(waitArgs(), "--interval=0s"), false, "--interval must be positive"}, {"stable", append(waitArgs(), "--slice=1s", "--interval=1s"), false, "shorter than"}, {"format", append(waitArgs(), "--format=toml"), false, "unsupported format"}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			deps := dependencies()
			calls := 0
			deps.WaitChecks = func(context.Context, githubchecks.PullRequestWaitOptions) (githubchecks.PullRequestWaitResult, error) {
				calls++
				return githubchecks.PullRequestWaitResult{}, nil
			}
			if test.branchError {
				deps.ValidateBranch = func(string) error { return errors.New("branch refused") }
			}
			_, _, err := execute(newWaitCmd(runtime(func() string { return "root" }), deps), test.args...)
			if err == nil || calls != 0 || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}
func TestWaitOutcomesPreserveTypedExitAndResume(t *testing.T) {
	t.Parallel()
	for _, status := range []githubchecks.PullRequestWaitStatus{githubchecks.PullRequestWaitPassed, githubchecks.PullRequestWaitPending, githubchecks.PullRequestWaitFailed, githubchecks.PullRequestWaitStatus("rejected")} {
		for _, jsonOut := range []bool{false, true} {
			t.Run(string(status)+map[bool]string{true: "/json", false: "/text"}[jsonOut], func(t *testing.T) {
				t.Parallel()
				deps := dependencies()
				deps.WaitChecks = func(context.Context, githubchecks.PullRequestWaitOptions) (githubchecks.PullRequestWaitResult, error) {
					return githubchecks.PullRequestWaitResult{Status: status, Repository: "acme/app", Head: testHead, Target: "main", Reason: "result reason"}, nil
				}
				args := append(waitArgs(), "--slice=2m", "--interval=1s")
				if jsonOut {
					args = append(args, "--json")
				}
				out, _, err := execute(newWaitCmd(runtime(func() string { return "root" }), deps), args...)
				if status == githubchecks.PullRequestWaitPassed {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					var code *codedError
					if !errors.As(err, &code) || code.code != 1 || code.message != "CI wait "+string(status)+": result reason" {
						t.Fatalf("error=%v", err)
					}
				}
				if status == githubchecks.PullRequestWaitPending && !strings.Contains(out, "wb") {
					t.Fatalf("no resume: %s", out)
				}
				if !strings.Contains(out, "result reason") {
					t.Fatal(out)
				}
			})
		}
	}
}

type refusedWriter struct{}

func (refusedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestWaitPropagatesOperationAndOutputErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("operation refused")
	deps := dependencies()
	deps.WaitChecks = func(context.Context, githubchecks.PullRequestWaitOptions) (githubchecks.PullRequestWaitResult, error) {
		return githubchecks.PullRequestWaitResult{}, boom
	}
	_, stderr, err := execute(newWaitCmd(runtime(func() string { return "root" }), deps), waitArgs()...)
	if !errors.Is(err, boom) || !strings.Contains(stderr, "operation refused") {
		t.Fatalf("error=%v stderr=%s", err, stderr)
	}
	for _, format := range []string{"text", "json"} {
		cmd := newWaitCmd(runtime(func() string { return "root" }), dependencies())
		cmd.SetOut(refusedWriter{})
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append(waitArgs(), "--format="+format))
		if err := cmd.Execute(); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("%s error=%v", format, err)
		}
	}
	deps = dependencies()
	deps.Now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
	if _, _, err := execute(newWaitCmd(runtime(func() string { return "root" }), deps), append(waitArgs(), "--json")...); err == nil {
		t.Fatal("invalid JSON timestamp accepted")
	}
}
func TestAuditMapsFlagsAndStrictFindingsToLocalOutput(t *testing.T) {
	t.Parallel()
	for _, strict := range []bool{false, true} {
		for _, jsonOut := range []bool{false, true} {
			t.Run(map[bool]string{true: "strict", false: "report"}[strict]+map[bool]string{true: "/json", false: "/text"}[jsonOut], func(t *testing.T) {
				t.Parallel()
				deps := dependencies()
				root := "before"
				deps.Audit = func(o ciaudit.BatchOptions) ([]ciaudit.Report, error) {
					want := ciaudit.BatchOptions{Path: "explicit", ProjectsRoot: "after", Filter: "app", Target: "main", Fleet: true}
					if !reflect.DeepEqual(o, want) {
						t.Fatalf("options=%+v", o)
					}
					return []ciaudit.Report{{Path: "/acme/app", HasGo: true, Findings: []ciaudit.Finding{{Code: "go-coverage-threshold", Message: "missing"}}}}, nil
				}
				cmd := New(runtime(func() string { return root }), deps)
				cmd.PersistentFlags().StringVar(&root, "projects-root", root, "")
				args := []string{"audit", "explicit", "--fleet", "--target=main", "--projects-root=after"}
				if strict {
					args = append(args, "--strict")
				}
				if jsonOut {
					args = append(args, "--json")
				}
				out, _, err := execute(cmd, args...)
				if strict {
					var code *codedError
					if !errors.As(err, &code) || code.code != 1 {
						t.Fatalf("error=%v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out, "/acme/app") || !strings.Contains(out, "go-coverage-threshold") {
					t.Fatal(out)
				}
			})
		}
	}
}
func TestAuditDefaultsAndOperationErrors(t *testing.T) {
	t.Parallel()
	deps := dependencies()
	calls := 0
	deps.Audit = func(o ciaudit.BatchOptions) ([]ciaudit.Report, error) {
		calls++
		if o.Path != "." || o.Fleet || o.Target != "" {
			t.Fatalf("options=%+v", o)
		}
		return []ciaudit.Report{}, nil
	}
	out, _, err := execute(New(runtime(func() string { return "root" }), deps), "audit", "--strict", "--json")
	if err != nil || calls != 1 || out != "[]\n" {
		t.Fatalf("out=%q calls=%d err=%v", out, calls, err)
	}
	boom := errors.New("audit refused")
	deps.Audit = func(ciaudit.BatchOptions) ([]ciaudit.Report, error) { return nil, boom }
	if _, _, err := execute(New(runtime(func() string { return "root" }), deps), "audit"); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
}
func TestAuditAndResumeOutputFailures(t *testing.T) {
	t.Parallel()
	reports := []ciaudit.Report{{Path: "empty"}, {Path: "full", HasGo: true, GoCoverageThreshold: true, HasFrontend: true, FrontendCoverageThreshold: true, HasDeploy: true, ArtifactPromotion: true, Findings: []ciaudit.Finding{{Code: "one", File: "file"}, {Code: "two"}}}}
	for call := 1; call <= 8; call++ {
		if err := printCIAudit(&failAfterNWriter{failAt: call}, reports); err == nil {
			t.Fatalf("write%d error=nil", call)
		}
	}
	for _, format := range []string{"text", "json"} {
		deps := dependencies()
		deps.Audit = func(ciaudit.BatchOptions) ([]ciaudit.Report, error) { return []ciaudit.Report{{Path: "repo"}}, nil }
		cmd := New(runtime(func() string { return "root" }), deps)
		cmd.SetOut(refusedWriter{})
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"audit", "--format=" + format})
		if err := cmd.Execute(); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("%s=%v", format, err)
		}
	}
	cmd := &cobra.Command{}
	cmd.SetOut(&failAfterNWriter{failAt: 2})
	if err := printCIWait(cmd, WaitOutput{ResumeArgs: []string{"wb"}}); err == nil {
		t.Fatal("resume write failure lost")
	}
}
func TestPendingResumeKeepsExactIdentityAndFactoryError(t *testing.T) {
	t.Parallel()
	deps := dependencies()
	deps.WaitChecks = func(ctx context.Context, o githubchecks.PullRequestWaitOptions) (githubchecks.PullRequestWaitResult, error) {
		if o.Head != strings.Repeat("a", 40) || o.PullRequest != "17" {
			t.Fatalf("options=%+v", o)
		}
		return githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPending, Reason: "continue"}, nil
	}
	r := runtime(func() string { return "root" })
	want := errors.New("factory result")
	r.ExitError = func(code int, message string) error {
		if code != 1 || message != "CI wait pending: continue" {
			t.Fatalf("code=%d message=%s", code, message)
		}
		return want
	}
	out, _, err := execute(newWaitCmd(r, deps), "--repo=acme/app", "--pr=17", "--target=main", "--head="+strings.Repeat("A", 40), "--slice=2m", "--interval=1s", "--json")
	if err != want || !strings.Contains(out, `"--pr"`) || !strings.Contains(out, `"17"`) {
		t.Fatalf("out=%s error=%v", out, err)
	}
}
func TestAuditRejectsExtraArgumentsWithoutOperations(t *testing.T) {
	t.Parallel()
	deps := dependencies()
	calls := 0
	deps.Audit = func(ciaudit.BatchOptions) ([]ciaudit.Report, error) { calls++; return nil, nil }
	if _, _, err := execute(New(runtime(func() string { return "root" }), deps), "audit", "one", "two"); err == nil || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
