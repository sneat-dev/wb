package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBranchCleanupRelativeReportDirectoryAndYoungerBranch(t *testing.T) {
	root := t.TempDir()
	normalized, err := normalizeBranchCleanupOptions(BranchCleanupOptions{
		ProjectsRoot: root, Scope: BranchScopeLocal, ReportDir: "reports/branch-cleanup",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(normalized.ReportDir) || !strings.HasSuffix(normalized.ReportDir, filepath.Join("reports", "branch-cleanup")) {
		t.Fatalf("relative report path was not normalized: %q", normalized.ReportDir)
	}
	now := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	results := planBranchCleanup([]BranchEntry{{Repository: "acme/app", Branch: "feature/recent",
		Scope: BranchScopeLocal, Disposition: BranchContained, CommitterDate: now}},
		branchSweepOptions{Now: now, OlderThan: time.Hour})
	if len(results) != 1 || results[0].Eligible || !strings.Contains(results[0].SkipReason, "younger") {
		t.Fatalf("recent branch cleanup plan = %#v", results)
	}
}

func TestBranchCleanupReportFilesystemFailuresAreReturned(t *testing.T) {
	root := t.TempDir()
	reportFile := filepath.Join(root, "report-file")
	if err := os.WriteFile(reportFile, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeBranchCleanupReportInjected(reportFile, BranchCleanupOptions{Base: "main"}, time.Now(), nil, nil); err == nil {
		t.Fatal("report writer accepted a regular file as directory")
	}
	symlink := filepath.Join(root, "report-link")
	if err := os.Symlink(root, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := writeBranchCleanupReportInjected(filepath.Join(symlink, "nested"), BranchCleanupOptions{}, time.Now(), nil, nil); err == nil {
		t.Fatal("report writer followed a symlink ancestor")
	}
	if err := rejectSymlinkAncestors(filepath.Join(reportFile, "child")); err == nil {
		t.Fatal("non-directory ancestor was ignored")
	}
	missing := filepath.Join(root, "missing")
	if _, err := copyFileSHA256Injected(missing, filepath.Join(root, "copy"), nil); err == nil {
		t.Fatal("copy reported a digest for a missing source")
	}
	if err := syncFile(missing); err == nil {
		t.Fatal("missing file synced")
	}
	if err := syncDirectory(missing); err == nil {
		t.Fatal("missing directory synced")
	}
	if err := syncDirectoryAndAncestors(missing); err == nil {
		t.Fatal("missing directory ancestry synced")
	}
	if err := validateBranchCleanupReportDir(context.Background(), filepath.Join(root, "safe"),
		map[string]string{"acme/app": ""}); err != nil {
		t.Fatalf("an empty repository path should be skipped: %v", err)
	}
}

func TestBranchCleanupSourceWithoutRegisteredRootsIsRejected(t *testing.T) {
	ctx := lifecycleGitContext(t, "/fixture/repository", lifecycleGitReply{operation: "worktree", output: ""})
	if _, err := sourceRepositoryRoots(ctx, "/fixture/repository"); err == nil ||
		!strings.Contains(err.Error(), "no registered worktree roots") {
		t.Fatalf("rootless source repository error = %v", err)
	}
}

func TestBranchCleanupRecoveryArchiveFailureStopsRetirement(t *testing.T) {
	fixture := newGitFixture(t)
	reportFile := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(reportFile, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := BranchCleanupResult{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "feature/reviewed",
		Scope: BranchScopeLocal, Disposition: BranchSuperseded, SupersededAtOrigin: true},
		Eligible: true, Outcome: "planned"}
	if _, err := archiveReviewedBranch(context.Background(), reportFile, fixture.canonical, result); err == nil {
		t.Fatal("archive succeeded despite blocked recovery destination")
	}
	results := []BranchCleanupResult{result}
	applyBranchCleanup(context.Background(), results, map[string]string{"acme/app": fixture.canonical},
		BranchCleanupOptions{ReportDir: reportFile}, time.Now())
	if results[0].Outcome != "failed" || results[0].Applied || !strings.Contains(results[0].Error, "recovery bundle") {
		t.Fatalf("retirement continued after archive failure: %#v", results[0])
	}
}

func TestBranchCleanupApplyFailsWhenAuditReportCannotBeWritten(t *testing.T) {
	fixture := newGitFixture(t)
	reportFile := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(reportFile, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BranchCleanup(context.Background(), BranchCleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeLocal, Apply: true, ReportDir: reportFile,
	}); err == nil {
		t.Fatal("cleanup continued without a durable audit report")
	}
}
