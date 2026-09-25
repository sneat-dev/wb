package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// cwWtCmd builds a bare cobra command whose stdout is the given writer, which
// is all the print helpers in worktree.go need.
func cwWtCmd(out *bytes.Buffer) *cobra.Command {
	command := &cobra.Command{}
	command.SetOut(out)
	var errOut bytes.Buffer
	command.SetErr(&errOut)
	return command
}

func TestCwWtPrintWorktreeListStates(t *testing.T) {
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
	if err := printWorktreeList(cwWtCmd(&out), results); err != nil {
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
	if err := printWorktreeList(cwWtCmd(&out), nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "no WB worktrees\n" {
		t.Fatalf("empty list = %q", got)
	}

	// A finalized row with no report path prints "-".
	out.Reset()
	if err := printWorktreeList(cwWtCmd(&out), []worktrees.ListResult{{Task: "t", Repository: "acme/a", TerminalResult: "success"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "report=-") {
		t.Fatalf("finalized row without a report = %q", out.String())
	}

	// Every write failure is propagated.
	for allow := 0; allow < 2; allow++ {
		command := &cobra.Command{}
		command.SetOut(&cwWtFailWriter{Allow: allow})
		if err := printWorktreeList(command, results); err == nil {
			t.Fatalf("printWorktreeList with %d writes allowed returned nil", allow)
		}
	}
	command := &cobra.Command{}
	command.SetOut(&cwWtFailWriter{Allow: 0})
	if err := printWorktreeList(command, nil); err == nil {
		t.Fatal("empty printWorktreeList did not propagate the write failure")
	}
}

func TestCwWtWorktreeAgeLabel(t *testing.T) {
	if got := worktreeAgeLabel(worktrees.ListResult{}); got != "-" {
		t.Fatalf("zero age = %q", got)
	}
	if got := worktreeAgeLabel(worktrees.ListResult{AgeSeconds: -5}); got != "-" {
		t.Fatalf("negative age = %q", got)
	}
	if got := worktreeAgeLabel(worktrees.ListResult{AgeSeconds: 90}); got != "1m0s" {
		t.Fatalf("truncated age = %q", got)
	}
	if got := worktreeAgeLabel(worktrees.ListResult{AgeSeconds: 90, Expired: true}); got != "1m0s!" {
		t.Fatalf("expired age = %q", got)
	}
}

func TestCwWtPrintWorktreeSummaryBranches(t *testing.T) {
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
	if err := printWorktreeSummary(cwWtCmd(&out), "task", results, true); err != nil {
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
	if err := printWorktreeSummary(cwWtCmd(&out), "task", nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no live worktrees for this task") {
		t.Fatalf("empty summary = %q", out.String())
	}

	// A failed first write is reported.
	for allow := 0; allow < 3; allow++ {
		if err := printWorktreeSummary(cwWtCmdWriter(&cwWtFailWriter{Allow: allow}), "task", results, true); err == nil {
			t.Fatalf("printWorktreeSummary with %d writes allowed returned nil", allow)
		}
	}
}

func cwWtCmdWriter(writer *cwWtFailWriter) *cobra.Command {
	command := &cobra.Command{}
	command.SetOut(writer)
	return command
}

func TestCwWtWorktreeSummaryState(t *testing.T) {
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
		if got := worktreeSummaryState(test.result); got != test.want {
			t.Errorf("%s: worktreeSummaryState = %q, want %q", name, got, test.want)
		}
	}
}

func TestCwWtPrintWorktreeCleanupAndRename(t *testing.T) {
	cleanup := []worktrees.CleanupResult{
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/a"}, Applied: true, RemoteDeleted: true, WorktreeResidueRemoved: true},
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/b"}, Applied: true},
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/c"}, Eligible: true},
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/d"}, Reason: "not merged"},
	}
	var out bytes.Buffer
	if err := printWorktreeCleanup(cwWtCmd(&out), cleanup, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"removed t acme/a and remote branch (WB removed the checkout Git unregistered but could not delete)",
		"removed t acme/b\n", "would remove t acme/c", "skip t acme/d: not merged",
		"1 eligible; dry-run only, pass --apply to remove",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("cleanup output missing %q:\n%s", want, text)
		}
	}
	out.Reset()
	if err := printWorktreeCleanup(cwWtCmd(&out), cleanup, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2 removed") {
		t.Fatalf("cleanup apply output = %q", out.String())
	}
	out.Reset()
	if err := printWorktreeCleanup(cwWtCmd(&out), nil, true); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "no WB worktrees matched\n" {
		t.Fatalf("empty cleanup = %q", got)
	}

	rename := []worktrees.RenameResult{
		{OldTask: "old", Repository: "acme/a", NewWorktreeDir: "/tmp/new", NewBranch: "new", Applied: true, OldBranchDeleted: true, OldBranch: "old"},
		{OldTask: "old", Repository: "acme/b", NewWorktreeDir: "/tmp/new2", NewBranch: "new", Applied: true},
		{OldTask: "old", Repository: "acme/c", NewWorktreeDir: "/tmp/new3", Eligible: true},
		{OldTask: "old", Repository: "acme/d", Reason: "dirty"},
	}
	out.Reset()
	if err := printWorktreeRename(cwWtCmd(&out), rename, false); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	for _, want := range []string{
		"renamed old acme/a -> /tmp/new (new) and deleted old branch old",
		"renamed old acme/b -> /tmp/new2 (new)\n",
		"would rename old acme/c -> /tmp/new3", "skip old acme/d: dirty",
		"1 eligible; dry-run only, pass --apply to rename",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rename output missing %q:\n%s", want, text)
		}
	}
	out.Reset()
	if err := printWorktreeRename(cwWtCmd(&out), rename, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2 renamed") {
		t.Fatalf("rename apply output = %q", out.String())
	}
	out.Reset()
	if err := printWorktreeRename(cwWtCmd(&out), nil, false); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "no WB worktrees matched\n" {
		t.Fatalf("empty rename = %q", got)
	}

	// Write failures propagate in both.
	for allow := 0; allow < 3; allow++ {
		if err := printWorktreeCleanup(cwWtCmdWriter(&cwWtFailWriter{Allow: allow}), cleanup, false); err == nil {
			t.Fatalf("printWorktreeCleanup with %d writes allowed returned nil", allow)
		}
		if err := printWorktreeRename(cwWtCmdWriter(&cwWtFailWriter{Allow: allow}), rename, false); err == nil {
			t.Fatalf("printWorktreeRename with %d writes allowed returned nil", allow)
		}
	}
}

func TestCwWtPrintRetireTaskShells(t *testing.T) {
	outcome := worktrees.RetireShellsOutcome{
		Results: []worktrees.RetiredShell{
			{Task: "applied", Path: "/tmp/a", Applied: true},
			{Task: "eligible", Path: "/tmp/b", Eligible: true},
			{Task: "failed", Path: "/tmp/c", Error: "boom"},
			{Task: "skipped", Path: "/tmp/d", Reason: "still has members"},
		},
		Totals: map[string]int{"retired": 1, "would_retire": 2},
	}
	var out bytes.Buffer
	if err := printRetireTaskShells(cwWtCmd(&out), outcome); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"retired      applied /tmp/a", "would retire eligible /tmp/b",
		"failed       failed /tmp/c: boom", "skip         skipped /tmp/d: still has members",
		"2 would retire",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("shells output missing %q:\n%s", want, text)
		}
	}
	out.Reset()
	outcome.Apply = true
	if err := printRetireTaskShells(cwWtCmd(&out), outcome); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 retired") {
		t.Fatalf("shells apply output = %q", out.String())
	}
	out.Reset()
	if err := printRetireTaskShells(cwWtCmd(&out), worktrees.RetireShellsOutcome{}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "no WB task directories found\n" {
		t.Fatalf("empty shells = %q", got)
	}

	for allow := 0; allow < 2; allow++ {
		if err := printRetireTaskShells(cwWtCmdWriter(&cwWtFailWriter{Allow: allow}), outcome); err == nil {
			t.Fatalf("printRetireTaskShells with %d writes allowed returned nil", allow)
		}
	}
}

func TestCwWtRenderAdopt(t *testing.T) {
	results := []worktrees.AdoptResult{
		{Path: "/tmp/a", Task: "t", Action: worktrees.AdoptAdopted},
		{Path: "/tmp/b", Task: "t", Action: worktrees.AdoptWouldAdopt},
		{Path: "/tmp/c", Action: worktrees.AdoptSkipped, Reason: "already managed"},
	}
	var out bytes.Buffer
	if err := renderAdopt(&out, results, false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"skipped /tmp/c: already managed",
		"adopted", "/tmp/a", "would_adopt", "/tmp/b",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("adopt output missing %q:\n%s", want, text)
		}
	}
	out.Reset()
	if err := renderAdopt(&out, results, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "dry-run only") {
		t.Fatalf("adopt apply output = %q", out.String())
	}
	out.Reset()
	if err := renderAdopt(&out, nil, true); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("empty adopt output = %q", out.String())
	}

	for allow := 0; allow < 3; allow++ {
		if err := renderAdopt(&cwWtFailWriter{Allow: allow}, results, false); err == nil {
			t.Fatalf("renderAdopt with %d writes allowed returned nil", allow)
		}
	}
}

func TestCwWtRenderOrphans(t *testing.T) {
	report := worktrees.OrphanReport{
		Families: []worktrees.OrphanFamily{
			{
				RootEffort: "effort-1", Disposition: worktrees.DispositionRemove, Reason: "every worktree landed",
				Worktrees: []worktrees.OrphanWorktree{
					{Disposition: "remove", Repository: "acme/a", Branch: "b", Layout: worktrees.LayoutCurrent, HasManifest: false, Dirty: true, Missing: true, OwnerState: worktrees.OwnerLive, Evidence: []string{"landed"}},
					{Disposition: "remove", Repository: "acme/b", Branch: "b", Layout: worktrees.LayoutCurrent, HasManifest: true, Provenance: "reconstructed", OwnerState: worktrees.OwnerGone},
					{Disposition: "remove", Repository: "acme/c", Branch: "b", Layout: worktrees.LayoutLegacy, HasManifest: true, OwnerState: ""},
				},
			},
			{RootEffort: "effort-2", Disposition: worktrees.DispositionReview, Reason: "needs a look"},
		},
		Residue: []worktrees.OrphanResidue{
			{Task: "task", Repository: "acme/a", Layout: worktrees.LayoutLocal, Evidence: []string{"unregistered"}, Remedy: "wb worktree gc"},
		},
		Totals: worktrees.OrphanTotals{
			Worktrees: 4, Families: 2,
			ByLayout:    map[string]int{worktrees.LayoutCurrent: 2, worktrees.LayoutLegacy: 1, worktrees.LayoutExternal: 1},
			ByDispositn: map[string]int{worktrees.DispositionRemove: 1, worktrees.DispositionReview: 1},
			NoManifest:  1, Dirty: 1, Residue: 1,
		},
		Unscanned: []string{"/tmp/unreadable"},
	}
	var out bytes.Buffer
	if err := renderOrphans(&out, report, ""); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"effort-1 [remove] every worktree landed",
		"no-manifest", "reconstructed", "dirty", "missing", "owner live", "owner gone", "owner unstated",
		"- landed", "unregistered checkouts (1)", "wb worktree gc",
		"4 worktrees in 2 efforts (2 shown): 2 current, 1 legacy, 1 external; 1 without a manifest, 1 dirty",
		"unscanned: /tmp/unreadable",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("orphans output missing %q:\n%s", want, text)
		}
	}

	// --only filters both families and the residue section.
	out.Reset()
	if err := renderOrphans(&out, report, worktrees.DispositionRemove); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	if strings.Contains(text, "effort-2") || strings.Contains(text, "unregistered checkouts") {
		t.Fatalf("--only=remove output = %q", text)
	}
	out.Reset()
	if err := renderOrphans(&out, report, worktrees.DispositionReview); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	if strings.Contains(text, "effort-1") || !strings.Contains(text, "unregistered checkouts") {
		t.Fatalf("--only=review output = %q", text)
	}

	// A report with no families and no residue still prints the footer.
	out.Reset()
	if err := renderOrphans(&out, worktrees.OrphanReport{}, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(0 shown)") {
		t.Fatalf("empty orphans output = %q", out.String())
	}

	for allow := 0; allow < 3; allow++ {
		if err := renderOrphans(&cwWtFailWriter{Allow: allow}, report, ""); err == nil {
			t.Fatalf("renderOrphans with %d writes allowed returned nil", allow)
		}
	}
}

func TestCwWtFormatCanonicalFreshness(t *testing.T) {
	if got := formatCanonicalFreshness(nil); got != "not checked" {
		t.Fatalf("nil freshness = %q", got)
	}
	withError := &worktrees.CanonicalFreshness{Status: worktrees.CanonicalFreshnessOffline, RemoteRef: "origin/main", Error: "network down"}
	if got := formatCanonicalFreshness(withError); !strings.Contains(got, "status=") || !strings.Contains(got, "network down") {
		t.Fatalf("error freshness = %q", got)
	}
	fresh := &worktrees.CanonicalFreshness{
		Status: worktrees.CanonicalFreshnessCurrent, RemoteRef: "origin/main",
		LocalSHA: "aaaa", RemoteSHA: "bbbb", Ahead: 1, Behind: 2,
	}
	got := formatCanonicalFreshness(fresh)
	for _, want := range []string{"status=current", "target=origin/main", "local=aaaa", "remote=bbbb", "(1 ahead, 2 behind)"} {
		if !strings.Contains(got, want) {
			t.Errorf("freshness text missing %q: %s", want, got)
		}
	}
}

func TestCwWtReadPromptBody(t *testing.T) {
	directory := t.TempDir()
	promptFile := filepath.Join(directory, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("  from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyFile := filepath.Join(directory, "empty.txt")
	if err := os.WriteFile(emptyFile, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readPromptBody("", ""); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("neither source = %v", err)
	}
	if _, err := readPromptBody("inline", promptFile); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("both sources = %v", err)
	}
	if body, err := readPromptBody("inline", ""); err != nil || string(body) != "inline" {
		t.Fatalf("inline body = (%q, %v)", body, err)
	}
	if _, err := readPromptBody("   ", ""); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty inline body = %v", err)
	}
	if body, err := readPromptBody("", promptFile); err != nil || string(body) != "  from a file\n" {
		t.Fatalf("file body = (%q, %v)", body, err)
	}
	if _, err := readPromptBody("", filepath.Join(directory, "missing.txt")); err == nil || !strings.Contains(err.Error(), "read prompt file") {
		t.Fatalf("missing file = %v", err)
	}
	if _, err := readPromptBody("", emptyFile); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("empty file = %v", err)
	}
}

func TestCwWtEncodeLogVerbResult(t *testing.T) {
	result := worktrees.LogVerbResult{
		Verb: "steer", Worktree: "/tmp/wt", Applied: true, Prompt: "prompt-1",
		Event:   &worktrees.LocalWorkLogEvent{Type: "prompt_recorded", Seq: 3},
		Offline: true, Outbox: 2,
		Notes: []string{"note one"}, Diagnosis: []string{"line one"},
	}
	var out bytes.Buffer
	if err := encodeLogVerbResult(cwWtCmd(&out), "text", result); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"steer /tmp/wt applied=true", "prompt=prompt-1",
		"event=prompt_recorded#3", "offline outbox=2",
		"- note one", "diagnosis: line one",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("log verb text missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	if err := encodeLogVerbResult(cwWtCmd(&out), "json", result); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("log verb JSON: %v\n%s", err, out.String())
	}
	if decoded["verb"] != "steer" || decoded["prompt"] != "prompt-1" {
		t.Fatalf("log verb JSON = %+v", decoded)
	}

	// A minimal result writes only the header line.
	out.Reset()
	if err := encodeLogVerbResult(cwWtCmd(&out), "text", worktrees.LogVerbResult{Verb: "sync"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "sync  applied=false\n" {
		t.Fatalf("minimal log verb = %q", got)
	}

	for allow := 0; allow < 7; allow++ {
		if err := encodeLogVerbResult(cwWtCmdWriter(&cwWtFailWriter{Allow: allow}), "text", result); err == nil {
			t.Fatalf("encodeLogVerbResult with %d writes allowed returned nil", allow)
		}
	}
}

func TestCwWtWorktreeLogPath(t *testing.T) {
	if got := worktreeLogPath(nil); got != "." {
		t.Fatalf("no arg = %q", got)
	}
	if got := worktreeLogPath([]string{"/tmp/wt"}); got != "/tmp/wt" {
		t.Fatalf("one arg = %q", got)
	}
}

func TestCwWtValidateWorktreeBranchFlags(t *testing.T) {
	build := func(args ...string) *cobra.Command {
		command := &cobra.Command{Use: "x", Args: cobra.ExactArgs(2)}
		var branch string
		command.Flags().StringVar(&branch, "branch", "", "")
		command.Flags().StringVar(new(string), "branch-prefix", "", "")
		command.SetArgs(args)
		_ = command.ParseFlags(args)
		return command
	}
	if err := validateWorktreeBranchFlags(build(), ""); err != nil {
		t.Fatalf("no flags = %v", err)
	}
	if err := validateWorktreeBranchFlags(build("--branch", "b"), "b"); err != nil {
		t.Fatalf("branch only = %v", err)
	}
	if err := validateWorktreeBranchFlags(build("--branch", "b", "--branch-prefix", "p"), "b"); err == nil {
		t.Fatal("both flags must be refused")
	}
	command := build("--branch=")
	if err := validateWorktreeBranchFlags(command, ""); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("explicitly empty branch = %v", err)
	}
}

func TestCwWtRequireOutputFormat(t *testing.T) {
	if err := requireOutputFormat("json", "text", "json"); err != nil {
		t.Fatalf("allowed value = %v", err)
	}
	err := requireOutputFormat("yaml", "text", "json")
	if err == nil || !strings.Contains(err.Error(), "text or json") {
		t.Fatalf("refused value = %v", err)
	}
}

func TestCwWtCleanupTaskNameHelpers(t *testing.T) {
	outcome := worktrees.CleanupOutcome{
		Results: []worktrees.CleanupResult{
			{ListResult: worktrees.ListResult{Task: "alpha"}, Applied: true},
			{ListResult: worktrees.ListResult{Task: "beta"}},
		},
		Artifacts: []worktrees.LifecycleArtifact{{Task: "gamma", Applied: true}},
	}
	applied := appliedCleanupTaskNames(outcome)
	if !applied["alpha"] || applied["beta"] || !applied["gamma"] {
		t.Fatalf("applied task names = %v", applied)
	}

	if !namedCleanupApplySatisfied([]string{"alpha"}, outcome) {
		t.Fatal("a single applied task must satisfy the check")
	}
	if namedCleanupApplySatisfied([]string{"beta"}, outcome) {
		t.Fatal("an unapplied task must not satisfy the check")
	}
	if !namedCleanupApplySatisfied([]string{"alpha", "beta"}, outcome) {
		t.Fatal("multi-task selections are always satisfied")
	}
	// An empty resolution falls back to the requested name.
	if namedCleanupApplySatisfied([]string{"alpha"}, worktrees.CleanupOutcome{}) {
		t.Fatal("an empty outcome must not satisfy a named apply")
	}
	if namedCleanupApplySatisfied(nil, worktrees.CleanupOutcome{}) != true {
		t.Fatal("an empty selection is trivially satisfied")
	}
	// A resolved identity that differs from the selector is judged by the
	// resolved name.
	resolved := worktrees.CleanupOutcome{
		ResolvedTasks: []string{"session-resume-1"},
		Results:       []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Task: "session-resume-1"}, Applied: true}},
	}
	if !namedCleanupApplySatisfied([]string{"logical-effort"}, resolved) {
		t.Fatal("a resolved identity that applied must satisfy the check")
	}
}

func TestCwWtWorktreeCmdsRejectBadFormatInProcess(t *testing.T) {
	projects := t.TempDir()
	builders := map[string]func() *cobra.Command{
		"log-show":    newWorktreeLogShowCmd,
		"log-refresh": newWorktreeLogRefreshCmd,
		"backfill":    newWorktreeBackfillCmd,
		"adopt":       newWorktreeAdoptCmd,
		"orphans":     newWorktreeOrphansCmd,
		"list":        newWorktreeListCmd,
		"marker":      newWorktreeMarkerCmd,
		"relocate":    newWorktreeRelocateCmd,
		"checkpoint":  newWorktreeCheckpointFetchCmd,
		"rescue":      newWorktreeRescueCmd,
		"end":         newWorktreeEndCmd,
		"gc":          func() *cobra.Command { return newWorktreeGCCmd(&invocation{}) },
		"cleanup":     func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{}) },
	}
	arguments := map[string][]string{
		"checkpoint":  {"--task", "t"},
		"relocate":    {"t"},
		"rescue":      {"."},
		"end":         {"t"},
		"cleanup":     {"t"},
		"marker":      {"."},
		"adopt":       {"."},
		"log-show":    {"."},
		"log-refresh": {"."},
	}
	for name, build := range builders {
		args := append(append([]string{}, arguments[name]...), "--format", "bogus")
		_, _, err := cwCovExec(t, projects, build, args...)
		if err == nil || !strings.Contains(err.Error(), "unsupported format") {
			t.Errorf("%s --format bogus error = %v", name, err)
		}
	}
}

func TestCwWtWorktreeCleanupArgValidation(t *testing.T) {
	projects := t.TempDir()
	cases := []struct {
		args []string
		want string
	}{
		{args: nil, want: "supply one or more tasks or use --all-merged"},
		{args: []string{"--recover-stages"}, want: "--recover-stages requires one or more named tasks"},
		{args: []string{"--recover-stages", "--retire-shells", "t"}, want: "--recover-stages cannot be combined"},
		{args: []string{"--retire-shells", "t"}, want: "--retire-shells sweeps every task"},
		{args: []string{"--retire-shells", "--all-merged"}, want: "--retire-shells and --all-merged cannot be combined"},
		{args: []string{"t", "--all-merged"}, want: "tasks and --all-merged cannot be combined"},
		{args: []string{"t", "--resume-interrupted", "--apply", "extra"}, want: "--resume-interrupted requires one explicit task"},
	}
	for _, test := range cases {
		_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{}) }, test.args...)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("cleanup %v error = %v, want %q", test.args, err, test.want)
		}
	}
}

func TestCwWtWorktreeListAndSummaryInProcess(t *testing.T) {
	projects := cwCovProjectsRoot(t, "acme/app")
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	stdout, _, err := cwCovExec(t, projects, newWorktreeListCmd)
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if !strings.Contains(stdout, "no WB worktrees") {
		t.Fatalf("worktree list stdout = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, newWorktreeListCmd, "--format", "json")
	if err != nil {
		t.Fatalf("worktree list json: %v", err)
	}
	if !strings.Contains(stdout, "\"results\"") {
		t.Fatalf("worktree list json stdout = %q", stdout)
	}

	_, _, err = cwCovExec(t, projects, newWorktreeListCmd, "--finalized", "--not-finalized")
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("conflicting finalize filters = %v", err)
	}

	stdout, _, err = cwCovExec(t, projects, newWorktreeSummaryCmd, "absent-task")
	if err != nil {
		t.Fatalf("worktree summary: %v", err)
	}
	if !strings.Contains(stdout, "no live worktrees for this task") {
		t.Fatalf("worktree summary stdout = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, newWorktreeSummaryCmd, "absent-task", "--format", "json")
	if err != nil {
		t.Fatalf("worktree summary json: %v", err)
	}
	if !strings.Contains(stdout, "\"results\"") {
		t.Fatalf("worktree summary json stdout = %q", stdout)
	}
}

func TestCwWtWorktreeBackfillAdoptOrphansInProcess(t *testing.T) {
	projects := cwCovProjectsRoot(t, "acme/app")
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	stdout, _, err := cwCovExec(t, projects, newWorktreeBackfillCmd)
	if err != nil {
		t.Fatalf("backfill dry run: %v", err)
	}
	if !strings.Contains(stdout, "dry-run only, pass --apply to write") {
		t.Fatalf("backfill stdout = %q", stdout)
	}
	stdout, _, err = cwCovExec(t, projects, newWorktreeBackfillCmd, "--apply")
	if err != nil {
		t.Fatalf("backfill apply: %v", err)
	}
	if strings.Contains(stdout, "dry-run only") {
		t.Fatalf("backfill apply stdout = %q", stdout)
	}
	if _, _, err := cwCovExec(t, projects, newWorktreeBackfillCmd, "--format", "json"); err != nil {
		t.Fatalf("backfill json: %v", err)
	}

	// adopt requires exactly one selector.
	if _, _, err := cwCovExec(t, projects, newWorktreeAdoptCmd, "--apply"); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("adopt with no selector = %v", err)
	}
	if _, _, err := cwCovExec(t, projects, newWorktreeAdoptCmd, ".", "--all-external"); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("adopt with both selectors = %v", err)
	}
	stdout, _, err = cwCovExec(t, projects, newWorktreeAdoptCmd, "--all-external")
	if err != nil {
		t.Fatalf("adopt dry run: %v", err)
	}
	if !strings.Contains(stdout, "dry-run only, pass --apply to write") {
		t.Fatalf("adopt stdout = %q", stdout)
	}
	if _, _, err = cwCovExec(t, projects, newWorktreeAdoptCmd, "--all-external", "--format", "json"); err != nil {
		t.Fatalf("adopt json: %v", err)
	}

	stdout, _, err = cwCovExec(t, projects, newWorktreeOrphansCmd)
	if err != nil {
		t.Fatalf("orphans: %v", err)
	}
	if !strings.Contains(stdout, "worktrees in") {
		t.Fatalf("orphans stdout = %q", stdout)
	}
	if _, _, err = cwCovExec(t, projects, newWorktreeOrphansCmd, "--format", "json"); err != nil {
		t.Fatalf("orphans json: %v", err)
	}
	if _, _, err = cwCovExec(t, projects, newWorktreeOrphansCmd, "--only", "remove"); err != nil {
		t.Fatalf("orphans --only: %v", err)
	}
}

func TestCwWtWorktreeRenameInProcess(t *testing.T) {
	projects := cwCovProjectsRoot(t, "acme/app")
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	// An old task that does not exist is refused with the backend's reason.
	_, _, err := cwCovExec(t, projects, newWorktreeRenameCmd, "absent-old", "absent-new", "--model", "unknown")
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("rename of an absent task = %v", err)
	}
	if _, _, err = cwCovExec(t, projects, newWorktreeRenameCmd, "absent-old", "absent-new", "--model", "unknown", "--format", "json"); err == nil {
		t.Fatal("rename json of an absent task must fail")
	}
	// --branch and --branch-prefix together are refused before the backend.
	if _, _, err = cwCovExec(t, projects, newWorktreeRenameCmd, "a", "b", "--branch", "x", "--branch-prefix", "y"); err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("rename --branch with --branch-prefix = %v", err)
	}
	// --model is required for the new Work Log claim.
	if _, _, err = cwCovExec(t, projects, newWorktreeRenameCmd, "a", "b"); err == nil || !strings.Contains(err.Error(), "--model is required") {
		t.Fatalf("rename without --model = %v", err)
	}
}

func TestCwWtWorktreeRenameRealTaskInProcess(t *testing.T) {
	projects, _, _ := initGCFixture(t)
	// The fixture worktree is dirty, so the dry-run plans a skip whose reason
	// is printed; either way the text renderer runs.
	stdout, _, err := cwCovExec(t, projects, newWorktreeRenameCmd, "gc-cli", "gc-cli-renamed", "--model", "unknown")
	if err != nil && exitCodeOf(t, err) != exitFindings {
		t.Fatalf("rename dry run of a real task: %v", err)
	}
	if !strings.Contains(stdout, "gc-cli") {
		t.Fatalf("rename dry-run stdout = %q", stdout)
	}
	if _, _, err := cwCovExec(t, projects, newWorktreeRenameCmd, "gc-cli", "gc-cli-renamed", "--model", "unknown", "--format", "json"); err != nil {
		t.Fatalf("rename json of a real task: %v", err)
	}
}

func TestCwWtWorktreeRelocateInProcess(t *testing.T) {
	projects := cwCovProjectsRoot(t, "acme/app")
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	_, _, err := cwCovExec(t, projects, newWorktreeRelocateCmd, "absent-task", "--to", "local")
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("relocate of an absent task = %v", err)
	}
	// An invalid destination is refused before the backend.
	if _, _, err = cwCovExec(t, projects, newWorktreeRelocateCmd, "absent-task", "--to", "elsewhere"); err == nil {
		t.Fatal("relocate with an invalid --to must fail")
	}
	if _, _, err := cwCovExec(t, projects, newWorktreeRelocateCmd, "absent-task", "--to", "local", "--json", "--format", "text"); err == nil {
		t.Fatal("--json with a conflicting --format must be refused")
	}
}

func TestCwWtWorktreeRelocateRealTaskInProcess(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	// The fixture task starts dirty, and relocate refuses a dirty checkout, so
	// clean it: then the repository-local layout reports it as already there.
	if err := os.Remove(filepath.Join(worktree, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := cwCovExec(t, projects, newWorktreeRelocateCmd, "gc-cli", "--to", "local")
	if err != nil {
		t.Fatalf("relocate dry run of a real task: %v", err)
	}
	if !strings.Contains(stdout, "already there") && !strings.Contains(stdout, "would relocate") {
		t.Fatalf("relocate dry-run stdout = %q", stdout)
	}
	stdout, _, err = cwCovExec(t, projects, newWorktreeRelocateCmd, "gc-cli", "--to", "local", "--json")
	if err != nil {
		t.Fatalf("relocate json of a real task: %v", err)
	}
	if !strings.Contains(stdout, "schema_version") {
		t.Fatalf("relocate json stdout = %q", stdout)
	}
}

func TestCwWtWorktreeGuardAndCheckpointFetchInProcess(t *testing.T) {
	seed := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seed, "app", clone)
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	stdout, _, err := cwCovExec(t, projects, newWorktreeGuardCmd, clone)
	if err != nil {
		t.Fatalf("guard on a clean canonical clone: %v", err)
	}
	if !strings.Contains(stdout, "ok: ") {
		t.Fatalf("guard stdout = %q", stdout)
	}
	stdout, _, err = cwCovExec(t, projects, newWorktreeGuardCmd, clone, "--format", "json")
	if err != nil {
		t.Fatalf("guard json: %v", err)
	}
	if !strings.Contains(stdout, "\"kind\"") {
		t.Fatalf("guard json stdout = %q", stdout)
	}
	stdout, _, err = cwCovExec(t, projects, newWorktreeGuardCmd, clone, "--quiet")
	if err != nil {
		t.Fatalf("guard --quiet: %v", err)
	}
	if stdout != "" {
		t.Fatalf("guard --quiet stdout = %q", stdout)
	}
	if _, _, err := cwCovExec(t, projects, newWorktreeGuardCmd, clone, "--admission", "bogus"); err == nil || !strings.Contains(err.Error(), "unsupported admission mode") {
		t.Fatalf("guard bad admission = %v", err)
	}
	if _, _, err := cwCovExec(t, projects, newWorktreeGuardCmd, clone, "--published"); err != nil {
		// A branch pushed to this local bare origin is verified; either way
		// the code path through PublicationFinding must not crash.
		if !strings.Contains(err.Error(), "not verified as published") {
			t.Fatalf("guard --published error = %v", err)
		}
	}

	// checkpoint-fetch needs a --task and an explicit format.
	if _, _, err := cwCovExec(t, projects, newWorktreeCheckpointFetchCmd, clone); err == nil || !strings.Contains(err.Error(), "--task is required") {
		t.Fatalf("checkpoint-fetch without --task = %v", err)
	}
	_, _, err = cwCovExec(t, projects, newWorktreeCheckpointFetchCmd, clone, "--task", "t")
	if err == nil {
		t.Fatal("checkpoint-fetch for an absent ref must fail")
	}
}

func TestCwWtWorktreeSetAndLogVerbsInProcess(t *testing.T) {
	projects := t.TempDir()
	checkout := cwWtGitRepo(t, filepath.Join(projects, "acme", "app"))
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	// set requires a prompt source and refuses an empty one.
	if _, _, err := cwCovExec(t, projects, newWorktreeSetCmd, checkout); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("set without a source = %v", err)
	}
	if _, _, err := cwCovExec(t, projects, newWorktreeSetCmd, checkout, "--prompt", "   "); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("set with a blank prompt = %v", err)
	}

	// log show on a path that does not exist reports the backend error.
	_, _, err := cwCovExec(t, projects, newWorktreeLogShowCmd, filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("log show on a missing path must fail")
	}
	// log show on a real checkout with no journal is a valid, empty read.
	if _, _, err := cwCovExec(t, projects, newWorktreeLogShowCmd, checkout); err != nil {
		t.Fatalf("log show on a clean checkout: %v", err)
	}

	// log init on a plain git checkout is the first step that can succeed.
	// The admission flags live on the parent `worktree log` command, so the
	// whole subcommand tree is exercised.
	stdout, _, err := cwCovExec(t, projects, newWorktreeWorkLogCmd, "init", checkout, "--format", "json")
	if err != nil {
		t.Fatalf("log init: %v", err)
	}
	if !strings.Contains(stdout, "\"verb\"") {
		t.Fatalf("log init stdout = %q", stdout)
	}
}

func TestCwWtWorktreeErrorPropagationFromBackend(t *testing.T) {
	// A projects root beneath a regular file cannot be resolved, which drives
	// the "backend failed" branches without any fixture.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadableRoot := filepath.Join(blocker, "projects")
	builders := []struct {
		name  string
		build func() *cobra.Command
		args  []string
	}{
		{name: "list", build: newWorktreeListCmd},
		{name: "summary", build: newWorktreeSummaryCmd, args: []string{"t"}},
		{name: "backfill", build: newWorktreeBackfillCmd},
		{name: "orphans", build: newWorktreeOrphansCmd},
		{name: "gc", build: func() *cobra.Command { return newWorktreeGCCmd(&invocation{}) }},
		{name: "cleanup", build: func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{}) }, args: []string{"t"}},
		{name: "rename", build: newWorktreeRenameCmd, args: []string{"a", "b"}},
		{name: "relocate", build: newWorktreeRelocateCmd, args: []string{"t"}},
		{name: "adopt", build: newWorktreeAdoptCmd, args: []string{"--all-external"}},
	}
	for _, builder := range builders {
		if _, _, err := cwCovExec(t, unreadableRoot, builder.build, builder.args...); err == nil {
			t.Errorf("%s against an unreadable projects root returned no error", builder.name)
		}
	}
}
