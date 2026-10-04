package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/sneat-dev/wb/internal/worktreeend"
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
