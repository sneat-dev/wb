package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWtLogCovRenameEligibility(t *testing.T) {
	t.Parallel()
	if eligible, reason := renameEligibility(ListResult{Clean: true}); !eligible || reason != "" {
		t.Fatalf("clean worktree = %t/%q", eligible, reason)
	}
	if eligible, reason := renameEligibility(ListResult{Clean: false}); eligible || reason != "worktree has local changes" {
		t.Fatalf("dirty worktree = %t/%q", eligible, reason)
	}
	if eligible, reason := renameEligibility(ListResult{Clean: true, External: true}); eligible || !strings.Contains(reason, "adopted worktree cannot be renamed") {
		t.Fatalf("external worktree = %t/%q", eligible, reason)
	}
	if eligible, reason := renameEligibility(ListResult{Clean: true, Locked: true, Task: "locked-task", LockOwner: LockOwnerLive, LockOwnerPID: 4242}); eligible || !strings.Contains(reason, "still running") {
		t.Fatalf("locked worktree = %t/%q", eligible, reason)
	}
}

func TestWtLogCovBlockRenameTask(t *testing.T) {
	t.Parallel()
	plans := []renamePlan{
		{result: RenameResult{Repository: "acme/app", Eligible: true}},
		{result: RenameResult{Repository: "acme/lib", Eligible: false, Reason: "dirty"}},
	}
	blockRenameTask(plans, nil, "")
	if plans[0].result.Eligible || !strings.Contains(plans[0].result.Reason, "coordinated task blocked by acme/lib: dirty") {
		t.Fatalf("ineligible plan did not block the task: %#v", plans[0].result)
	}

	diagnostics := []ListDiagnostic{{Path: "/tmp/broken", Message: "unreadable"}}
	diagnosed := []renamePlan{{result: RenameResult{Eligible: true}}}
	blockRenameTask(diagnosed, diagnostics, "")
	if diagnosed[0].result.Eligible || !strings.Contains(diagnosed[0].result.Reason, "malformed candidate /tmp/broken: unreadable") {
		t.Fatalf("diagnostic did not block the task: %#v", diagnosed[0].result)
	}

	collision := []renamePlan{{result: RenameResult{Eligible: true}}}
	blockRenameTask(collision, nil, "destination exists")
	if collision[0].result.Eligible || collision[0].result.Reason != "destination exists" {
		t.Fatalf("destination collision = %#v", collision[0].result)
	}

	allGood := []renamePlan{{result: RenameResult{Eligible: true}}}
	blockRenameTask(allGood, nil, "")
	if !allGood[0].result.Eligible || allGood[0].result.Reason != "" {
		t.Fatalf("eligible task was blocked: %#v", allGood[0].result)
	}
}

func TestWtLogCovCollectAndFirstRenameReason(t *testing.T) {
	t.Parallel()
	plans := []renamePlan{
		{result: RenameResult{Repository: "acme/app"}},
		{result: RenameResult{Repository: "acme/lib", Reason: "second"}},
	}
	results := collectRenameResults(plans)
	if len(results) != 2 || results[0].Repository != "acme/app" || results[1].Reason != "second" {
		t.Fatalf("results = %#v", results)
	}
	if got := firstRenameReason(plans); got != "second" {
		t.Fatalf("firstRenameReason = %q", got)
	}
	if got := firstRenameReason([]renamePlan{{result: RenameResult{Repository: "acme/app"}}}); got != "" {
		t.Fatalf("firstRenameReason with no reasons = %q", got)
	}
}

func TestWtLogCovNormalizePreserveCachePaths(t *testing.T) {
	if paths, err := normalizePreserveCachePaths(nil); err != nil || paths != nil {
		t.Fatalf("empty paths = %#v/%v", paths, err)
	}
	paths, err := normalizePreserveCachePaths([]string{" node_modules ", "dist", "node_modules"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "dist,node_modules" {
		t.Fatalf("normalized paths = %#v", paths)
	}
	for name, value := range map[string][]string{
		"empty":     {""},
		"absolute":  {"/etc/passwd"},
		"double":    {"a//b"},
		"parent":    {"../escape"},
		"dot":       {"a/./b"},
		"unsafe":    {"a/../b"},
		"backslash": {"a\\b"},
	} {
		if _, err := normalizePreserveCachePaths(value); err == nil {
			t.Errorf("unsafe preserve path %q was accepted", name)
		}
	}
}

func TestWtLogCovNormalizeRenameOptions(t *testing.T) {
	base := RenameOptions{ProjectsRoot: t.TempDir(), OldTask: "old-task", NewTask: "new-task", WorkLog: WorkLogOptions{Model: "unknown"}}
	normalized, err := normalizeRenameOptions(base)
	if err != nil {
		t.Fatalf("valid rename options rejected: %v", err)
	}
	if normalized.OldTask != "old-task" || normalized.NewTask != "new-task" || normalized.Base != "main" || !normalized.DeleteOldBranch {
		t.Fatalf("normalized = %#v", normalized)
	}
	if normalized.WorkLog.RunID == "" {
		t.Fatal("normalization must assign a Work Log run id")
	}

	withBranch := base
	withBranch.Branch = "feature/exact"
	withBranch.ReportDir = filepath.Join(t.TempDir(), "reports", ".")
	if _, err := normalizeRenameOptions(withBranch); err != nil {
		t.Fatalf("exact branch options rejected: %v", err)
	}
	withPrefix := base
	withPrefix.BranchPrefix = "feature/"
	if _, err := normalizeRenameOptions(withPrefix); err != nil {
		t.Fatalf("branch prefix options rejected: %v", err)
	}

	mutations := map[string]func(*RenameOptions){
		"missing old task":     func(o *RenameOptions) { o.OldTask = "" },
		"unsafe new task":      func(o *RenameOptions) { o.NewTask = "../escape" },
		"same task":            func(o *RenameOptions) { o.NewTask = o.OldTask },
		"branch and prefix":    func(o *RenameOptions) { o.Branch, o.BranchPrefix = "feature/x", "feature/" },
		"empty chosen branch":  func(o *RenameOptions) { o.Branch, o.BranchChosen = "", true },
		"invalid branch":       func(o *RenameOptions) { o.Branch = "feature/../x" },
		"branch equals base":   func(o *RenameOptions) { o.Branch, o.Base = "main", "main" },
		"prefix whitespace":    func(o *RenameOptions) { o.BranchPrefix, o.BranchPrefixChosen = " feature/", true },
		"bad cache path":       func(o *RenameOptions) { o.PreserveCachePaths = []string{"../escape"} },
		"apply without remote": func(o *RenameOptions) { o.Apply = true },
		"missing model":        func(o *RenameOptions) { o.WorkLog.Model = "" },
		"invalid base":         func(o *RenameOptions) { o.Base = "bad branch name" },
	}
	for name, mutate := range mutations {
		options := base
		mutate(&options)
		if _, err := normalizeRenameOptions(options); err == nil {
			t.Errorf("invalid rename options %q were accepted", name)
		}
	}
}

func TestWtLogCovDefaultRenameReportDir(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	got := DefaultRenameReportDir("/home/wb", now)
	want := filepath.Join("/home/wb", "reports", "worktree-rename", "20260203T040506.000000000Z")
	if got != want {
		t.Fatalf("DefaultRenameReportDir = %q, want %q", got, want)
	}
}

func TestWtLogCovWriteRenameReport(t *testing.T) {
	t.Parallel()
	reportDir := filepath.Join(t.TempDir(), "reports")
	options := RenameOptions{ReportDir: reportDir, OldTask: "old", NewTask: "new", Base: "main", DeleteOldBranch: true, Apply: true}
	path, err := writeRenameReport(options, time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC), "apply",
		[]RenameResult{{Repository: "acme/app", Eligible: true, Applied: true}}, []ListDiagnostic{{Path: "/tmp/x", Message: "y"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(reportDir, "rename.json") {
		t.Fatalf("report path = %q", path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded renameReport
	if err := json.Unmarshal(contents, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Phase != "apply" || decoded.OldTask != "old" || decoded.NewTask != "new" || len(decoded.Results) != 1 || len(decoded.Diagnostics) != 1 {
		t.Fatalf("decoded report = %#v", decoded)
	}
	if !decoded.Apply || !decoded.DeleteOldBranch {
		t.Fatalf("decoded report flags = %#v", decoded)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary report file leaked: %v", err)
	}

	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeRenameReport(RenameOptions{ReportDir: blocked}, time.Now(), "plan", nil, nil); err == nil {
		t.Fatal("report into a file path was accepted")
	}
}

func TestWtLogCovRollbackAppliedRenames(t *testing.T) {
	if err := rollbackAppliedRenames(context.Background(), t.TempDir(), nil); err != nil {
		t.Fatalf("nil plans = %v", err)
	}
	plans := []renamePlan{{result: RenameResult{Applied: false, Repository: "acme/app"}}}
	if err := rollbackAppliedRenames(context.Background(), t.TempDir(), plans); err != nil {
		t.Fatalf("unapplied plans = %v", err)
	}
	plan := &renamePlan{result: RenameResult{Applied: true, Repository: "acme/app", OldBranchDeleted: true,
		OldRemoteDeleted: true, Repaired: true, PreservedCachePaths: []string{"node_modules"}}}
	resetRenameResultAfterRollback(plan)
	if plan.result.Applied || plan.result.OldBranchDeleted || plan.result.OldRemoteDeleted || plan.result.Repaired || plan.result.PreservedCachePaths != nil {
		t.Fatalf("reset result = %#v", plan.result)
	}
}

func TestWtLogCovRenamePhysicalDestinationShared(t *testing.T) {
	destinationRoot := filepath.Join(t.TempDir(), "new-root")
	plan := &renamePlan{
		destinationRoot: destinationRoot,
		entry:           ListResult{CanonicalDir: t.TempDir()},
		result:          RenameResult{NewWorktreeDir: filepath.Join(destinationRoot, "new-task", "acme", "app")},
	}
	if err := prepareRenamePhysicalDestination(context.Background(), "new-task", plan); err != nil {
		t.Fatalf("shared destination preparation failed: %v", err)
	}
	for _, relative := range []string{filepath.Join("new-task", "acme"), "new-task"} {
		if _, err := os.Stat(filepath.Join(destinationRoot, relative)); err != nil {
			t.Fatalf("shared destination %s missing: %v", relative, err)
		}
	}
	// moveWorktree owns the final no-replace publication of the checkout name.
	if _, err := os.Stat(filepath.Join(destinationRoot, "new-task", "acme", "app")); !os.IsNotExist(err) {
		t.Fatalf("shared destination must leave the repository child absent: %v", err)
	}

	// The final repository child must stay absent for moveWorktree to publish.
	if err := os.MkdirAll(filepath.Join(destinationRoot, "new-task", "acme", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareRenamePhysicalDestination(context.Background(), "new-task", plan); err == nil {
		t.Fatal("existing shared destination was accepted")
	}

	blocked := filepath.Join(t.TempDir(), "blocked-root")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareRenamePhysicalDestination(context.Background(), "new-task", &renamePlan{destinationRoot: blocked, result: RenameResult{NewWorktreeDir: filepath.Join(blocked, "new-task", "acme", "app")}}); err == nil {
		t.Fatal("file destination root was accepted")
	}
}

func TestWtLogCovPreflightRenamePhysicalDestinationShared(t *testing.T) {
	destinationRoot := filepath.Join(t.TempDir(), "new-root")
	plan := &renamePlan{destinationRoot: destinationRoot, entry: ListResult{CanonicalDir: t.TempDir()}}
	if err := preflightRenamePhysicalDestination(context.Background(), "new-task", plan); err != nil {
		t.Fatalf("shared destination preflight failed: %v", err)
	}
	entries, err := os.ReadDir(destinationRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("preflight left artifacts behind: %#v", entries)
	}
	blocked := filepath.Join(t.TempDir(), "blocked-root")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := preflightRenamePhysicalDestination(context.Background(), "new-task", &renamePlan{destinationRoot: blocked}); err == nil {
		t.Fatal("file destination root passed preflight")
	}
}

func TestWtLogCovRenamePhysicalDestinationLocal(t *testing.T) {
	fixture := newGitFixture(t)
	baseRevision := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")
	plan := &renamePlan{
		destinationLocal: true,
		destinationRoot:  filepath.Join(fixture.canonical, ".worktrees"),
		baseRevision:     baseRevision,
		entry:            ListResult{CanonicalDir: fixture.canonical},
	}
	if err := preflightRenamePhysicalDestination(context.Background(), "new-task", plan); err != nil {
		t.Fatalf("local destination preflight failed: %v", err)
	}
	if err := prepareRenamePhysicalDestination(context.Background(), "new-task", plan); err != nil {
		t.Fatalf("local destination preparation failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plan.destinationRoot, "new-task")); !os.IsNotExist(err) {
		t.Fatalf("local rename must not create the task child: %v", err)
	}
	// A second preparation must still succeed: the task child is still absent.
	if err := prepareRenamePhysicalDestination(context.Background(), "new-task", plan); err != nil {
		t.Fatalf("repeated local destination preparation failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(plan.destinationRoot, "new-task"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareRenamePhysicalDestination(context.Background(), "new-task", plan); err == nil {
		t.Fatal("existing local task child was accepted")
	}
}

func TestWtLogCovDeleteOldBranchIfSafe(t *testing.T) {
	t.Parallel()
	if deleted, reason, err := deleteOldBranchIfSafe(context.Background(), nil, "", "head", "new", "main", false); err != nil || deleted || !strings.Contains(reason, "nothing to delete") {
		t.Fatalf("empty old branch = %t/%q/%v", deleted, reason, err)
	}
	if deleted, reason, err := deleteOldBranchIfSafe(context.Background(), nil, "same", "head", "same", "main", false); err != nil || deleted || !strings.Contains(reason, "nothing to delete") {
		t.Fatalf("same old branch = %t/%q/%v", deleted, reason, err)
	}
}

func TestWtLogCovRunSecureRenameGitHelperRejectsBadArguments(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"too few":        {"a", "b", "c", "d", "e"},
		"relative root":  {"relative", "/b", "/c", "admin", "git"},
		"relative wt":    {"/a", "relative", "/c", "admin", "git"},
		"relative root2": {"/a", "/b", "relative", "admin", "git"},
		"bad admin":      {"/a", "/b", "/c", "../admin", "git"},
	} {
		if code := RunSecureRenameGitHelper(args); code != 1 {
			t.Errorf("RunSecureRenameGitHelper(%q) = %d, want 1", name, code)
		}
	}
}

func TestWtLogCovLinkedWorktreeGitFileAdminNameAndValidation(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"app-branch", "a", "with space", strings.Repeat("a", 300)} {
		if !validLinkedWorktreeAdminName(name) {
			t.Errorf("valid admin name %q was rejected", name)
		}
	}
	for _, name := range []string{"", ".", "..", "../escape", "slash/name", filepath.Join("a", "b")} {
		if validLinkedWorktreeAdminName(name) {
			t.Errorf("invalid admin name %q was accepted", name)
		}
	}
	worktree := t.TempDir()
	gitFile := filepath.Join(worktree, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /tmp/does-not-exist/worktrees/app-branch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handle, err := os.Open(gitFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	canonical := &canonicalRepository{path: "/tmp/canonical"}
	if _, err := linkedWorktreeGitFileAdminName(canonical, nil); err == nil {
		t.Fatal("nil git file was accepted")
	}
	if _, err := linkedWorktreeGitFileAdminName(canonical, handle); err == nil {
		t.Fatal("git file outside the canonical admin root was accepted")
	}
	if _, err := linkedWorktreeGitFileAdminName(&canonicalRepository{}, handle); err == nil {
		t.Fatal("canonical repository without a path was accepted")
	}

	// A well-formed pointer resolves to its admin name.
	canonicalRoot := t.TempDir()
	adminDir := filepath.Join(canonicalRoot, ".git", "worktrees", "app-branch")
	if err := os.MkdirAll(adminDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goodGitFile := filepath.Join(t.TempDir(), ".git")
	if err := os.WriteFile(goodGitFile, []byte("gitdir: "+adminDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	goodHandle, err := os.Open(goodGitFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = goodHandle.Close() }()
	name, err := linkedWorktreeGitFileAdminName(&canonicalRepository{path: canonicalRoot}, goodHandle)
	if err != nil || name != "app-branch" {
		t.Fatalf("admin name = %q err = %v", name, err)
	}

	// Non-regular and malformed pointers must be refused.
	directoryPointer := filepath.Join(t.TempDir(), ".git")
	if err := os.MkdirAll(directoryPointer, 0o755); err != nil {
		t.Fatal(err)
	}
	if handle, err := os.Open(directoryPointer); err == nil {
		if _, err := linkedWorktreeGitFileAdminName(&canonicalRepository{path: canonicalRoot}, handle); err == nil {
			t.Fatal("directory .git was accepted")
		}
		_ = handle.Close()
	}
	for name, contents := range map[string]string{
		"no prefix": adminDir + "\n",
		"relative":  "gitdir: relative/worktrees/app-branch\n",
		"unclean":   "gitdir: " + canonicalRoot + "/.git/worktrees/../worktrees/app-branch\n",
		"empty":     "gitdir: \n",
	} {
		path := filepath.Join(t.TempDir(), ".git")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		handle, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := linkedWorktreeGitFileAdminName(&canonicalRepository{path: canonicalRoot}, handle); err == nil {
			t.Errorf("malformed .git pointer %q was accepted", name)
		}
		_ = handle.Close()
	}
}
