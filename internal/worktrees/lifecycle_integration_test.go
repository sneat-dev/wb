package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These integration tests stub only hosted PR metadata. Every safety-relevant
// Git transition uses real bare remotes, clones, commits, merges, linked
// worktrees, local refs, and remote refs created under t.TempDir.
func TestListDefaultsToOfflineRealGitData(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "offline-list",
	})
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unreachable.git"))
	installFailingGitHubFixture(t)

	listed, err := List(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "offline-list",
	})
	if err != nil {
		t.Fatalf("offline list contacted a remote service: %v", err)
	}
	if len(listed) != 1 || listed[0].WorktreeDir != created[0].WorktreeDir ||
		listed[0].RemoteHeadSHA != "" || listed[0].OpenPullRequest != nil ||
		listed[0].MergedPullRequest != nil {
		t.Fatalf("offline list = %#v", listed)
	}
}

func TestCleanupMergedTaskWithRealGitData(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "cleanup-real")
	installMergedPullRequestFixture(t, head, mergedAt)
	now := mergedAt.Add(48 * time.Hour)

	listed, err := List(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-real",
		Base:         "main",
		GitHub:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !listed[0].Clean || !listed[0].LocallyMerged || listed[0].MergedPullRequest == nil {
		t.Fatalf("listed worktree = %#v", listed)
	}

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-real",
		Base:         "main",
		DeleteRemote: true,
		OlderThan:    24 * time.Hour,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || !planned.Results[0].Eligible || planned.Results[0].Applied {
		t.Fatalf("cleanup plan = %#v", planned)
	}
	if planned.ReportPath != "" {
		t.Fatalf("dry-run unexpectedly wrote report %q", planned.ReportPath)
	}
	if _, err := os.Stat(result.WorktreeDir); err != nil {
		t.Fatalf("dry-run removed worktree: %v", err)
	}
	if !gitRefExists(fixture.canonical, "refs/heads/"+result.Branch) {
		t.Fatal("dry-run removed local branch")
	}
	if got := remoteBranchForTest(t, fixture.canonical, result.Branch); got != head {
		t.Fatalf("dry-run remote branch = %q, want %q", got, head)
	}

	applied, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-real",
		Base:         "main",
		Apply:        true,
		DeleteRemote: true,
		OlderThan:    24 * time.Hour,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Results) != 1 || !applied.Results[0].Applied || !applied.Results[0].RemoteDeleted {
		t.Fatalf("applied cleanup = %#v", applied)
	}
	reportContent, err := os.ReadFile(applied.ReportPath)
	if err != nil {
		t.Fatalf("read cleanup audit report: %v", err)
	}
	var report cleanupReport
	if err := json.Unmarshal(reportContent, &report); err != nil {
		t.Fatalf("decode cleanup audit report: %v", err)
	}
	if report.Phase != "applied" || len(report.Results) != 1 ||
		report.Results[0].HeadSHA != head ||
		report.Results[0].MergedPullRequest == nil ||
		report.Results[0].MergedPullRequest.URL != "https://github.com/acme/app/pull/17" ||
		!report.Results[0].Applied {
		t.Fatalf("cleanup audit report = %#v", report)
	}
	if _, err := os.Stat(result.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after cleanup: %v", err)
	}
	if gitRefExists(fixture.canonical, "refs/heads/"+result.Branch) {
		t.Fatal("local branch still exists after cleanup")
	}
	if got := remoteBranchForTest(t, fixture.canonical, result.Branch); got != "" {
		t.Fatalf("remote branch still exists after cleanup: %s", got)
	}
}

func TestCleanupPreservesDirtyRealWorktree(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "cleanup-dirty")
	installMergedPullRequestFixture(t, head, mergedAt)
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "keep.txt"), []byte("do not remove\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanup, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-dirty",
		Base:         "main",
		Apply:        true,
		OlderThan:    time.Hour,
		Now:          func() time.Time { return mergedAt.Add(48 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanup.Results) != 1 || cleanup.Results[0].Eligible || cleanup.Results[0].Applied ||
		!strings.Contains(cleanup.Results[0].Reason, "local changes") {
		t.Fatalf("dirty cleanup result = %#v", cleanup)
	}
	if cleanup.ReportPath == "" {
		t.Fatal("destructive cleanup attempt did not write an audit report")
	}
	if _, err := os.Stat(result.WorktreeDir); err != nil {
		t.Fatalf("dirty worktree was removed: %v", err)
	}
	if !gitRefExists(fixture.canonical, "refs/heads/"+result.Branch) {
		t.Fatal("dirty worktree branch was removed")
	}
}

func TestCleanupRejectsBranchAdvancedAfterMergedPullRequest(t *testing.T) {
	fixture, result, mergedHead, mergedAt := prepareMergedTask(t, "cleanup-advanced")
	installMergedPullRequestFixture(t, mergedHead, mergedAt)
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "later.txt"), []byte("new work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "later.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "advance branch")
	gitTest(t, result.WorktreeDir, "push", "origin", result.Branch)

	cleanup, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-advanced",
		Base:         "main",
		Apply:        true,
		OlderThan:    0,
		Now:          func() time.Time { return mergedAt.Add(48 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanup.Results) != 1 || cleanup.Results[0].Eligible || cleanup.Results[0].Applied ||
		!strings.Contains(cleanup.Results[0].Reason, "no merged pull request matches") {
		t.Fatalf("advanced cleanup result = %#v", cleanup)
	}
	if _, err := os.Stat(result.WorktreeDir); err != nil {
		t.Fatalf("advanced worktree was removed: %v", err)
	}
}

func prepareMergedTask(t *testing.T, task string) (*gitFixture, CreateResult, string, time.Time) {
	t.Helper()
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    task,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "feature.txt"), []byte(task+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "feature.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "feature")
	head := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)
	gitTest(t, fixture.canonical, "merge", "--no-ff", result.Branch, "-m", "merge feature")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	return fixture, result, head, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
}

func installMergedPullRequestFixture(t *testing.T, head string, mergedAt time.Time) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := `#!/bin/sh
set -eu
if [ "$1 $2" != "pr list" ]; then
    echo "unexpected gh command: $*" >&2
    exit 2
fi
printf '[{"number":17,"url":"https://github.com/acme/app/pull/17","state":"MERGED","mergedAt":"%s","headRefOid":"%s","baseRefName":"main"}]\n' "$WB_TEST_MERGED_AT" "$WB_TEST_HEAD"
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_HEAD", head)
	t.Setenv("WB_TEST_MERGED_AT", mergedAt.Format(time.RFC3339))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installFailingGitHubFixture(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'gh must not run' >&2\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func gitRefExists(repository, reference string) bool {
	command := exec.Command("git", "-C", repository, "show-ref", "--verify", "--quiet", reference)
	return command.Run() == nil
}

func remoteBranchForTest(t *testing.T, repository, branch string) string {
	t.Helper()
	command := exec.Command("git", "-C", repository, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("read remote branch %s: %v\n%s", branch, err, output)
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return ""
	}
	if len(fields) != 2 {
		t.Fatalf("unexpected remote branch output: %q", output)
	}
	return fields[0]
}
