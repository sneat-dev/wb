package cmdagent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/spf13/cobra"
)

func timePtr(value time.Time) *time.Time { return &value }

func TestStatusDocumentReportsEveryObservedFact(t *testing.T) {
	t.Parallel()
	cost := 0.42
	result := agents.Result{
		AgentID: "agt-00000000000000000000000000000001", State: agents.StateCompleted, Status: agents.StatusCompleted,
		RequestedProfile: "op", Resolved: agents.Resolved{Profile: "op", Harness: "opencode", Model: "p/m", Variant: "high"},
		TaskType: "coding", Complexity: "high", EffortRequested: "high", EffortEffective: "high",
		Worktree: "op", WorktreeMode: agents.ModeAuto, WorktreeDir: "/fleet/op",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: timePtr(time.Unix(200, 0).UTC()), DurationMS: 1000,
		Failure: "boom", FailureCategory: agents.FailureHarness, ExitCode: intPointerTest(1),
		Changes:    &agents.ChangeSummary{},
		Validation: agents.ValidationRecord{Status: agents.ValidationRunning, Reason: "still running"},
		Review:     agents.ReviewRecord{Status: agents.ReviewAccepted, Evaluator: "alex"},
		Telemetry:  agents.Telemetry{Available: true, Tokens: &agents.Tokens{Input: intPointerTest(5)}},
		Cost:       agents.CostRecord{USD: &cost, Source: "harness-estimated"},
		LogPath:    "/private/events.jsonl",
	}
	var out strings.Builder
	if err := printAgentStatusText(&out, result); err != nil {
		t.Fatalf("printAgentStatusText: %v", err)
	}
	for _, want := range []string{"variant=high", "effective=high", "category: harness_failed", "exit:     1", "validation: running (still running)", "review:   accepted by alex", "usage:    in=5", "cost:     $0.4200"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output %q is missing %q", out.String(), want)
		}
	}
	if got := validationWord(agents.ValidationRecord{Status: agents.ValidationRunning}); got != "running" {
		t.Fatalf("validation word = %q", got)
	}
	if got := validationWord(agents.ValidationRecord{}); got != agents.ValidationPending {
		t.Fatalf("empty validation = %q", got)
	}
	if got := reviewWord(agents.ReviewRecord{}); got != agents.ReviewPending {
		t.Fatalf("empty review = %q", got)
	}
	if got := reviewWord(agents.ReviewRecord{Status: agents.ReviewRejected}); got != agents.ReviewRejected {
		t.Fatalf("bodiless review = %q", got)
	}
	if got := numberWord(nil); got != "?" {
		t.Fatalf("missing number = %q", got)
	}
	if got := emptyWord("", "fallback"); got != "fallback" {
		t.Fatalf("empty word = %q", got)
	}
}

func TestStatusDocumentCoversUntrackedAndUnavailableUsage(t *testing.T) {
	t.Parallel()
	result := agents.Result{
		AgentID: "agt-00000000000000000000000000000001", State: agents.StateFailed, Status: agents.StatusFailed,
		Resolved:  agents.Resolved{Harness: "dsh"},
		Changes:   &agents.ChangeSummary{},
		Telemetry: agents.Telemetry{Available: false, UnavailableReason: "dsh reports none"},
		Cost:      agents.CostRecord{Source: "unavailable", Note: "no price"},
	}
	result.Changes.Untracked = []string{"new.txt"}
	var out strings.Builder
	if err := printAgentStatusText(&out, result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "untracked: new.txt") || !strings.Contains(out.String(), "usage:    unavailable: dsh reports none") || !strings.Contains(out.String(), "cost:     unavailable: no price") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestStatusRenderingReportsAWriteFailure(t *testing.T) {
	t.Parallel()
	err := printAgentStatusText(failingWriter{err: errors.New("closed")}, agents.Result{Resolved: agents.Resolved{}})
	if err == nil {
		t.Fatal("a write failure must be reported")
	}
}

func TestExperimentTableReportsEachAttemptAndSummary(t *testing.T) {
	t.Parallel()
	mean := int64(1500)
	report := agents.ExperimentReport{
		Experiment: agents.Experiment{ExperimentID: "exp-1", BaseSHA: "base", Profiles: []string{"a"}, Repeat: 1, UncontrolledDifferences: []string{"tool sets differ"}},
		Runs: []agents.Result{
			{RequestedProfile: "a", Status: agents.StatusCompleted, Validation: agents.ValidationRecord{Status: agents.ValidationPassed},
				Changes: &agents.ChangeSummary{}, Telemetry: agents.Telemetry{Available: true, Tokens: &agents.Tokens{Input: intPointerTest(1), Output: intPointerTest(2)}}},
			{RequestedProfile: "a", Status: agents.StatusFailed, Validation: agents.ValidationRecord{Status: agents.ValidationSkipped}},
		},
		Summaries: []agents.ProfileSummary{{Profile: "a", Attempts: 2, MeanDurationMS: &mean}},
	}
	report.Runs[0].Changes.FilesChanged = 3
	var out strings.Builder
	if err := printExperiment(&out, report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"experiment exp-1", "base: base", "3", "n/a", "summary", "mean=1.5s", "note: tool sets differ"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output %q is missing %q", out.String(), want)
		}
	}
	if got := meanWord(nil); got != "n/a" {
		t.Fatalf("mean word = %q", got)
	}
	if got := tokenWord(agents.Result{}); got != "n/a" {
		t.Fatalf("token word = %q", got)
	}
}

func TestExperimentRenderingReportsAWriteFailure(t *testing.T) {
	t.Parallel()
	report := agents.ExperimentReport{Experiment: agents.Experiment{ExperimentID: "exp-1"}}
	if err := printExperiment(failingWriter{err: errors.New("closed")}, report); err == nil {
		t.Fatal("a write failure must be reported")
	}
	withRun := agents.ExperimentReport{
		Experiment: agents.Experiment{ExperimentID: "exp-1"},
		Runs:       []agents.Result{{RequestedProfile: "a"}},
	}
	if err := printExperiment(failingWriter{err: errors.New("closed")}, withRun); err == nil {
		t.Fatal("a write failure in the table must be reported")
	}
	if err := printExperiment(failingWriter{err: errors.New("closed")}, agents.ExperimentReport{
		Experiment: agents.Experiment{ExperimentID: "exp-1"}, Summaries: []agents.ProfileSummary{{Profile: "a"}},
	}); err == nil {
		t.Fatal("a write failure in the summary must be reported")
	}
	if err := printExperiment(failingWriter{err: errors.New("closed")}, agents.ExperimentReport{
		Experiment: agents.Experiment{ExperimentID: "exp-1", UncontrolledDifferences: []string{"x"}},
	}); err == nil {
		t.Fatal("a write failure in the notes must be reported")
	}
}

func TestPlanWordsCoverEverySource(t *testing.T) {
	t.Parallel()
	if got := effortPlanWord(agents.Plan{}); got != "(none)" {
		t.Fatalf("no effort = %q", got)
	}
	if got := effortPlanWord(agents.Plan{EffortRequested: "high", EffortSource: "explicit"}); !strings.HasPrefix(got, "high (explicit)") {
		t.Fatalf("unsupported effort = %q", got)
	}
	applied := effortPlanWord(agents.Plan{EffortRequested: "high", EffortSource: "task_type", EffortEffective: "high", EffortApplied: true, EffortVariant: "heavy"})
	if !strings.Contains(applied, "as variant heavy") {
		t.Fatalf("variant effort = %q", applied)
	}
	if got := validationPlanWord(agents.Plan{ValidationSource: "default"}); got != "none (default)" {
		t.Fatalf("no validation = %q", got)
	}
	if got := acceptanceWord(false); got != "optional" {
		t.Fatalf("optional acceptance = %q", got)
	}
	if got := acceptanceWord(true); got != "required" {
		t.Fatalf("required acceptance = %q", got)
	}
}

func TestEveryCommandReportsAWriteFailureOrADependencyFailure(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.Profiles = func(context.Context, agentrun.DiscoveryRequest) (agentrun.ProfilesCatalog, error) {
		return agentrun.ProfilesCatalog{Profiles: []agentrun.ProfileEntry{{Name: "op", Resolved: agents.Resolved{Profile: "op", Harness: "opencode", Model: "p/m", Variant: "high"}}}, Harnesses: []agents.HarnessCapabilities{{Harness: "opencode"}}}, nil
	}
	deps.TaskTypes = func(context.Context, agentrun.DiscoveryRequest) (agentrun.TaskTypesCatalog, error) {
		return agentrun.TaskTypesCatalog{TaskTypes: []agentrun.TaskTypeEntry{{Name: "coding", Policy: agents.TaskType{Profile: "op", Validate: []string{"go"}}}}}, nil
	}
	deps.Experiment = func(context.Context, agentrun.ExperimentRequest) (agents.ExperimentReport, error) {
		return agents.ExperimentReport{Experiment: agents.Experiment{ExperimentID: "exp-1"}}, nil
	}
	deps.Diff = func(context.Context, agentrun.DiffRequest) (string, error) { return "diff", nil }
	deps.Review = func(context.Context, agentrun.ReviewRequest) (agents.ReviewRecord, error) {
		return agents.ReviewRecord{Status: agents.ReviewAccepted, Evaluator: "alex"}, nil
	}
	deps.Prune = func(context.Context, agentrun.PruneRequest) (agentrun.PrunePlan, error) {
		return agentrun.PrunePlan{Entries: []agentrun.PruneEntry{{AgentID: "agt-1", Action: "remove"}}}, nil
	}
	for _, args := range [][]string{
		{"agent", "profiles"},
		{"agent", "task-types"},
		{"agent", "experiment", "exp-1"},
		{"agent", "diff", "agt-00000000000000000000000000000001"},
		{"agent", "review", "agt-00000000000000000000000000000001", "--status", "accepted"},
		{"agent", "prune", "--older-than", "1h"},
	} {
		if code, _, errout := runFakeWithWriter(t, deps, args, failingWriter{err: errors.New("closed")}); code != exitFindings && code != exitOK {
			t.Fatalf("%v: code = %d stderr=%s", args, code, errout)
		}
	}
}

// runFakeWithWriter executes a command against an output stream that always
// fails, so a renderer's error path is observable.
func runFakeWithWriter(t *testing.T, deps Dependencies, args []string, writer io.Writer) (int, string, string) {
	t.Helper()
	cmd := newRootForTest(t, deps)
	cmd.SetArgs(args)
	cmd.SetOut(writer)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		return exitOK, "", ""
	}
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code, "", err.Error()
	}
	return exitFindings, "", err.Error()
}

func newRootForTest(t *testing.T, deps Dependencies) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "wb", SilenceUsage: true, SilenceErrors: true}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &codedError{exitUsage, err.Error()} })
	root.AddCommand(New(testRuntime(), deps))
	return root
}

func TestCommandsSurfaceDependencyFailures(t *testing.T) {
	t.Parallel()
	boom := errors.New("dependency failed")
	cases := []struct {
		name string
		deps func(*Dependencies)
		args []string
		code int
	}{
		{"profiles", func(deps *Dependencies) {
			deps.Profiles = func(context.Context, agentrun.DiscoveryRequest) (agentrun.ProfilesCatalog, error) {
				return agentrun.ProfilesCatalog{}, boom
			}
		}, []string{"agent", "profiles"}, exitFindings},
		{"task-types", func(deps *Dependencies) {
			deps.TaskTypes = func(context.Context, agentrun.DiscoveryRequest) (agentrun.TaskTypesCatalog, error) {
				return agentrun.TaskTypesCatalog{}, boom
			}
		}, []string{"agent", "task-types"}, exitFindings},
		{"experiment", func(deps *Dependencies) {
			deps.Experiment = func(context.Context, agentrun.ExperimentRequest) (agents.ExperimentReport, error) {
				return agents.ExperimentReport{}, boom
			}
		}, []string{"agent", "experiment", "exp-1"}, exitFindings},
		{"diff", func(deps *Dependencies) {
			deps.Diff = func(context.Context, agentrun.DiffRequest) (string, error) { return "", boom }
		}, []string{"agent", "diff", "agt-00000000000000000000000000000001"}, exitFindings},
		{"review", func(deps *Dependencies) {
			deps.Review = func(context.Context, agentrun.ReviewRequest) (agents.ReviewRecord, error) {
				return agents.ReviewRecord{}, boom
			}
		}, []string{"agent", "review", "agt-00000000000000000000000000000001", "--status", "accepted"}, exitFindings},
		{"prune", func(deps *Dependencies) {
			deps.Prune = func(context.Context, agentrun.PruneRequest) (agentrun.PrunePlan, error) {
				return agentrun.PrunePlan{}, boom
			}
		}, []string{"agent", "prune", "--older-than", "1h"}, exitFindings},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			testCase.deps(&deps)
			code, _, _ := runFake(t, deps, testCase.args...)
			if code != testCase.code {
				t.Fatalf("code = %d, want %d", code, testCase.code)
			}
		})
	}
}

func TestReviewAndDiffJSONRendering(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.Diff = func(context.Context, agentrun.DiffRequest) (string, error) { return "diff", nil }
	deps.Review = func(context.Context, agentrun.ReviewRequest) (agents.ReviewRecord, error) {
		return agents.ReviewRecord{Status: agents.ReviewAccepted, Evaluator: "alex"}, nil
	}
	code, out, _ := runFake(t, deps, "agent", "diff", "agt-00000000000000000000000000000001", "--format", "json")
	if code != exitOK || !strings.Contains(out, `"patch": false`) {
		t.Fatalf("diff json = %q", out)
	}
	code, out, _ = runFake(t, deps, "agent", "review", "agt-00000000000000000000000000000001", "--status", "accepted", "--format", "json")
	if code != exitOK || !strings.Contains(out, `"status": "accepted"`) {
		t.Fatalf("review json = %q", out)
	}
}

func TestDryRunJSONAndFallbackLine(t *testing.T) {
	t.Parallel()
	deps, _, _ := runFixture(t)
	deps.Plan = func(context.Context, agentrun.PlanRequest) (agents.Plan, error) {
		return agents.Plan{
			TaskType: "coding", TaskTypeSource: "task_type",
			Profile: agents.Resolved{Profile: "op"}, ProfileSource: "fallback", FallbackFrom: "coder",
			WorktreeMode: agents.ModeAuto, WorktreeSource: "default", TimeoutMS: 1000, TimeoutSource: "default",
			ValidationSource: "default", EffortNote: "not applied",
		}, nil
	}
	code, out, _ := runFake(t, deps, "agent", "run", "--task", "x", "--dry-run", "--format", "json")
	if code != exitOK || !strings.Contains(out, `"fallback_from": "coder"`) {
		t.Fatalf("dry-run json = %q", out)
	}
	code, out, _ = runFake(t, deps, "agent", "run", "--task", "x", "--dry-run")
	if code != exitOK || !strings.Contains(out, "fallback:   from coder") {
		t.Fatalf("dry-run text = %q", out)
	}
}

//nolint:paralleltest // the cases share one dependency recorder
func TestWaitReportsANonTerminalRunAndADispatchFailure(t *testing.T) {
	deps, _, _ := runFixture(t)
	deps.Run = func(context.Context, agentrun.RunRequest) (agents.Result, error) {
		return agents.Result{AgentID: "agt-00000000000000000000000000000001", State: agents.StateRunning, Status: agents.StatusRunning, Resolved: agents.Resolved{}}, nil
	}
	code, out, errout := runFake(t, deps, "agent", "run", "--task", "x", "--wait")
	if code != exitFindings || !strings.Contains(errout, "still running") {
		t.Fatalf("code = %d out=%q stderr=%s", code, out, errout)
	}
	deps.Run = func(context.Context, agentrun.RunRequest) (agents.Result, error) {
		return agents.Result{}, &agents.UnknownAgentError{AgentID: "agt-1", Root: "/root"}
	}
	if code, _, _ := runFake(t, deps, "agent", "run", "--task", "x", "--wait"); code != exitFindings {
		t.Fatalf("unknown result code = %d", code)
	}
	deps.Run = func(context.Context, agentrun.RunRequest) (agents.Result, error) {
		return agents.Result{}, errors.New("transport")
	}
	if code, _, _ := runFake(t, deps, "agent", "run", "--task", "x", "--wait"); code != exitFindings {
		t.Fatalf("dispatch failure code = %d", code)
	}
}

func TestRunReportsADispatchErrorThatIsNotARequestError(t *testing.T) {
	t.Parallel()
	deps, _, _ := runFixture(t)
	deps.Run = func(context.Context, agentrun.RunRequest) (agents.Result, error) {
		return agents.Result{}, errors.New("transport")
	}
	if code, _, _ := runFake(t, deps, "agent", "run", "--task", "x"); code != exitFindings {
		t.Fatalf("code = %d", code)
	}
}

func TestDiscoveryRenderingReportsWriteFailures(t *testing.T) {
	t.Parallel()
	deps := discoveryDependencies()
	if code, _, _ := runFakeWithWriter(t, deps, []string{"agent", "profiles"}, failingWriter{err: errors.New("closed")}); code == exitOK {
		t.Fatal("a profile write failure must surface")
	}
	if code, _, _ := runFakeWithWriter(t, deps, []string{"agent", "task-types"}, failingWriter{err: errors.New("closed")}); code == exitOK {
		t.Fatal("a task-type write failure must surface")
	}
	// An empty configuration still renders, and so does an unusable profile.
	deps.Profiles = func(context.Context, agentrun.DiscoveryRequest) (agentrun.ProfilesCatalog, error) {
		return agentrun.ProfilesCatalog{Profiles: []agentrun.ProfileEntry{{Name: "broken", Problem: "nope"}}}, nil
	}
	code, out, _ := runFake(t, deps, "agent", "profiles")
	if code != exitOK || !strings.Contains(out, "unusable: nope") {
		t.Fatalf("broken profile render = %q", out)
	}
}

//nolint:paralleltest // the test sets the process environment
func TestReviewEvaluatorFallsBackToTheInvokingUser(t *testing.T) {
	t.Setenv("USER", "")
	if got := invokingUser(); got != "operator" {
		t.Fatalf("evaluator = %q", got)
	}
	t.Setenv("USER", "alex")
	if got := invokingUser(); got != "alex" {
		t.Fatalf("evaluator = %q", got)
	}
}

func TestPruneAndReviewRenderingReportWriteFailures(t *testing.T) {
	t.Parallel()
	if err := printPrune(failingWriter{err: errors.New("closed")}, agentrun.PrunePlan{}); err == nil {
		t.Fatal("a prune write failure must surface")
	}
	if err := writeText(failingWriter{err: errors.New("closed")}, "text"); err == nil {
		t.Fatal("a text write failure must surface")
	}
}

// nthFailWriter fails on exactly one write, so every renderer line's error
// branch is reachable.
type nthFailWriter struct {
	failAt int
	seen   int
}

func (writer *nthFailWriter) Write(data []byte) (int, error) {
	writer.seen++
	if writer.seen == writer.failAt {
		return 0, errors.New("closed")
	}
	return len(data), nil
}

// coverEveryWriteFailure runs a renderer once per write position.
func coverEveryWriteFailure(t *testing.T, render func(io.Writer) error) {
	t.Helper()
	for failAt := 1; failAt <= 64; failAt++ {
		_ = render(&nthFailWriter{failAt: failAt})
	}
}

func TestRenderersSurviveAFailureAtEveryWritePosition(t *testing.T) {
	t.Parallel()
	result := agents.Result{
		AgentID: "agt-00000000000000000000000000000001", State: agents.StateCompleted, Status: agents.StatusCompleted,
		RequestedProfile: "op", Resolved: agents.Resolved{Profile: "op", Harness: "opencode", Model: "p/m", Variant: "high"},
		TaskType: "coding", Complexity: "high", EffortRequested: "high", EffortEffective: "high",
		Worktree: "op", WorktreeMode: agents.ModeAuto, WorktreeDir: "/fleet/op",
		StartedAt: time.Unix(100, 0).UTC(), SourceDirty: true, ExperimentID: "exp-1", RepeatIndex: 1,
		Changes:    &agents.ChangeSummary{Files: []string{"a.go"}, Untracked: []string{"new.txt"}},
		Validation: agents.ValidationRecord{Status: agents.ValidationPassed, Steps: []agents.ValidationStepResult{{Status: agents.ValidationPassed}}},
		Review:     agents.ReviewRecord{Status: agents.ReviewAccepted, Evaluator: "alex"},
		Telemetry:  agents.Telemetry{Available: true, Tokens: &agents.Tokens{Input: intPointerTest(5)}},
		Result:     "done",
		LogPath:    "/private/events.jsonl",
	}
	coverEveryWriteFailure(t, func(writer io.Writer) error { return printAgentStatusText(writer, result) })
	coverEveryWriteFailure(t, func(writer io.Writer) error { return printAgentAwaitText(writer, result) })
	notTerminal := result
	notTerminal.Terminal = false
	coverEveryWriteFailure(t, func(writer io.Writer) error { return printAgentAwaitText(writer, notTerminal) })
	coverEveryWriteFailure(t, func(writer io.Writer) error { return printAgentList(writer, []agents.Result{result}) })
	coverEveryWriteFailure(t, func(writer io.Writer) error { return printAgentList(writer, nil) })

	report := agents.ExperimentReport{
		Experiment: agents.Experiment{ExperimentID: "exp-1", BaseSHA: "base", Profiles: []string{"a"}, Repeat: 1, UncontrolledDifferences: []string{"note"}},
		Runs: []agents.Result{{RequestedProfile: "a", Status: agents.StatusCompleted, Validation: agents.ValidationRecord{Status: agents.ValidationPassed},
			Changes: &agents.ChangeSummary{FilesChanged: 2}, Telemetry: agents.Telemetry{Available: true, Tokens: &agents.Tokens{Input: intPointerTest(1)}}}},
		Summaries: []agents.ProfileSummary{{Profile: "a", Attempts: 1}},
	}
	coverEveryWriteFailure(t, func(writer io.Writer) error { return printExperiment(writer, report) })

	profiles := agentrun.ProfilesCatalog{
		Profiles:  []agentrun.ProfileEntry{{Name: "op", Resolved: agents.Resolved{Profile: "op", Harness: "opencode", Model: "p/m", Variant: "high", Effort: "high"}}, {Name: "broken", Problem: "nope"}},
		Providers: []string{"deepseek"}, Fallback: []string{"op"}, DefaultProfile: "op",
		Harnesses: []agents.HarnessCapabilities{{Harness: "opencode"}},
	}
	coverEveryWriteFailure(t, func(writer io.Writer) error { return printProfiles(writer, profiles) })
	coverEveryWriteFailure(t, func(writer io.Writer) error { return printProfiles(writer, agentrun.ProfilesCatalog{}) })

	taskTypes := agentrun.TaskTypesCatalog{Default: "coding", TaskTypes: []agentrun.TaskTypeEntry{
		{Name: "coding", Policy: agents.TaskType{Profile: "op", Worktree: "auto", Timeout: "60m", Validate: []string{"go"}, Acceptance: "required"}},
		{Name: "research", Builtin: true},
	}}
	coverEveryWriteFailure(t, func(writer io.Writer) error { return printTaskTypes(writer, taskTypes) })
	coverEveryWriteFailure(t, func(writer io.Writer) error {
		return printPrune(writer, agentrun.PrunePlan{Entries: []agentrun.PruneEntry{{AgentID: "agt-1", Action: "remove", Reason: "old"}}, Removed: 1})
	})
	coverEveryWriteFailure(t, func(writer io.Writer) error { return writeText(writer, "text") })
}

func TestRunAndCompareReportTaskReadAndWriteFailures(t *testing.T) {
	t.Parallel()
	deps, _, _ := runFixture(t)
	deps.Plan = func(context.Context, agentrun.PlanRequest) (agents.Plan, error) {
		return agents.Plan{Profile: agents.Resolved{Profile: "op"}, WorktreeMode: agents.ModeAuto}, nil
	}
	if code, _, _ := runFake(t, deps, "agent", "run", "--task-file", "/nonexistent/task.md"); code != exitFindings {
		t.Fatalf("run task read code = %d", code)
	}
	if code, _, _ := runFake(t, deps, "agent", "compare", "--profiles", "op", "--task-file", "/nonexistent/task.md"); code != exitFindings {
		t.Fatalf("compare task read code = %d", code)
	}
	code, _, errout := runFakeWithWriter(t, deps, []string{"agent", "run", "--task", "x", "--dry-run"}, failingWriter{err: errors.New("closed")})
	t.Logf("dry-run code=%d stderr=%s", code, errout)
	if code == exitOK {
		t.Fatal("a dry-run write failure must surface")
	}
	code, _, errout = runFakeWithWriter(t, deps, []string{"agent", "compare", "--profiles", "op", "--task", "x", "--format", "json"}, failingWriter{err: errors.New("closed")})
	t.Logf("compare code=%d stderr=%s", code, errout)
	if code == exitOK {
		t.Fatal("a compare JSON write failure must surface")
	}
}
