package disk

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points every root Collect would otherwise discover on the real
// machine at empty temporary directories. Without it these tests walk the
// developer's actual Go module cache — eight gigabytes on the machine this was
// written on — which is both slow and a measurement of the wrong thing.
func isolate(t *testing.T) {
	t.Helper()
	empty := t.TempDir()
	t.Setenv("GOCACHE", filepath.Join(empty, "go-build"))
	t.Setenv("GOMODCACHE", filepath.Join(empty, "go-mod"))
	t.Setenv("HOME", empty)
	t.Setenv("TMPDIR", filepath.Join(empty, "tmp"))
}

func TestAvailableRatioIsZeroWhenTheVolumeIsUnknown(t *testing.T) {
	t.Parallel()
	// A zero total means the measurement failed. Reporting a ratio of 1 would
	// read as "plenty of room" on exactly the machine that could not be read.
	if got := (Filesystem{}).AvailableRatio(); got != 0 {
		t.Fatalf("ratio = %v, want 0", got)
	}
	got := Filesystem{TotalBytes: 200, AvailableBytes: 50}.AvailableRatio()
	if got != 0.25 {
		t.Fatalf("ratio = %v, want 0.25", got)
	}
}

func TestCollectRequiresAProjectsRoot(t *testing.T) {
	t.Parallel()
	if _, err := Collect(context.Background(), Options{}); err == nil {
		t.Fatal("empty projects root was accepted")
	}
}

// Worktree discovery must follow the {org}/{repo}/.worktrees layout WB
// maintains, and must not descend into unrelated directories.
func TestCollectFindsWorktreesInTheManagedLayout(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	managed := filepath.Join(root, "acme", "app", ".worktrees", "task")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A .worktrees directory one level too deep is not the managed layout.
	deep := filepath.Join(root, "acme", "app", "nested", ".worktrees")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	report, err := Collect(context.Background(), Options{
		ProjectsRoot: root,
		WBHome:       filepath.Join(root, "absent-wb-home"),
	})
	if err != nil {
		t.Fatal(err)
	}

	var worktrees *Category
	for i := range report.Categories {
		if report.Categories[i].Name == "worktrees" {
			worktrees = &report.Categories[i]
		}
	}
	if worktrees == nil {
		t.Fatalf("no worktrees category: %#v", report.Categories)
	}
	expected := filepath.Join(root, "acme", "app", ".worktrees")
	if len(worktrees.Roots) != 1 || worktrees.Roots[0] != expected {
		t.Fatalf("roots = %#v, want only %s", worktrees.Roots, expected)
	}
	if worktrees.ApparentBytes <= 0 {
		t.Fatalf("apparent bytes = %d, want the written file counted", worktrees.ApparentBytes)
	}
}

// A category whose roots are all absent must not appear at all. Printing a row
// of zeroes for a cache this machine does not have invites the reader to
// conclude the cache is empty rather than elsewhere.
func TestCollectOmitsCategoriesWithNoPresentRoots(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	report, err := Collect(context.Background(), Options{
		ProjectsRoot: root,
		WBHome:       filepath.Join(root, "absent"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, category := range report.Categories {
		if len(category.Roots) == 0 {
			t.Fatalf("category %q reported with no roots", category.Name)
		}
	}
}

func TestCollectSkipSizesStillReportsTheFilesystem(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	report, err := Collect(context.Background(), Options{ProjectsRoot: root, SkipSizes: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Filesystem.TotalBytes <= 0 {
		t.Fatalf("filesystem not measured: %#v", report.Filesystem)
	}
	if report.AttributedBytes != 0 {
		t.Fatalf("attributed bytes = %d, want 0 when sizes are skipped", report.AttributedBytes)
	}
}

func TestFindingsReportLowHeadroomAgainstTheConfiguredFloor(t *testing.T) {
	t.Parallel()
	report := Report{Filesystem: Filesystem{
		Path: "/", TotalBytes: 1000, AvailableBytes: 50,
	}}
	found := findings(report, 0.10, true)
	if len(found) != 1 || !strings.Contains(found[0], "available") {
		t.Fatalf("findings = %#v", found)
	}
	// Above the floor there is nothing to say.
	report.Filesystem.AvailableBytes = 500
	if found := findings(report, 0.10, true); len(found) != 0 {
		t.Fatalf("findings = %#v, want none", found)
	}
}

func TestFindingsReportOwnerlessScratch(t *testing.T) {
	t.Parallel()
	report := Report{
		Filesystem: Filesystem{Path: "/", TotalBytes: 1000, AvailableBytes: 900},
		Categories: []Category{{Name: "scratch", Kind: "scratch", UnsharedBytes: 4096}},
	}
	found := findings(report, 0.10, false)
	if len(found) != 1 || !strings.Contains(found[0], "no owning task") {
		t.Fatalf("findings = %#v", found)
	}
	// With sizes skipped the scratch total is unknown, so claiming it is
	// ownerless would be asserting something unmeasured.
	if found := findings(report, 0.10, true); len(found) != 0 {
		t.Fatalf("findings = %#v, want none when sizes were skipped", found)
	}
}

// An unreadable root must reduce confidence in the total rather than silently
// shrink it, or a partially measured machine reads as a tidy one.
func TestFindingsFlagUnmeasuredRoots(t *testing.T) {
	t.Parallel()
	report := Report{
		Filesystem: Filesystem{Path: "/", TotalBytes: 1000, AvailableBytes: 900},
		Skipped:    []string{"/some/root: permission denied"},
	}
	found := findings(report, 0.10, false)
	if len(found) != 1 || !strings.Contains(found[0], "floor, not a total") {
		t.Fatalf("findings = %#v", found)
	}
}

func TestRenderLeadsWithHeadroomAndShowsBothFigures(t *testing.T) {
	t.Parallel()
	rendered := Render(Report{
		Filesystem:      Filesystem{Path: "/vol", TotalBytes: 200, AvailableBytes: 100},
		AttributedBytes: 64,
		Categories: []Category{
			{Name: "node-package-store", Kind: "cache", UnsharedBytes: 64, ApparentBytes: 4096},
		},
		Findings: []string{"something worth acting on"},
	})
	if !strings.HasPrefix(rendered, "Filesystem /vol") {
		t.Fatalf("headroom is not first: %q", rendered)
	}
	// Both columns must be present: the gap between them is the whole point.
	if !strings.Contains(rendered, "RECLAIM") || !strings.Contains(rendered, "APPARENT") {
		t.Fatalf("missing a size column: %q", rendered)
	}
	if !strings.Contains(rendered, "something worth acting on") {
		t.Fatalf("findings not rendered: %q", rendered)
	}
}

func TestScratchRootsIncludeTheOwnedPathAndAdHocSiblings(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	for _, name := range []string{"wb", "wb-pr308-test-cache", "unrelated-thing"} {
		if err := os.MkdirAll(filepath.Join(temp, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots := scratchRoots()
	joined := strings.Join(roots, "\n")
	if !strings.Contains(joined, filepath.Join(temp, "wb")) {
		t.Fatalf("owned scratch path missing: %#v", roots)
	}
	if !strings.Contains(joined, filepath.Join(temp, "wb-pr308-test-cache")) {
		t.Fatalf("ad-hoc per-task cache missing: %#v", roots)
	}
	if strings.Contains(joined, "unrelated-thing") {
		t.Fatalf("claimed a directory WB did not create: %#v", roots)
	}
}
