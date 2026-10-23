package cmdpr

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
)

func integrationDeps(result orchestrate.IntegrationResult, got *orchestrate.IntegrationOptions) IntegrationDependencies {
	run := func(_ context.Context, options orchestrate.IntegrationOptions) (orchestrate.IntegrationResult, error) {
		*got = options
		return result, nil
	}
	return IntegrationDependencies{Add: run, Merge: run}
}

func TestIntegrationVerbsAreWiredUnderPRBatchAndIntegration(t *testing.T) {
	t.Parallel()
	deps := DefaultIntegrationDependencies()
	if deps.Add == nil || deps.Merge == nil {
		t.Fatal("a default integration verb is nil")
	}
	pr := New(testRuntime(), Dependencies{Integration: deps})
	if found, _, err := pr.Find([]string{"batch", "add"}); err != nil || found.Flags().Lookup("target") == nil {
		t.Fatalf("wb pr batch add --target missing: %v", err)
	}
	integration := NewIntegration(testRuntime(), deps)
	merge, _, err := integration.Find([]string{"merge"})
	if err != nil || merge.Flags().Lookup("checks") == nil || merge.Flags().Lookup("target") != nil {
		t.Fatalf("wb integration merge flags wrong: %v", err)
	}
	for _, want := range []string{"commitcomment", "git commit --no-edit", "no ruleset", "--base <integration>"} {
		if !strings.Contains(strings.ReplaceAll(merge.Long, "\n", " "), want) {
			t.Errorf("integration help lacks %q", want)
		}
	}
}

func TestPRBatchAddPassesTargetWorktreeAndPins(t *testing.T) {
	t.Parallel()
	var got orchestrate.IntegrationOptions
	result := orchestrate.IntegrationResult{Outcome: orchestrate.LandFindings, RefusalCode: orchestrate.BatchRefusalConflict, Reason: "conflicts in a.go",
		Command: "wb pr batch add again", Refusals: []orchestrate.BatchIssue{{Code: "batch-source-unreviewed", Source: "lane@abc", Reason: "no review"}},
		Lanes:  []orchestrate.LaneMerge{{Branch: "lane", SHA: "abc1234def", State: orchestrate.LaneStateConflicted, ConflictPaths: []string{"a.go"}}},
		Checks: &quality.VerificationReport{Results: []quality.VerificationEntry{{Check: quality.CheckTest, Status: quality.StatusFailed, Detail: "boom\n"}, {Check: quality.CheckLint, Status: quality.StatusPassed}}}}
	var out, errOut bytes.Buffer
	code := executeTest(NewBatch(testRuntime(), integrationDeps(result, &got)), []string{"add", "acme/app", "--target", "public-demo", "--worktree", "/w", "lane@abc1234=https://x"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if got.Repository != "acme/app" || got.Integration != "public-demo" || got.Worktree != "/w" || strings.Join(got.Pins, ",") != "lane@abc1234=https://x" {
		t.Fatalf("options = %+v", got)
	}
	for _, want := range []string{"refusal: batch-conflict", "batch-source-unreviewed lane@abc: no review", "lane@abc1234def conflicted conflicts: a.go", "check test failed: boom", "next: wb pr batch add again"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "check lint") {
		t.Error("a passed check was reported as failed")
	}
}

func TestIntegrationMergeParsesChecksAndPrintsJSON(t *testing.T) {
	t.Parallel()
	var got orchestrate.IntegrationOptions
	var out, errOut bytes.Buffer
	code := executeTest(NewIntegration(testRuntime(), integrationDeps(orchestrate.IntegrationResult{Outcome: orchestrate.LandSuccess, Verb: "integration merge"}, &got)),
		[]string{"merge", "acme/app", "public-demo", "lane@abc1234=https://x", "--checks", "lint,spec", "--format", "json"}, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), `"verb": "integration merge"`) || got.Worktree == "" {
		t.Fatalf("exit %d out %s options %+v", code, out.String(), got)
	}
	if got.Integration != "public-demo" || len(got.Pins) != 1 || len(got.Checks) != 2 || got.Checks[1] != quality.CheckSpec {
		t.Fatalf("options = %+v", got)
	}
}

func TestIntegrationUsageErrors(t *testing.T) {
	t.Parallel()
	var got orchestrate.IntegrationOptions
	deps := integrationDeps(orchestrate.IntegrationResult{Outcome: orchestrate.LandRefused, Reason: "refused"}, &got)
	for name, run := range map[string]func(*bytes.Buffer, *bytes.Buffer) int{
		"add without pins": func(out, errOut *bytes.Buffer) int {
			return executeTest(NewBatch(testRuntime(), deps), []string{"add", "acme/app", "--target", "x"}, out, errOut)
		},
		"add without target": func(out, errOut *bytes.Buffer) int {
			return executeTest(NewBatch(testRuntime(), deps), []string{"add", "acme/app", "lane@abc1234=u"}, out, errOut)
		},
		"merge without integration": func(out, errOut *bytes.Buffer) int {
			return executeTest(NewIntegration(testRuntime(), deps), []string{"merge", "acme/app"}, out, errOut)
		},
		"unknown check": func(out, errOut *bytes.Buffer) int {
			return executeTest(NewIntegration(testRuntime(), deps), []string{"merge", "acme/app", "x", "--checks", "fuzz"}, out, errOut)
		},
		"refused": func(out, errOut *bytes.Buffer) int {
			return executeTest(NewIntegration(testRuntime(), deps), []string{"merge", "acme/app", "x"}, out, errOut)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var out, errOut bytes.Buffer
			if code := run(&out, &errOut); code != 2 {
				t.Errorf("exit %d, want 2 (%s)", code, errOut.String())
			}
		})
	}
	var out, errOut bytes.Buffer
	if code := executeTest(NewIntegration(testRuntime(), deps), []string{"merge", "acme/app", "x", "--format", "yaml"}, &out, &errOut); code == 0 {
		t.Fatal("an unsupported format exited 0")
	}
}

func TestIntegrationReportsVerbErrorsAndWriteFailures(t *testing.T) {
	t.Parallel()
	failing := func(context.Context, orchestrate.IntegrationOptions) (orchestrate.IntegrationResult, error) {
		return orchestrate.IntegrationResult{}, context.Canceled
	}
	var out, errOut bytes.Buffer
	if code := executeTest(NewIntegration(testRuntime(), IntegrationDependencies{Merge: failing}), []string{"merge", "acme/app", "x"}, &out, &errOut); code == 0 {
		t.Fatal("a verb error exited 0")
	}
	if err := printIntegration(failingWriter{}, "text", orchestrate.IntegrationResult{}); err == nil {
		t.Fatal("a text write failure was swallowed")
	}
	if err := printIntegration(failingWriter{}, "json", orchestrate.IntegrationResult{}); err == nil {
		t.Fatal("a json write failure was swallowed")
	}
	var got orchestrate.IntegrationOptions
	command := NewIntegration(testRuntime(), integrationDeps(orchestrate.IntegrationResult{Outcome: orchestrate.LandSuccess}, &got))
	command.SetOut(failingWriter{})
	command.SetErr(&errOut)
	command.SetArgs([]string{"merge", "acme/app", "x"})
	command.SilenceErrors, command.SilenceUsage = true, true
	if err := command.Execute(); err == nil {
		t.Fatal("an output failure exited 0")
	}
}
