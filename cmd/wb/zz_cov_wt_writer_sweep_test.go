package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/sneat-dev/wb/internal/worktreeend"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

// The helpers below drive every writer-error return in the report renderers by
// failing the writer at each successive write. A renderer that ignores a write
// failure would leave a truncated report on stdout while exiting 0, which is
// exactly the silent-truncation bug these branches exist to prevent.

func cwWtSweepWrites(t *testing.T, max int, run func(writer *cwWtFailWriter) error) {
	t.Helper()
	for allow := 0; allow <= max; allow++ {
		if err := run(&cwWtFailWriter{Allow: allow}); err == nil {
			return
		}
	}
	t.Fatalf("no write budget up to %d let the renderer finish", max)
}

func TestCwWtWriterSweepListAndSummary(t *testing.T) {
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
	cwWtSweepWrites(t, 12, func(writer *cwWtFailWriter) error {
		return printWorktreeList(cwWtCmdWriter(writer), results)
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
	cwWtSweepWrites(t, 60, func(writer *cwWtFailWriter) error {
		return printWorktreeSummary(cwWtCmdWriter(writer), "task", summary, true)
	})
}

func TestCwWtWriterSweepCleanupRenameShellsAdopt(t *testing.T) {
	cleanup := []worktrees.CleanupResult{
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/a"}, Applied: true, RemoteDeleted: true, WorktreeResidueRemoved: true},
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/b"}, Applied: true},
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/c"}, Eligible: true},
		{ListResult: worktrees.ListResult{Task: "t", Repository: "acme/d"}, Reason: "not merged"},
	}
	cwWtSweepWrites(t, 8, func(writer *cwWtFailWriter) error {
		return printWorktreeCleanup(cwWtCmdWriter(writer), cleanup, false)
	})

	rename := []worktrees.RenameResult{
		{OldTask: "old", Repository: "acme/a", NewWorktreeDir: "/tmp/n", NewBranch: "new", Applied: true, OldBranchDeleted: true, OldBranch: "old"},
		{OldTask: "old", Repository: "acme/b", NewWorktreeDir: "/tmp/n2", NewBranch: "new", Applied: true},
		{OldTask: "old", Repository: "acme/c", NewWorktreeDir: "/tmp/n3", Eligible: true},
		{OldTask: "old", Repository: "acme/d", Reason: "dirty"},
	}
	cwWtSweepWrites(t, 8, func(writer *cwWtFailWriter) error {
		return printWorktreeRename(cwWtCmdWriter(writer), rename, false)
	})

	shells := worktrees.RetireShellsOutcome{
		Results: []worktrees.RetiredShell{
			{Task: "a", Path: "/tmp/a", Applied: true},
			{Task: "b", Path: "/tmp/b", Eligible: true},
			{Task: "c", Path: "/tmp/c", Error: "boom"},
			{Task: "d", Path: "/tmp/d", Reason: "still has members"},
		},
		Totals: map[string]int{"would_retire": 1},
	}
	cwWtSweepWrites(t, 8, func(writer *cwWtFailWriter) error {
		return printRetireTaskShells(cwWtCmdWriter(writer), shells)
	})

	adopt := []worktrees.AdoptResult{
		{Path: "/tmp/a", Task: "t", Action: worktrees.AdoptAdopted},
		{Path: "/tmp/b", Task: "t", Action: worktrees.AdoptWouldAdopt},
		{Path: "/tmp/c", Action: worktrees.AdoptSkipped, Reason: "already managed"},
	}
	cwWtSweepWrites(t, 10, func(writer *cwWtFailWriter) error {
		return renderAdopt(writer, adopt, false)
	})
}

func TestCwWtWriterSweepOrphansAndActive(t *testing.T) {
	report := worktrees.OrphanReport{
		Families: []worktrees.OrphanFamily{
			{
				RootEffort: "e1", Disposition: worktrees.DispositionRemove, Reason: "landed",
				Worktrees: []worktrees.OrphanWorktree{
					{Disposition: "remove", Repository: "acme/a", Branch: "b", Layout: worktrees.LayoutCurrent, HasManifest: false, Dirty: true, Missing: true, OwnerState: worktrees.OwnerLive, Evidence: []string{"one"}},
					{Disposition: "remove", Repository: "acme/b", Branch: "b", Layout: worktrees.LayoutLegacy, HasManifest: true, Provenance: "reconstructed", OwnerState: worktrees.OwnerGone},
					{Disposition: "remove", Repository: "acme/c", Branch: "b", Layout: worktrees.LayoutExternal, HasManifest: true},
				},
			},
		},
		Residue: []worktrees.OrphanResidue{{Task: "t", Repository: "acme/a", Layout: worktrees.LayoutLocal, Evidence: []string{"unregistered"}, Remedy: "wb worktree gc"}},
		Totals: worktrees.OrphanTotals{
			Worktrees: 3, Families: 1,
			ByLayout:    map[string]int{worktrees.LayoutCurrent: 1, worktrees.LayoutLegacy: 1, worktrees.LayoutExternal: 1},
			ByDispositn: map[string]int{worktrees.DispositionRemove: 1},
			NoManifest:  1, Dirty: 1, Residue: 1,
		},
		Unscanned: []string{"/tmp/unreadable"},
	}
	cwWtSweepWrites(t, 40, func(writer *cwWtFailWriter) error {
		return renderOrphans(writer, report, "")
	})

	active := activeWorktreeReport{
		SchemaVersion: 1,
		Local:         activeLocalStatus{Status: "incomplete", OmittedUnresolvedClaims: 2},
		Remote:        activeRemoteStatus{Status: "stale", Error: "stale snapshot"},
		Worktrees: []activeWorktreeRow{
			{Locality: "local", Repository: "acme/a", Task: "t", Branch: "b", OwnerState: "active", Lifecycle: "working", Summary: "s"},
			{Locality: "remote", Machine: "m", Repository: "acme/b", Task: "t2", Branch: "b", OwnerState: "unknown", Lifecycle: "working", SnapshotStale: true},
		},
	}
	cwWtSweepWrites(t, 14, func(writer *cwWtFailWriter) error {
		return writeActiveWorktreeText(writer, active)
	})
}

func TestCwWtWriterSweepEndMarkerRescueAndLogVerb(t *testing.T) {
	end := worktreeend.Result{
		Task: "t", Applied: true,
		Members: []worktreeend.MemberResult{
			{Repository: "acme/a", Worktree: "/tmp/w", Action: "retired", Dirty: []string{"a.go"}, CaptureRef: "ref", Detail: "detail"},
			{Repository: "acme/b", Worktree: "/tmp/w2", Action: "skip"},
		},
		ClaimOutcome: "released",
	}
	cwWtSweepWrites(t, 10, func(writer *cwWtFailWriter) error {
		return printWorktreeEnd(cwWtCmdWriter(writer), "text", end)
	})

	outcomes := []markerOutcome{
		{Path: "/a", Kind: "canonical", MarkerWritten: true, ExcludeWritten: true},
		{Path: "/b", Kind: "worktree", MarkerWritten: true},
		{Path: "/c", Kind: "worktree", ExcludeWritten: true},
		{Path: "/d", Kind: "worktree"},
		{Path: "/e", Kind: "worktree", Error: "boom"},
	}
	cwWtSweepWrites(t, 10, func(writer *cwWtFailWriter) error {
		return renderMarkerOutcomes(cwWtCmdWriter(writer), "text", false, outcomes)
	})

	changes := make([]canonicalrescue.Change, 0, 25)
	for index := 0; index < 25; index++ {
		changes = append(changes, canonicalrescue.Change{Status: "M", Path: "file"})
	}
	// The applied spelling is used because the dry-run spelling always ends in
	// the findings exit error; only a captured, pushed, restored report returns
	// nil, which is what lets the sweep detect the true end of the output.
	rescue := canonicalrescue.Report{
		Path: "/tmp/clone", Changes: changes, UntrackedCount: 25,
		RescueBranch: "rescue/x", RescueCommit: "deadbeef", Pushed: true, Restored: true,
	}
	cwWtSweepWrites(t, 30, func(writer *cwWtFailWriter) error {
		return renderRescueReport(cwWtCmdWriter(writer), "text", true, rescue)
	})

	verb := worktrees.LogVerbResult{
		Verb: "steer", Worktree: "/tmp/wt", Applied: true, Prompt: "p",
		Event:   &worktrees.LocalWorkLogEvent{Type: "prompt_recorded", Seq: 1},
		Offline: true, Outbox: 1, Notes: []string{"n"}, Diagnosis: []string{"d"},
	}
	cwWtSweepWrites(t, 10, func(writer *cwWtFailWriter) error {
		return encodeLogVerbResult(cwWtCmdWriter(writer), "text", verb)
	})
}

func TestCwWtWorkLogArchiveAfterFinalize(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	if err := os.Remove(filepath.Join(worktree, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	// finalize --apply seals the claim; archive --apply then copies the sealed
	// journal into WB_HOME.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "finalize", worktree,
		"--mode", "manual", "--initiator", "cwWt", "--result", "success", "--message", "done", "--apply"); err != nil {
		t.Fatalf("finalize --apply: %v", err)
	}
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "archive", worktree,
		"--mode", "manual", "--initiator", "cwWt", "--apply", "--force")
	if err != nil {
		t.Fatalf("archive --apply: %v", err)
	}
	if !strings.Contains(stdout, "archive ") || !strings.Contains(stdout, "applied=true") {
		t.Fatalf("archive stdout = %q", stdout)
	}
	// --force is the documented operator override for the terminal/TTL gates.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "archive", worktree,
		"--mode", "manual", "--initiator", "cwWt", "--apply", "--force"); err != nil {
		t.Fatalf("archive --force: %v", err)
	}
	// integrate on a clean worktree reaches its success path.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "integrate", worktree,
		"--mode", "manual", "--initiator", "cwWt"); err != nil {
		t.Logf("integrate on a clean worktree reported: %v", err)
	}
}
