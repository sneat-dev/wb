package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderDiffShowsStatAndPatch(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	record := Record{
		AgentID: "agt-00000000000000000000000000000001", WorktreeDir: worktree, BaseSHA: "base",
		Changes: &ChangeSummary{Untracked: []string{"new.txt"}},
	}
	deps := TargetDeps{Git: func(_ context.Context, _ string, args ...string) (string, error) {
		if args[0] == "diff" && containsAll(args, "--stat") {
			return " a.go | 2 ++\n", nil
		}
		return "diff --git a/a.go b/a.go\n+changed\n", nil
	}}
	stat, err := RenderDiff(context.Background(), record, false, deps)
	if err != nil {
		t.Fatalf("RenderDiff: %v", err)
	}
	if !strings.Contains(stat, "a.go | 2 ++") || !strings.Contains(stat, "untracked") || !strings.Contains(stat, "new.txt") {
		t.Fatalf("stat = %q", stat)
	}
	patch, err := RenderDiff(context.Background(), record, true, deps)
	if err != nil {
		t.Fatalf("RenderDiff patch: %v", err)
	}
	if !strings.Contains(patch, "+changed") {
		t.Fatalf("patch = %q", patch)
	}
}

func TestRenderDiffHandlesAnEmptyChangeAndAFailingGit(t *testing.T) {
	t.Parallel()
	record := Record{AgentID: "agt-00000000000000000000000000000001", WorktreeDir: t.TempDir()}
	empty := TargetDeps{Git: func(context.Context, string, ...string) (string, error) { return "", nil }}
	if text, err := RenderDiff(context.Background(), record, false, empty); err != nil || !strings.Contains(text, "no tracked change") {
		t.Fatalf("empty stat = %q %v", text, err)
	}
	if text, err := RenderDiff(context.Background(), record, true, empty); err != nil || !strings.Contains(text, "no tracked change") {
		t.Fatalf("empty patch = %q %v", text, err)
	}
	failing := TargetDeps{Git: func(context.Context, string, ...string) (string, error) { return "", errors.New("boom") }}
	if _, err := RenderDiff(context.Background(), record, false, failing); err == nil {
		t.Fatal("a failing git must be reported")
	}
	if _, err := RenderDiff(context.Background(), record, true, failing); err == nil {
		t.Fatal("a failing git must be reported for the patch too")
	}
	if _, err := RenderDiff(context.Background(), Record{AgentID: "agt-00000000000000000000000000000001"}, false, empty); err == nil {
		t.Fatal("a run without a worktree must be refused")
	}
}

func TestRenderDiffBoundsAHugePatch(t *testing.T) {
	t.Parallel()
	record := Record{AgentID: "agt-00000000000000000000000000000001", WorktreeDir: t.TempDir(), BaseSHA: "base"}
	deps := TargetDeps{Git: func(context.Context, string, ...string) (string, error) {
		return strings.Repeat("x", maxDiffBytes+10), nil
	}}
	text, err := RenderDiff(context.Background(), record, true, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(text) > maxDiffBytes+200 || !strings.Contains(text, "truncated") {
		t.Fatalf("patch length = %d", len(text))
	}
}

func TestWriteDiffStoresAPrivateBoundedPatch(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	path, err := store.WriteDiff(context.Background(), record.AgentID, t.TempDir(), "base", TargetDeps{
		Git: func(context.Context, string, ...string) (string, error) { return "diff", nil },
	})
	if err != nil {
		t.Fatalf("WriteDiff: %v", err)
	}
	if path == "" {
		t.Fatal("a diff path was expected")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("diff mode = %v", info.Mode().Perm())
	}
	if path, err := store.WriteDiff(context.Background(), record.AgentID, "", "base", TargetDeps{}); err != nil || path != "" {
		t.Fatalf("a run without a base or worktree writes nothing: %q %v", path, err)
	}
	if _, err := store.WriteDiff(context.Background(), record.AgentID, t.TempDir(), "base", TargetDeps{
		Git: func(context.Context, string, ...string) (string, error) { return "", errors.New("boom") },
	}); err == nil {
		t.Fatal("a failing git must be reported")
	}
}

func TestWriteDiffBoundsAHugePatch(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	path, err := store.WriteDiff(context.Background(), record.AgentID, t.TempDir(), "base", TargetDeps{
		Git: func(context.Context, string, ...string) (string, error) {
			return strings.Repeat("x", maxDiffBytes+1), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxDiffBytes+200 || !strings.Contains(string(raw), "truncated") {
		t.Fatalf("stored patch length = %d", len(raw))
	}
}

func TestUntrackedFromStatusCollectsOnlyUntrackedPaths(t *testing.T) {
	t.Parallel()
	got := untrackedFromStatus(" M a.go\n?? b.go\nA  c.go\n?? d.go\n")
	if len(got) != 2 || got[0] != "b.go" || got[1] != "d.go" {
		t.Fatalf("untracked = %#v", got)
	}
}

func TestDiffPathIsInsideTheRunDirectory(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	path, err := store.WriteDiff(context.Background(), record.AgentID, t.TempDir(), "base", TargetDeps{
		Git: func(context.Context, string, ...string) (string, error) { return "diff", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != store.Dir(record.AgentID) {
		t.Fatalf("diff %s is outside the run directory %s", path, store.Dir(record.AgentID))
	}
}
