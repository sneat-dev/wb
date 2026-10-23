package worktrees

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOrphanInventoryOrderingAndTotals(t *testing.T) {
	t.Parallel()
	report := OrphanReport{Unscanned: []string{"retained diagnostic"}}
	report.Totals.ByLayout = map[string]int{}
	report.Totals.ByDispositn = map[string]int{}
	entries := map[string][]OrphanWorktree{
		"z": {{EffortID: "same", Path: "/z/b", Layout: LayoutCurrent, Disposition: DispositionReview, Dirty: true}, {EffortID: "same", Path: "/z/a", Layout: LayoutLocal, Disposition: DispositionRemove, HasManifest: true}, {EffortID: "earlier", Path: "/z/c", Layout: LayoutLocal, Disposition: DispositionRemove, HasManifest: true}},
		"a": {{EffortID: "a", Path: "/a", Layout: LayoutExternal, Disposition: DispositionActive, HasManifest: true}},
	}
	finalizeOrphanReport(&report, entries)
	if report.Totals.Families != 2 || report.Totals.Worktrees != 4 || report.Totals.NoManifest != 1 || report.Totals.Dirty != 1 || report.Totals.ByLayout[LayoutLocal] != 2 || report.Totals.ByDispositn[DispositionRemove] != 2 {
		t.Fatalf("totals: %+v", report.Totals)
	}
	if report.Families[0].RootEffort != "a" || report.Families[1].RootEffort != "z" || report.Families[1].Worktrees[1].Path != "/z/a" || report.Families[1].Worktrees[2].Path != "/z/b" {
		t.Fatalf("ordering: %+v", report.Families)
	}
	if !reflect.DeepEqual(report.Unscanned, []string{"retained diagnostic"}) || report.Families[1].Disposition != DispositionReview {
		t.Fatalf("inventory evidence lost: %+v", report)
	}
}

func TestOrphanDispositionPreservesPrecedenceAndUnnamedOwner(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name                  string
		entry                 OrphanWorktree
		disposition, evidence string
	}{
		{"dirty", OrphanWorktree{Dirty: true, Merged: true, OwnerState: OwnerLive}, DispositionReview, "uncommitted"},
		{"merged", OrphanWorktree{Merged: true, OwnerState: OwnerLive}, DispositionRemove, "origin/main"},
		{"live", OrphanWorktree{OwnerState: OwnerLive, OwnerPID: 17}, DispositionActive, "an unnamed session"},
		{"no commit", OrphanWorktree{}, DispositionDecide, "no commit of its own"},
		{"gone", OrphanWorktree{LastCommit: now, OwnerState: OwnerGone, OwnerAgent: "codex", OwnerPID: 17}, DispositionDecide, "codex"},
		{"recent", OrphanWorktree{LastCommit: now}, DispositionActive, "age alone"},
		{"stale", OrphanWorktree{LastCommit: now.Add(-30 * 24 * time.Hour), AgeDays: 30}, DispositionDecide, "idle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, evidence := orphanDisposition(tc.entry, "main", 14*24*time.Hour, now)
			if got != tc.disposition || !evidenceContains(evidence, tc.evidence) {
				t.Fatalf("classification=%s %v", got, evidence)
			}
		})
	}
}

func TestOrphanResidueRetainsNativeEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := filepath.Join(root, "acme", "app")
	if err := os.MkdirAll(filepath.Join(canonical, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(root, "checkout")
	if err := os.Mkdir(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := inspectResidue(root, root, LayoutCurrent, "task", "acme/app", checkout, map[string]bool{filepath.Clean(checkout): true}); got != nil {
		t.Fatalf("registered residue: %+v", got)
	}
	got := inspectResidue(root, root, LayoutCurrent, "task", "acme/app", checkout, nil)
	if got == nil || got.CanonicalDir != canonical || !evidenceContains(got.Evidence, "no Git metadata") || !strings.Contains(got.Remedy, "cleanup task") {
		t.Fatalf("canonical residue: %+v", got)
	}
	if got := inspectResidue(root, root, LayoutCurrent, "task", "absent/app", checkout, nil); got != nil {
		t.Fatalf("ordinary directory: %+v", got)
	}
	marker := []byte("gitdir: " + filepath.Join(root, "missing-admin") + "\n")
	if err := os.WriteFile(filepath.Join(checkout, ".git"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	got = inspectResidue(root, root, LayoutLegacy, "task", "bad/coordinate/shape/extra", checkout, nil)
	if got == nil || got.CanonicalDir != "" || !evidenceContains(got.Evidence, "no longer exists") || !evidenceContains(got.Evidence, "no canonical clone at "+filepath.Join(root, "bad/coordinate/shape/extra")) {
		t.Fatalf("lost registration: %+v", got)
	}
	after, err := os.ReadFile(filepath.Join(checkout, ".git"))
	if err != nil || !reflect.DeepEqual(after, marker) {
		t.Fatalf("residue detection changed marker: %q %v", after, err)
	}
}

func TestOrphanInventoryAndBackfillRequireProjectsRoot(t *testing.T) {
	t.Parallel()
	report, err := Orphans(t.Context(), OrphanOptions{})
	if err == nil || err.Error() != "projects root is required" || !reflect.DeepEqual(report, OrphanReport{}) {
		t.Fatalf("empty inventory: %+v %v", report, err)
	}
	results, err := Backfill(t.Context(), BackfillOptions{})
	if err == nil || err.Error() != "projects root is required" || results != nil {
		t.Fatalf("empty adoption: %+v %v", results, err)
	}
}
