package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/spf13/cobra"
)

func newOutputCapturingCommand() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	command := &cobra.Command{Use: "test"}
	command.SetOut(&out)
	return command, &out
}

// errAtWrite is the sentinel a failAtCallWriter returns from its Nth Write.
var errAtWrite = errors.New("write failed")

// failAtCallWriter succeeds every Write call before failAt (1-indexed) and
// fails on and after it. Every one of printPullRequestLand's and
// printPullRequestCreate's Fprintf/Fprintln calls does exactly one Write, so
// this is the input that drives each "if err != nil { return err }" branch
// on demand without needing a real broken pipe or OS failure.
type failAtCallWriter struct {
	calls  int
	failAt int
}

func (w *failAtCallWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls >= w.failAt {
		return 0, errAtWrite
	}
	return len(p), nil
}

func newFailingCommand(failAt int) *cobra.Command {
	command := &cobra.Command{Use: "test"}
	command.SetOut(&failAtCallWriter{failAt: failAt})
	return command
}

// TestPrintPullRequestLandRendersASuccessfulLanding drives every
// conditional print in printPullRequestLand's LandSuccess branch: branch
// deletion, cleaned tasks, kept worktree, reviewer, review finding, closed
// issues, and a kept commit.
func TestPrintPullRequestLandRendersASuccessfulLanding(t *testing.T) {
	t.Parallel()
	command, out := newOutputCapturingCommand()
	result := orchestrate.PullRequestLandResult{
		Repository: "acme/app", PullRequest: 42, Title: "Fix thing", Mechanical: true,
		Outcome: orchestrate.LandSuccess, MergeSHA: "0123456789abcdef", BaseRef: "main",
		BranchDeleted: true, HeadRef: "feature-x",
		CleanedTasks:    []string{"task-a"},
		Kept:            false,
		Reviewer:        "gpt-5@codex",
		ReviewedHeadSHA: "0123456789abcdef",
		Evidence:        map[string]string{"review": "no findings", "closes": "Closes #7"},
		Closes:          []int{7},
		Commits: []orchestrate.LandedCommit{
			{SourceSHA: "abcdef012345", Subject: "kept commit", Kept: true},
			{SourceSHA: "fedcba012345", Subject: "squashed away", Kept: false},
		},
	}

	if err := printPullRequestLand(command, result); err != nil {
		t.Fatalf("printPullRequestLand returned %v, want nil", err)
	}

	rendered := out.String()
	for _, want := range []string{
		"acme/app#42 Fix thing (mechanical bump)",
		"landed 0123456789ab on main",
		"retired origin/feature-x",
		"retired worktree for task task-a",
		"reviewer: gpt-5@codex",
		"finding: no findings",
		"Closes #7",
		"kept commit abcdef012345 kept commit",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("output missing %q: %q", want, rendered)
		}
	}
	if strings.Contains(rendered, "squashed away") {
		t.Errorf("output rendered the squashed (non-kept) commit: %q", rendered)
	}
}

// TestPrintPullRequestLandRendersAKeptWorktree drives the "result.Kept"
// branch on its own (mutually exclusive with BranchDeleted in practice, but
// the print function does not enforce that).
func TestPrintPullRequestLandRendersAKeptWorktree(t *testing.T) {
	t.Parallel()
	command, out := newOutputCapturingCommand()
	result := orchestrate.PullRequestLandResult{
		Repository: "acme/app", PullRequest: 1, Title: "x",
		Outcome: orchestrate.LandSuccess, MergeSHA: "abc", BaseRef: "main",
		Kept: true,
	}
	if err := printPullRequestLand(command, result); err != nil {
		t.Fatalf("printPullRequestLand returned %v, want nil", err)
	}
	if !strings.Contains(out.String(), "kept the worktree (--keep)") {
		t.Errorf("output missing kept-worktree line: %q", out.String())
	}
}

// TestPrintPullRequestLandRendersARefusal drives the default (non-success)
// outcome branch: reason, refusal code, and sanctioned command.
func TestPrintPullRequestLandRendersARefusal(t *testing.T) {
	t.Parallel()
	command, out := newOutputCapturingCommand()
	result := orchestrate.PullRequestLandResult{
		Repository: "acme/app", PullRequest: 9, Title: "x",
		Outcome: orchestrate.LandOutcome("refused"), Reason: "checks are red",
		RefusalCode: "checks-red", SanctionedCommand: "wb ci wait acme/app#9",
	}
	if err := printPullRequestLand(command, result); err != nil {
		t.Fatalf("printPullRequestLand returned %v, want nil", err)
	}
	rendered := out.String()
	for _, want := range []string{
		"refused: checks are red",
		"refusal: checks-red",
		"resolve with: wb ci wait acme/app#9",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("output missing %q: %q", want, rendered)
		}
	}
}

// TestPrintPullRequestCreateRendersACreatedPullRequest drives the
// CreateSuccess branch: committed paths, created verb, auto-merge armed,
// and next command.
func TestPrintPullRequestCreateRendersACreatedPullRequest(t *testing.T) {
	t.Parallel()
	command, out := newOutputCapturingCommand()
	result := orchestrate.PullRequestCreateResult{
		Outcome:        orchestrate.CreateSuccess,
		CommittedPaths: []string{"a.go", "b.go"},
		Repository:     "acme/app", PullRequest: 5, Title: "Add thing",
		AutoMergeArmed: true,
		NextCommand:    "wb wait pr acme/app#5",
	}
	if err := printPullRequestCreate(command, result); err != nil {
		t.Fatalf("printPullRequestCreate returned %v, want nil", err)
	}
	rendered := out.String()
	for _, want := range []string{
		"committed a.go, b.go",
		"created acme/app#5 Add thing",
		"auto-merge armed",
		"next: wb wait pr acme/app#5",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("output missing %q: %q", want, rendered)
		}
	}
}

// TestPrintPullRequestCreateRendersAnAdoptedPullRequestWithFindings drives
// the "adopted" verb, the not-armed-with-reason branch, and the
// CreateFindings reason line.
func TestPrintPullRequestCreateRendersAnAdoptedPullRequestWithFindings(t *testing.T) {
	t.Parallel()
	command, out := newOutputCapturingCommand()
	result := orchestrate.PullRequestCreateResult{
		Outcome:    orchestrate.CreateFindings,
		Repository: "acme/app", PullRequest: 6, Title: "Bump dep", Adopted: true,
		AutoMergeReason: "target has no strict policy",
		Reason:          "mechanical bump needs review",
	}
	if err := printPullRequestCreate(command, result); err != nil {
		t.Fatalf("printPullRequestCreate returned %v, want nil", err)
	}
	rendered := out.String()
	for _, want := range []string{
		"adopted acme/app#6 Bump dep",
		"not armed: target has no strict policy",
		"finding: mechanical bump needs review",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("output missing %q: %q", want, rendered)
		}
	}
}

// TestPrintPullRequestCreateRendersARefusal drives the default (non-success,
// non-findings) outcome branch: reason, refusal code, sanctioned command.
func TestPrintPullRequestCreateRendersARefusal(t *testing.T) {
	t.Parallel()
	command, out := newOutputCapturingCommand()
	result := orchestrate.PullRequestCreateResult{
		Outcome: orchestrate.CreateOutcome("refused"), Reason: "dirty worktree",
		RefusalCode: "dirty", SanctionedCommand: "wb worktree guard .",
	}
	if err := printPullRequestCreate(command, result); err != nil {
		t.Fatalf("printPullRequestCreate returned %v, want nil", err)
	}
	rendered := out.String()
	for _, want := range []string{
		"refused: dirty worktree",
		"refusal: dirty",
		"resolve with: wb worktree guard .",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("output missing %q: %q", want, rendered)
		}
	}
}

// TestPrintPullRequestLandPropagatesAWriteFailureAtEveryPrintInSuccess
// drives every "return err" branch in printPullRequestLand's LandSuccess
// case: a result with every optional field populated so each print step is
// attempted, paired with a writer that fails at the exact call under test.
func TestPrintPullRequestLandPropagatesAWriteFailureAtEveryPrintInSuccess(t *testing.T) {
	t.Parallel()
	result := orchestrate.PullRequestLandResult{
		Repository: "acme/app", PullRequest: 1, Title: "x",
		Outcome: orchestrate.LandSuccess, MergeSHA: "abc", BaseRef: "main",
		BranchDeleted: true, HeadRef: "feature",
		CleanedTasks: []string{"task-a"},
		Kept:         true,
		Reviewer:     "gpt-5@codex",
		Evidence:     map[string]string{"review": "no findings", "closes": "Closes #7"},
		Closes:       []int{7},
		Commits:      []orchestrate.LandedCommit{{SourceSHA: "abc", Subject: "kept", Kept: true}},
	}
	// call order: 1 header, 2 landed, 3 retired origin, 4 retired task,
	// 5 kept worktree, 6 reviewer, 7 finding, 8 closes, 9 kept commit.
	for callIndex := 2; callIndex <= 9; callIndex++ {
		callIndex := callIndex
		t.Run(fmt.Sprintf("call-%d", callIndex), func(t *testing.T) {
			t.Parallel()
			command := newFailingCommand(callIndex)
			err := printPullRequestLand(command, result)
			if !errors.Is(err, errAtWrite) {
				t.Fatalf("printPullRequestLand (fail at call %d) returned %v, want errAtWrite", callIndex, err)
			}
		})
	}
}

// TestPrintPullRequestLandPropagatesAWriteFailureInDefaultOutcome drives
// every "return err" branch in printPullRequestLand's default (non-success)
// case.
func TestPrintPullRequestLandPropagatesAWriteFailureInDefaultOutcome(t *testing.T) {
	t.Parallel()
	result := orchestrate.PullRequestLandResult{
		Repository: "acme/app", PullRequest: 1, Title: "x",
		Outcome: orchestrate.LandOutcome("refused"), Reason: "checks are red",
		RefusalCode: "checks-red", SanctionedCommand: "wb ci wait acme/app#1",
	}
	// call order: 1 header, 2 outcome/reason, 3 refusal, 4 resolve.
	for callIndex := 2; callIndex <= 4; callIndex++ {
		callIndex := callIndex
		t.Run(fmt.Sprintf("call-%d", callIndex), func(t *testing.T) {
			t.Parallel()
			command := newFailingCommand(callIndex)
			err := printPullRequestLand(command, result)
			if !errors.Is(err, errAtWrite) {
				t.Fatalf("printPullRequestLand (fail at call %d) returned %v, want errAtWrite", callIndex, err)
			}
		})
	}
}

// TestPrintPullRequestCreatePropagatesAWriteFailureInSuccess drives every
// "return err" branch in printPullRequestCreate's CreateSuccess/CreateFindings
// case, including the mutually exclusive auto-merge-armed and
// not-armed-with-reason branches.
func TestPrintPullRequestCreatePropagatesAWriteFailureInSuccess(t *testing.T) {
	t.Parallel()
	armed := orchestrate.PullRequestCreateResult{
		Outcome:        orchestrate.CreateSuccess,
		CommittedPaths: []string{"a.go"},
		Repository:     "acme/app", PullRequest: 1, Title: "x",
		AutoMergeArmed: true,
		NextCommand:    "wb wait pr acme/app#1",
	}
	// call order: 1 committed, 2 created, 3 auto-merge armed, 4 next.
	for callIndex := 1; callIndex <= 4; callIndex++ {
		callIndex := callIndex
		t.Run(fmt.Sprintf("armed-call-%d", callIndex), func(t *testing.T) {
			t.Parallel()
			command := newFailingCommand(callIndex)
			err := printPullRequestCreate(command, armed)
			if !errors.Is(err, errAtWrite) {
				t.Fatalf("printPullRequestCreate (fail at call %d) returned %v, want errAtWrite", callIndex, err)
			}
		})
	}

	notArmed := orchestrate.PullRequestCreateResult{
		Outcome:    orchestrate.CreateFindings,
		Repository: "acme/app", PullRequest: 1, Title: "x",
		AutoMergeReason: "target has no strict policy",
		Reason:          "mechanical bump needs review",
	}
	// call order: 1 created (no committed paths), 2 not-armed reason,
	// 3 finding (CreateFindings && Reason != "").
	for callIndex := 2; callIndex <= 3; callIndex++ {
		callIndex := callIndex
		t.Run(fmt.Sprintf("not-armed-call-%d", callIndex), func(t *testing.T) {
			t.Parallel()
			command := newFailingCommand(callIndex)
			err := printPullRequestCreate(command, notArmed)
			if !errors.Is(err, errAtWrite) {
				t.Fatalf("printPullRequestCreate (fail at call %d) returned %v, want errAtWrite", callIndex, err)
			}
		})
	}
}

// TestPrintPullRequestCreatePropagatesAWriteFailureInDefaultOutcome drives
// every "return err" branch in printPullRequestCreate's default (refused)
// case.
func TestPrintPullRequestCreatePropagatesAWriteFailureInDefaultOutcome(t *testing.T) {
	t.Parallel()
	result := orchestrate.PullRequestCreateResult{
		Outcome: orchestrate.CreateOutcome("refused"), Reason: "dirty worktree",
		RefusalCode: "dirty", SanctionedCommand: "wb worktree guard .",
	}
	// call order: 1 outcome/reason, 2 refusal, 3 resolve.
	for callIndex := 1; callIndex <= 3; callIndex++ {
		callIndex := callIndex
		t.Run(fmt.Sprintf("call-%d", callIndex), func(t *testing.T) {
			t.Parallel()
			command := newFailingCommand(callIndex)
			err := printPullRequestCreate(command, result)
			if !errors.Is(err, errAtWrite) {
				t.Fatalf("printPullRequestCreate (fail at call %d) returned %v, want errAtWrite", callIndex, err)
			}
		})
	}
}

// TestRenderSessionsPropagatesAWriteFailure drives the two "return err"
// branches in renderSessions: the header line and a data row.
func TestRenderSessionsPropagatesAWriteFailure(t *testing.T) {
	t.Parallel()
	rows := []sessionRow{{
		View: session.View{
			Record: session.Record{WBSessionID: "s1", Machine: "mac", PID: 1, StartedAt: time.Now()},
			State:  session.StateLive,
		},
	}}

	t.Run("header", func(t *testing.T) {
		t.Parallel()
		err := renderSessions(&failAtCallWriter{failAt: 1}, rows)
		if !errors.Is(err, errAtWrite) {
			t.Fatalf("renderSessions (header) returned %v, want errAtWrite", err)
		}
	})
	t.Run("row", func(t *testing.T) {
		t.Parallel()
		err := renderSessions(&failAtCallWriter{failAt: 2}, rows)
		if !errors.Is(err, errAtWrite) {
			t.Fatalf("renderSessions (row) returned %v, want errAtWrite", err)
		}
	})
}

// TestWriteCoverageOutputToSummaryPropagatesAWriteFailure drives the two
// "return err" branches inside writeCoverageOutputTo's --format=summary
// case: the headline stats line and the diagnostics-index line (present
// only when a repository carries a failed-run diagnostic).
func TestWriteCoverageOutputToSummaryPropagatesAWriteFailure(t *testing.T) {
	t.Parallel()
	reportDir := t.TempDir()
	report := quality.CoverageReport{
		Statements: 10, Covered: 8, Percentage: 80,
		Repositories: []quality.RepositoryCoverage{{
			Repository: "acme/app", Statements: 10, Covered: 8,
			Diagnostic: &quality.CoverageDiagnostic{Manifest: "manifest.json", SHA256: "abc"},
		}},
	}

	t.Run("headline", func(t *testing.T) {
		t.Parallel()
		err := writeCoverageOutputTo(&failAtCallWriter{failAt: 1}, report, "summary", reportDir)
		if !errors.Is(err, errAtWrite) {
			t.Fatalf("writeCoverageOutputTo (headline) returned %v, want errAtWrite", err)
		}
	})
	t.Run("diagnostics", func(t *testing.T) {
		t.Parallel()
		err := writeCoverageOutputTo(&failAtCallWriter{failAt: 2}, report, "summary", reportDir)
		if !errors.Is(err, errAtWrite) {
			t.Fatalf("writeCoverageOutputTo (diagnostics) returned %v, want errAtWrite", err)
		}
	})
}
