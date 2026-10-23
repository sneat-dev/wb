package cmdpr

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

func batchDeps(result orchestrate.BatchResult, got *orchestrate.BatchOptions) BatchDependencies {
	run := func(_ context.Context, options orchestrate.BatchOptions) (orchestrate.BatchResult, error) {
		*got = options
		return result, nil
	}
	return BatchDependencies{Create: run, Add: run, Resume: run}
}

func TestPRBatchHasItsFiveVerbsAndDefaults(t *testing.T) {
	t.Parallel()
	deps := DefaultBatchDependencies()
	if deps.Create == nil || deps.Add == nil || deps.Resume == nil {
		t.Fatal("a default batch verb is nil")
	}
	command := NewBatch(testRuntime(), deps)
	var names []string
	for _, sub := range command.Commands() {
		names = append(names, sub.Name())
	}
	if strings.Join(names, ",") != "add,create,resume" {
		t.Fatalf("verbs = %v", names)
	}
	if !strings.Contains(command.Long, "--branch public-demo-batch-1") || !strings.Contains(command.Long, "still reviewed") {
		t.Fatal("batch help lacks the worktree recipe or the review rule")
	}
	pr := New(testRuntime(), Dependencies{Batch: deps})
	if found, _, err := pr.Find([]string{"batch", "resume"}); err != nil || found.Flags().Lookup("worktree") == nil {
		t.Fatalf("wb pr batch resume --worktree missing: %v", err)
	}
}

func TestPRBatchCreatePassesRepositoryTargetWorktreeAndPins(t *testing.T) {
	t.Parallel()
	var got orchestrate.BatchOptions
	state := orchestrate.BatchState{Target: "public-demo", Steps: []orchestrate.BatchStep{
		{Kind: orchestrate.BatchStepSource, PullRequest: 510, Head: "abc", State: orchestrate.BatchStateConflicted, ConflictPaths: []string{"a.go"}},
		{Kind: orchestrate.BatchStepRefresh, Head: "def", State: orchestrate.BatchStatePending},
	}}
	result := orchestrate.BatchResult{Outcome: orchestrate.LandFindings, RefusalCode: orchestrate.BatchRefusalConflict, Reason: "conflicts in a.go",
		Command: "wb pr batch resume", PullRequest: "acme/app#7", State: &state,
		Refusals: []orchestrate.BatchIssue{{Code: "batch-source-unreviewed", Source: "#511", Reason: "no review"}}}
	var out, errOut bytes.Buffer
	code := executeTest(NewBatch(testRuntime(), batchDeps(result, &got)), []string{"create", "acme/app", "--target", "public-demo", "--worktree", "/w", "510@abc1234=https://x"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if got.Repository != "acme/app" || got.Target != "public-demo" || got.Worktree != "/w" || strings.Join(got.Pins, ",") != "510@abc1234=https://x" {
		t.Fatalf("options = %+v", got)
	}
	for _, want := range []string{"refusal: batch-conflict", "batch-source-unreviewed #511", "pull request: acme/app#7", "1. #510 conflicted abc conflicts: a.go",
		"2. refresh public-demo pending def", "next: wb pr batch resume"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestPRBatchJSONSuccessAndCurrentDirectory(t *testing.T) {
	t.Parallel()
	var got orchestrate.BatchOptions
	var out, errOut bytes.Buffer
	code := executeTest(NewBatch(testRuntime(), batchDeps(orchestrate.BatchResult{Outcome: orchestrate.LandSuccess, Verb: "pr batch resume"}, &got)),
		[]string{"resume", "acme/app", "--target", "public-demo", "--format", "json"}, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), `"verb": "pr batch resume"`) || got.Worktree == "" {
		t.Fatalf("exit %d out %s options %+v", code, out.String(), got)
	}
}

func TestPRBatchUsageErrors(t *testing.T) {
	t.Parallel()
	var got orchestrate.BatchOptions
	deps := batchDeps(orchestrate.BatchResult{Outcome: orchestrate.LandRefused, Reason: "refused"}, &got)
	cases := [][]string{
		{"create", "acme/app", "--target", "public-demo"},
		{"resume", "acme/app", "extra", "--target", "public-demo"},
		{"resume"},
		{"add", "acme/app"},
		{"resume", "acme/app"},
	}
	for _, args := range cases {
		var out, errOut bytes.Buffer
		if code := executeTest(NewBatch(testRuntime(), deps), args, &out, &errOut); code != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, code, errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := executeTest(NewBatch(testRuntime(), deps), []string{"resume", "acme/app", "--target", "x", "--format", "yaml"}, &out, &errOut); code == 0 {
		t.Fatal("an unsupported format exited 0")
	}
	if code := executeTest(NewBatch(testRuntime(), deps), []string{"resume", "acme/app", "--target", "public-demo"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "refused") {
		t.Fatalf("refused verb: exit %d %s", code, errOut.String())
	}
}

func TestPRBatchReportsVerbErrorsAndWriteFailures(t *testing.T) {
	t.Parallel()
	failing := func(context.Context, orchestrate.BatchOptions) (orchestrate.BatchResult, error) {
		return orchestrate.BatchResult{}, context.Canceled
	}
	deps := BatchDependencies{Create: failing, Add: failing, Resume: failing}
	var out, errOut bytes.Buffer
	if code := executeTest(NewBatch(testRuntime(), deps), []string{"resume", "acme/app", "--target", "public-demo"}, &out, &errOut); code == 0 {
		t.Fatal("a verb error exited 0")
	}
	if err := printBatch(failingWriter{}, "text", orchestrate.BatchResult{}); err == nil {
		t.Fatal("a text write failure was swallowed")
	}
	if err := printBatch(failingWriter{}, "json", orchestrate.BatchResult{}); err == nil {
		t.Fatal("a json write failure was swallowed")
	}
	var got orchestrate.BatchOptions
	quiet := batchDeps(orchestrate.BatchResult{Outcome: orchestrate.LandSuccess}, &got)
	command := NewBatch(testRuntime(), quiet)
	command.SetOut(failingWriter{})
	command.SetErr(&errOut)
	command.SetArgs([]string{"resume", "acme/app", "--target", "public-demo"})
	command.SilenceErrors, command.SilenceUsage = true, true
	if err := command.Execute(); err == nil {
		t.Fatal("an output failure exited 0")
	}
}
