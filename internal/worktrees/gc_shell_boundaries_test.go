package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGCAccountingAndEvidenceBoundaries(t *testing.T) {
	t.Parallel()
	blocked := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(blocked, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyGC(context.Background(), GCOptions{ProjectsRoot: filepath.Join(blocked, "projects")}, &GCOutcome{}); err == nil {
		t.Fatal("GC accepted an invalid report authority")
	}
	if err := sweepTaskShells(context.Background(), GCOptions{ProjectsRoot: filepath.Join(blocked, "projects")}, &GCOutcome{}); err == nil || !strings.Contains(err.Error(), "sweep empty task shells") {
		t.Fatalf("shell-sweep error was lost: %v", err)
	}
	if _, err := GC(context.Background(), GCOptions{ProjectsRoot: filepath.Join(blocked, "projects")}); err == nil {
		t.Fatal("GC accepted an invalid projects root")
	}
	result := ListResult{Task: "task", Repository: "acme/app", WorktreeDir: t.TempDir(), Branch: "feature",
		HeadSHA: strings.Repeat("a", 40), RemoteTargetSHA: strings.Repeat("b", 40),
		RemoteHeadSHA: strings.Repeat("c", 40), AbsorbedBySHA: strings.Repeat("d", 40),
		RebaseMergedAtOrigin: true, AbsorbedAtOrigin: true, HeadUnknownToRemote: true}
	evidence := strings.Join(gcEvidence(result), " ")
	for _, want := range []string{"head=", "target=", "origin/feature=", "rebase-merged", "absorbed-by=", "head-never-pushed"} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("missing %q from evidence %q", want, evidence)
		}
	}
	result.MergedPullRequest = &PullRequest{URL: "https://example.test/merged"}
	result.OpenPullRequest = &PullRequest{URL: "https://example.test/open"}
	for _, want := range []string{"merged-pr=", "open-pr="} {
		if !strings.Contains(strings.Join(gcEvidence(result), " "), want) {
			t.Fatalf("missing %q from pull-request evidence", want)
		}
	}
	if command, _ := detachedUnknownResolution(result, ManagementManaged, nil); !strings.Contains(command, "abort") {
		t.Fatalf("managed detached command = %q", command)
	}
	if command, _ := detachedUnknownResolution(result, ManagementUnknown, nil); !strings.Contains(command, "adopt") {
		t.Fatalf("unknown attached command = %q", command)
	}
	result.Detached = true
	if command, _ := detachedUnknownResolution(result, ManagementUnknown, nil); !strings.Contains(command, "worktree remove") {
		t.Fatalf("unknown detached command = %q", command)
	}
	now := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	if !checkoutIsInUse(result, GCOptions{}, now) || activityAgeSeconds(result, now) != 0 {
		t.Fatal("unknown activity age was treated as safe to sweep")
	}
	result.LastActivityAt = now.Add(time.Hour)
	if activityAgeSeconds(result, now) != 0 {
		t.Fatal("future activity yielded a negative age")
	}
	result.LastActivityAt = now.Add(-time.Hour)
	if checkoutIsInUse(result, GCOptions{SessionFreshness: -1}, now) || activityAgeSeconds(result, now) != 3600 {
		t.Fatal("explicitly disabled freshness or elapsed activity was misclassified")
	}
	result.OwnerState, result.Owner = "active", "agent"
	if warning := staleOwnerWarning(result, GCOptions{SessionFreshness: time.Minute}, now, GCClassContained); !strings.Contains(warning, "agent") {
		t.Fatalf("stale active owner warning = %q", warning)
	}
	if warning := staleOwnerWarning(result, GCOptions{SessionFreshness: time.Minute}, now, GCClassDirty); warning != "" {
		t.Fatalf("dirty checkout received unrelated stale-owner warning: %q", warning)
	}
	worktree := t.TempDir()
	journal := filepath.Join(worktree, ".wb", "local")
	if err := os.MkdirAll(journal, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := "version: 1\neffort_id: task\neffort_kind: task\nrepository: acme/app\nworktree: \"\"\nbranch: main\nprovenance: created\n"
	if err := os.WriteFile(filepath.Join(journal, "manifest.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if management := worktreeManagement(worktree); management != ManagementUnmanaged {
		t.Fatalf("valid manifest without worktree identity = %q", management)
	}
}

func TestGCApplySortsPartialTaskReceipts(t *testing.T) {
	t.Parallel()
	outcome := GCOutcome{PartialTasks: []GCPartialTask{{Task: "zeta"}, {Task: "alpha"}}}
	if err := applyGC(context.Background(), GCOptions{ProjectsRoot: t.TempDir()}, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.PartialTasks[0].Task != "alpha" || outcome.PartialTasks[1].Task != "zeta" {
		t.Fatalf("unsorted partial task receipts: %#v", outcome.PartialTasks)
	}
}

//nolint:paralleltest // shell fixture configures HOME and WB root process-wide
func TestRetireTaskShellsRefusesUnsafeRootAndOwnerShapes(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	taskPath := filepath.Join(worktreesRoot, "unsafe-owner")
	owner := filepath.Join(taskPath, "acme")
	if err := os.MkdirAll(owner, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(owner, "linked-repo")); err != nil {
		t.Fatal(err)
	}
	result, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projectsRoot, Tasks: []string{"unsafe-owner"}})
	if err != nil || len(result.Results) != 1 || result.Results[0].Eligible {
		t.Fatalf("symlinked owner entry = %#v, %v", result.Results, err)
	}
	if empty, err := ownerDirectoryIsProvablyEmpty(filepath.Join(taskPath, "missing")); err == nil || empty {
		t.Fatalf("missing owner appeared empty: %t, %v", empty, err)
	}
	if empty, reason := taskShellIsEmpty(filepath.Join(worktreesRoot, "missing"), "missing", false); empty || !strings.Contains(reason, "stat task") {
		t.Fatalf("missing task shell = %t, %q", empty, reason)
	}
	blocked := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(blocked, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: filepath.Join(blocked, "projects")}); err == nil {
		t.Fatal("shell retirement accepted invalid projects root")
	}
}
