package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestCleanupApplyReportAndFleetFailureBoundaries(t *testing.T) {
	t.Parallel()
	t.Run("failed backlog resume", func(t *testing.T) {
		t.Parallel()
		run := &cleanupRun{ctx: context.Background(), backlog: []lifecycleBacklogRecord{{WorktreeDir: filepath.Join(t.TempDir(), "missing")}}}
		if _, err := run.applyCleanup(); err == nil {
			t.Fatal("invalid backlog resumed")
		}
	})
	t.Run("ineligible task is skipped", func(t *testing.T) {
		t.Parallel()
		run := &cleanupRun{
			ctx: context.Background(), normalized: CleanupOptions{beforeCleanupLocks: func() {}},
			outcome: CleanupOutcome{Results: []CleanupResult{{ListResult: ListResult{
				Task: "task", WorktreesRoot: t.TempDir(), Repository: "acme/app",
			}}}},
		}
		if _, err := run.applyCleanup(); err != nil {
			t.Fatalf("skipped task: %v", err)
		}
	})
	t.Run("failed report write", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "blocked-report-directory")
		if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		run := &cleanupRun{
			ctx:        context.Background(),
			normalized: CleanupOptions{Tasks: []string{"task"}, ReportDir: path},
			outcome:    CleanupOutcome{Results: []CleanupResult{{Reason: "preflight Work Log for acme/app: mismatch"}}},
		}
		_, err := run.applyCleanup()
		if err == nil || !strings.Contains(err.Error(), "write failed cleanup report") {
			t.Fatalf("missing failed report evidence: %v", err)
		}
	})
	t.Run("final report write", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "blocked-report-directory")
		if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		run := &cleanupRun{ctx: context.Background(), normalized: CleanupOptions{ReportDir: path}}
		_, err := run.applyCleanup()
		if err == nil {
			t.Fatal("final cleanup report unexpectedly succeeded")
		}
	})
	t.Run("fleet task failure reason", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		blockedRoot := filepath.Join(root, "blocked-root")
		if err := os.WriteFile(blockedRoot, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		run := &cleanupRun{
			ctx:        context.Background(),
			normalized: CleanupOptions{ProjectsRoot: root, AllMerged: true, Workers: 1},
			resolution: wbhome.Resolution{Write: wbhome.Layout{Home: filepath.Join(root, "home")}},
			outcome: CleanupOutcome{Results: []CleanupResult{{ListResult: ListResult{
				Task: "task", Repository: "acme/app", WorktreesRoot: blockedRoot,
				CanonicalDir: filepath.Join(root, "acme", "app"),
			}, Eligible: true, Applied: true}, {ListResult: ListResult{
				Task: "task", Repository: "acme/app", WorktreesRoot: blockedRoot,
				CanonicalDir: filepath.Join(root, "acme", "app"),
			}, Eligible: true}}},
		}
		outcome, err := run.applyCleanup()
		if err != nil || len(outcome.Diagnostics) != 1 || outcome.Results[1].Reason == "" || outcome.Results[0].Reason != "" {
			t.Fatalf("fleet error mapping outcome=%#v err=%v", outcome, err)
		}
	})
	t.Run("named task fails", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		blockedRoot := filepath.Join(root, "blocked-root")
		if err := os.WriteFile(blockedRoot, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		run := &cleanupRun{
			ctx: context.Background(), normalized: CleanupOptions{ProjectsRoot: root, Tasks: []string{"task"}, Workers: 1},
			resolution: wbhome.Resolution{Write: wbhome.Layout{Home: filepath.Join(root, "home")}},
			outcome: CleanupOutcome{Results: []CleanupResult{{ListResult: ListResult{
				Task: "task", Repository: "acme/app", WorktreesRoot: blockedRoot,
				CanonicalDir: filepath.Join(root, "acme", "app"),
			}, Eligible: true}}},
		}
		if _, err := run.applyCleanup(); err == nil {
			t.Fatal("named task failure was swallowed")
		}
	})
	t.Run("recovery report remains validated", func(t *testing.T) {
		t.Parallel()
		run := &cleanupRun{
			ctx: context.Background(), normalized: CleanupOptions{ReportDir: t.TempDir()},
			outcome: CleanupOutcome{Recovery: &InterruptedLockRecovery{}},
		}
		outcome, err := run.applyCleanup()
		if err != nil || outcome.ReportPath == "" {
			t.Fatalf("validated report=%q err=%v", outcome.ReportPath, err)
		}
	})
}
