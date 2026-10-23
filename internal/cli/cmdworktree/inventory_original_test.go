package cmdworktree

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/worktrees"
	"strings"
	"testing"
	"time"
)

func TestCwWtPrintWorktreeListStates(t *testing.T) {
	t.Parallel()
	mergedAt := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	results := []worktrees.ListResult{
		{Task: "t", Repository: "acme/a", Branch: "b", TerminalResult: "success", ReportPath: "/tmp/report.md", Owner: "agent-1", AgeSeconds: 120, Clean: true},
		{Task: "t", Repository: "acme/b", Branch: "b", TerminalResult: "failure", Clean: true},
		{Task: "t", Repository: "acme/c", Branch: "b", Clean: false},
		{Task: "t", Repository: "acme/d", Branch: "b", Clean: true, Locked: true},
		{Task: "t", Repository: "acme/e", Branch: "b", Clean: true, OpenPullRequest: &worktrees.PullRequest{Number: 7, URL: "https://example.test/pr/7", Merged: &mergedAt}},
		{Task: "t", Repository: "acme/f", Branch: "b", Clean: true, AbsorbedAtOrigin: true},
		{Task: "t", Repository: "acme/g", Branch: "b", Clean: true, MergedPullRequest: &worktrees.PullRequest{Number: 8, URL: "https://example.test/pr/8"}},
		{Task: "t", Repository: "acme/h", Branch: "b", Clean: true, LocallyMerged: true},
		{Task: "t", Repository: "acme/i", Branch: "", Clean: true, Detached: true, Expired: true, AgeSeconds: 3600},
	}
	var out bytes.Buffer
	if err := writeInventoryList(&out, results); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"finalized-success", "report=/tmp/report.md", "age=2m0s",
		"finalized-failure", " dirty ", " locked ",
		"open-pr", "https://example.test/pr/7",
		"absorbed", "merged", "https://example.test/pr/8",
		"locally-merged", "DETACHED", "detached", "age=1h0m0s!",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("list output missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	if err := writeInventoryList(&out, nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "no WB worktrees\n" {
		t.Fatalf("empty list = %q", got)
	}

	// A finalized row with no report path prints "-".
	out.Reset()
	if err := writeInventoryList(&out, []worktrees.ListResult{{Task: "t", Repository: "acme/a", TerminalResult: "success"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "report=-") {
		t.Fatalf("finalized row without a report = %q", out.String())
	}

	// Every write failure is propagated.
	for allow := 0; allow < 2; allow++ {
		writer := &activeLimitedWriter{Allow: allow}
		if err := writeInventoryList(writer, results); err == nil {
			t.Fatalf("printWorktreeList with %d writes allowed returned nil", allow)
		}
	}
	writer := &activeLimitedWriter{Allow: 0}
	if err := writeInventoryList(writer, nil); err == nil {
		t.Fatal("empty printWorktreeList did not propagate the write failure")
	}
}

func TestCwWtWorktreeAgeLabel(t *testing.T) {
	t.Parallel()
	if got := inventoryAgeLabel(worktrees.ListResult{}); got != "-" {
		t.Fatalf("zero age = %q", got)
	}
	if got := inventoryAgeLabel(worktrees.ListResult{AgeSeconds: -5}); got != "-" {
		t.Fatalf("negative age = %q", got)
	}
	if got := inventoryAgeLabel(worktrees.ListResult{AgeSeconds: 90}); got != "1m0s" {
		t.Fatalf("truncated age = %q", got)
	}
	if got := inventoryAgeLabel(worktrees.ListResult{AgeSeconds: 90, Expired: true}); got != "1m0s!" {
		t.Fatalf("expired age = %q", got)
	}
}

func TestCwWtPrintWorktreeSummaryBranches(t *testing.T) {
	t.Parallel()
	results := []worktrees.ListResult{
		{
			Repository: "acme/a", WorktreeDir: "/tmp/wt/a", Branch: "task/a", HeadSHA: strings.Repeat("a", 40),
			Base: "main", IntegratedAtOrigin: true, Clean: true,
			TerminalResult: "success", TerminalMessage: "done", FinalizedAt: time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC),
			ReportPath: "/tmp/report.md", OpenPullRequest: &worktrees.PullRequest{Number: 3, URL: "https://example.test/pr/3"},
		},
		{Repository: "acme/b", Branch: "task/b", HeadSHA: "short", Base: "main", AbsorbedAtOrigin: true, Clean: true, MergedPullRequest: &worktrees.PullRequest{Number: 4, URL: "https://example.test/pr/4"}},
		{Repository: "acme/c", Branch: "task/c", Base: "main", RebaseMergedAtOrigin: true, Clean: true},
		{Repository: "acme/d", Branch: "task/d", Base: "main", LocallyMerged: true, Clean: true},
		{Repository: "acme/e", Branch: "task/e", Base: "main", Clean: false, Locked: true},
	}
	var out bytes.Buffer
	if err := writeInventorySummary(&out, "task", results, true); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"# WB worktree summary: task", "5 worktree(s)",
		"head:     " + strings.Repeat("a", 12),
		"finalize: success at 2025-03-04T05:06:07Z", "message:  done", "report:   /tmp/report.md",
		"pr:       open #3",
		"integrated at origin/main", "absorbed at origin/main",
		"rebase-merged at origin/main", "locally merged; awaiting push",
		"not integrated at origin/main", "dirty,locked,active",
		"pr:       merged #4",
		"pr:       none",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("summary output missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	if err := writeInventorySummary(&out, "task", nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no live worktrees for this task") {
		t.Fatalf("empty summary = %q", out.String())
	}

	// A failed first write is reported.
	for allow := 0; allow < 3; allow++ {
		if err := writeInventorySummary(&activeLimitedWriter{Allow: allow}, "task", results, true); err == nil {
			t.Fatalf("printWorktreeSummary with %d writes allowed returned nil", allow)
		}
	}
}

func TestCwWtWorktreeSummaryState(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		result worktrees.ListResult
		want   string
	}{
		"active":           {worktrees.ListResult{Clean: true}, "clean,active"},
		"dirty":            {worktrees.ListResult{Clean: false}, "dirty,active"},
		"finalized":        {worktrees.ListResult{Clean: true, TerminalResult: "success"}, "finalized-success,clean,active"},
		"locked":           {worktrees.ListResult{Clean: true, Locked: true}, "clean,locked,active"},
		"open-pr":          {worktrees.ListResult{Clean: true, OpenPullRequest: &worktrees.PullRequest{}}, "clean,open-pr"},
		"absorbed":         {worktrees.ListResult{Clean: true, AbsorbedAtOrigin: true}, "clean,absorbed"},
		"merged":           {worktrees.ListResult{Clean: true, MergedPullRequest: &worktrees.PullRequest{}}, "clean,merged"},
		"locally-merged":   {worktrees.ListResult{Clean: true, LocallyMerged: true}, "clean,locally-merged"},
		"dirty-and-locked": {worktrees.ListResult{Clean: false, Locked: true, LocallyMerged: true}, "dirty,locked,locally-merged"},
	}
	for name, test := range cases {
		if got := inventorySummaryState(test.result); got != test.want {
			t.Errorf("%s: worktreeSummaryState = %q, want %q", name, got, test.want)
		}
	}
}

func TestCwWtWriterSweepListAndSummary(t *testing.T) {
	t.Parallel()
	results := []worktrees.ListResult{
		{Task: "t", Repository: "acme/a", Branch: "b", Clean: true, TerminalResult: "success", ReportPath: "/tmp/r", Owner: "o", AgeSeconds: 60},
		{Task: "t", Repository: "acme/b", Branch: "b", Clean: false},
		{Task: "t", Repository: "acme/c", Branch: "b", Clean: true, Locked: true},
		{Task: "t", Repository: "acme/d", Branch: "b", Clean: true, OpenPullRequest: &worktrees.PullRequest{Number: 1, URL: "u"}},
		{Task: "t", Repository: "acme/e", Branch: "b", Clean: true, AbsorbedAtOrigin: true},
		{Task: "t", Repository: "acme/f", Branch: "b", Clean: true, MergedPullRequest: &worktrees.PullRequest{Number: 2, URL: "u"}},
		{Task: "t", Repository: "acme/g", Branch: "b", Clean: true, LocallyMerged: true},
		{Task: "t", Repository: "acme/h", Branch: "", Clean: true, Detached: true, Expired: true, AgeSeconds: 90},
	}
	activeSweepWrites(t, 12, func(writer *activeLimitedWriter) error {
		return writeInventoryList(writer, results)
	})

	summary := []worktrees.ListResult{
		{
			Repository: "acme/a", WorktreeDir: "/tmp/a", Branch: "b", HeadSHA: strings.Repeat("a", 40), Base: "main",
			IntegratedAtOrigin: true, Clean: true, TerminalResult: "success", TerminalMessage: "done",
			FinalizedAt: time.Now().UTC(), ReportPath: "/tmp/r",
			OpenPullRequest: &worktrees.PullRequest{Number: 1, URL: "u"},
		},
		{Repository: "acme/b", Branch: "b", Base: "main", AbsorbedAtOrigin: true, Clean: true, MergedPullRequest: &worktrees.PullRequest{Number: 2, URL: "u"}},
		{Repository: "acme/c", Branch: "b", Base: "main", RebaseMergedAtOrigin: true, Clean: true},
		{Repository: "acme/d", Branch: "b", Base: "main", LocallyMerged: true, Clean: true},
		{Repository: "acme/e", Branch: "b", Base: "main", Clean: false, Locked: true},
	}
	// Five rows at roughly twenty writes each, plus the header and spacer.
	activeSweepWrites(t, 60, func(writer *activeLimitedWriter) error {
		return writeInventorySummary(writer, "task", summary, true)
	})
}
