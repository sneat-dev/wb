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

func TestListStopsAtLegacyDirectRepositoryRootsAndRetainsValidSiblings(t *testing.T) {
	fixture := newDefaultHomeGitFixture(t)
	legacyRoot := filepath.Join(fixture.projectsRoot, ".wb", "worktrees")
	direct := filepath.Join(legacyRoot, "ci-gates", "app")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "codex/ci-gates", direct, "main")
	for _, directory := range []string{
		filepath.Join(direct, ".claude"),
		filepath.Join(direct, ".github", "workflows"),
		filepath.Join(direct, "source", "generated"),
		filepath.Join(legacyRoot, "ci-gates", "acme", "broken"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	outcome, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "ci-gates"})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].WorktreeDir != direct || outcome.Results[0].Repository != "acme/app" {
		t.Fatalf("valid direct-root inventory = %#v", outcome.Results)
	}
	if len(outcome.Diagnostics) != 1 || outcome.Diagnostics[0].Path != filepath.Join(legacyRoot, "ci-gates", "acme", "broken") {
		t.Fatalf("diagnostics = %#v, want only malformed sibling", outcome.Diagnostics)
	}
	for _, diagnostic := range outcome.Diagnostics {
		if strings.Contains(diagnostic.Path, ".claude") || strings.Contains(diagnostic.Path, ".github") || strings.Contains(diagnostic.Path, "source") {
			t.Fatalf("scanner descended below Git root: %#v", outcome.Diagnostics)
		}
	}
}

func TestListIgnoresDotDirectoriesAtEveryManagedHierarchyLevel(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "dot-directories",
	})
	if err != nil {
		t.Fatal(err)
	}
	taskRoot := filepath.Join(fixture.home, "worktrees", "dot-directories")
	for _, directory := range []string{
		filepath.Join(fixture.home, "worktrees", ".metadata"),
		filepath.Join(taskRoot, ".claude"),
		filepath.Join(taskRoot, "acme", ".github"),
		filepath.Join(created[0].WorktreeDir, ".tooling"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	outcome, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "dot-directories",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].WorktreeDir != created[0].WorktreeDir {
		t.Fatalf("dot-directory inventory = %#v", outcome.Results)
	}
	if len(outcome.Diagnostics) != 0 {
		t.Fatalf("dot directories must not block cleanup: %#v", outcome.Diagnostics)
	}
}

func TestListAndCleanupRegisteredHiddenWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	hidden := filepath.Join(fixture.home, "worktrees", "hidden-worktree", "acme", ".hidden-repo")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "codex/hidden-worktree", hidden, "main")
	if err := os.WriteFile(filepath.Join(hidden, "hidden.txt"), []byte("hidden\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, hidden, "add", "hidden.txt")
	gitTest(t, hidden, "commit", "-m", "hidden feature")
	head := gitTestOutput(t, hidden, "rev-parse", "HEAD")
	gitTest(t, hidden, "push", "-u", "origin", "codex/hidden-worktree")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "codex/hidden-worktree", "-m", "merge hidden feature")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	installMergedPullRequestFixture(t, head, mergedAt)

	listed, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "hidden-worktree",
		GitHub:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Results) != 1 || listed.Results[0].WorktreeDir != hidden || listed.Results[0].Repository != "acme/app" || len(listed.Diagnostics) != 0 {
		t.Fatalf("hidden worktree inventory = %#v", listed)
	}

	cleaned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "hidden-worktree",
		Apply:        true,
		OlderThan:    0,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned.Results) != 1 || !cleaned.Results[0].Applied {
		t.Fatalf("hidden worktree cleanup = %#v", cleaned)
	}
	if _, err := os.Stat(hidden); !os.IsNotExist(err) {
		t.Fatalf("hidden worktree remains after cleanup: %v", err)
	}
}

func TestCreateListAndCleanupCanonicalDotPrefixedRepository(t *testing.T) {
	fixture := newGitFixtureForRepository(t, ".github")
	created, err := Create(context.Background(), []string{"acme/.github"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "dot-repository",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0].WorktreeDir != filepath.Join(fixture.home, "worktrees", "dot-repository", "acme", ".github") {
		t.Fatalf("dot-prefixed repository creation = %#v", created)
	}
	worktree := created[0].WorktreeDir
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("dot repository\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "feature.txt")
	gitTest(t, worktree, "commit", "-m", "dot repository feature")
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	gitTest(t, worktree, "push", "-u", "origin", "codex/dot-repository")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "codex/dot-repository", "-m", "merge dot repository feature")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	installMergedPullRequestFixture(t, head, mergedAt)

	listed, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "dot-repository",
		GitHub:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Results) != 1 || listed.Results[0].Repository != "acme/.github" || listed.Results[0].WorktreeDir != worktree || len(listed.Diagnostics) != 0 {
		t.Fatalf("dot-prefixed repository inventory = %#v", listed)
	}

	cleaned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "dot-repository",
		Apply:        true,
		OlderThan:    0,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned.Results) != 1 || !cleaned.Results[0].Applied {
		t.Fatalf("dot-prefixed repository cleanup = %#v", cleaned)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("dot-prefixed worktree remains after cleanup: %v", err)
	}
}

func TestCleanupLegacyLayoutWritesAuditToAuthoritativeDefaultHome(t *testing.T) {
	fixture := newDefaultHomeGitFixture(t)
	legacy := filepath.Join(fixture.projectsRoot, ".wb", "worktrees", "cleanup-legacy", "acme", "app")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "codex/cleanup-legacy", legacy, "main")
	if err := os.WriteFile(filepath.Join(legacy, "feature.txt"), []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, legacy, "add", "feature.txt")
	gitTest(t, legacy, "commit", "-m", "legacy feature")
	head := gitTestOutput(t, legacy, "rev-parse", "HEAD")
	gitTest(t, legacy, "push", "-u", "origin", "codex/cleanup-legacy")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "codex/cleanup-legacy", "-m", "merge legacy feature")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	installMergedPullRequestFixture(t, head, mergedAt)

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-legacy",
		Apply:        true,
		OlderThan:    0,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("legacy cleanup = %#v", outcome)
	}
	if !strings.HasPrefix(outcome.ReportPath, filepath.Join(fixture.home, "reports")+string(filepath.Separator)) {
		t.Fatalf("cleanup report = %q, want authoritative default-home report", outcome.ReportPath)
	}
	if _, statErr := os.Stat(legacy); !os.IsNotExist(statErr) {
		t.Fatalf("legacy worktree remains after cleanup: %v", statErr)
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
	if info, statErr := os.Stat(filepath.Join(fixture.home, "worktrees", "cleanup-real")); statErr != nil || !info.IsDir() {
		t.Fatalf("empty cleanup task root was not retained safely: info=%v err=%v", info, statErr)
	}
	if gitRefExists(fixture.canonical, "refs/heads/"+result.Branch) {
		t.Fatal("local branch still exists after cleanup")
	}
	if got := remoteBranchForTest(t, fixture.canonical, result.Branch); got != "" {
		t.Fatalf("remote branch still exists after cleanup: %s", got)
	}
}

func TestCleanupReauthorizesBeforeRemoteBranchDeletion(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "cleanup-network-reauthorization")
	installMergedPullRequestFixture(t, head, mergedAt)
	movedWorktree := result.WorktreeDir + "-moved"
	external := t.TempDir()

	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-network-reauthorization",
		Apply:        true,
		DeleteRemote: true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
		beforeCleanupNetworkBranchOperation: func(worktree string) {
			if worktree != result.WorktreeDir {
				return
			}
			if renameErr := os.Rename(worktree, movedWorktree); renameErr != nil {
				t.Fatalf("move cleanup worktree before remote deletion: %v", renameErr)
			}
			if symlinkErr := os.Symlink(external, worktree); symlinkErr != nil {
				t.Fatalf("substitute cleanup worktree before remote deletion: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cleanup worktree path changed") {
		t.Fatalf("cleanup network reauthorization error = %v", err)
	}
	if got := remoteBranchForTest(t, fixture.canonical, result.Branch); got != head {
		t.Fatalf("late worktree swap deleted remote branch: got %q want %q", got, head)
	}
	if _, statErr := os.Stat(movedWorktree); statErr != nil {
		t.Fatalf("late worktree swap removed moved checkout: %v", statErr)
	}
}

func TestCleanupChildRefusesWorktreeSwapAfterFinalGitAuthorization(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "cleanup-child-authorization")
	installMergedPullRequestFixture(t, head, mergedAt)
	movedWorktree := result.WorktreeDir + "-moved"
	external := t.TempDir()

	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-child-authorization",
		Apply:        true,
		DeleteRemote: true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
		afterCleanupGitAuthorization: func(operation string) {
			if operation != "delete remote branch" {
				return
			}
			if renameErr := os.Rename(result.WorktreeDir, movedWorktree); renameErr != nil {
				t.Fatalf("move worktree after final Git authorization: %v", renameErr)
			}
			if symlinkErr := os.Symlink(external, result.WorktreeDir); symlinkErr != nil {
				t.Fatalf("substitute worktree after final Git authorization: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "worktree path changed before Git operation") {
		t.Fatalf("late cleanup Git authorization error = %v", err)
	}
	if got := remoteBranchForTest(t, fixture.canonical, result.Branch); got != head {
		t.Fatalf("late worktree swap deleted remote branch: got %q want %q", got, head)
	}
	if _, statErr := os.Stat(movedWorktree); statErr != nil {
		t.Fatalf("late worktree swap removed moved checkout: %v", statErr)
	}
}

func TestCleanupUsesRetainedCanonicalRepositoryAfterFinalAuthorization(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "cleanup-canonical-authorization")
	installMergedPullRequestFixture(t, head, mergedAt)
	movedCanonical := fixture.canonical + "-moved"
	external := t.TempDir()

	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-canonical-authorization",
		Apply:        true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
		afterCleanupGitAuthorization: func(operation string) {
			if operation != "remove worktree" {
				return
			}
			if renameErr := os.Rename(fixture.canonical, movedCanonical); renameErr != nil {
				t.Fatalf("move canonical repository after final authorization: %v", renameErr)
			}
			if symlinkErr := os.Symlink(external, fixture.canonical); symlinkErr != nil {
				t.Fatalf("substitute canonical repository after final authorization: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "canonical repository path changed before Git operation") {
		t.Fatalf("descriptor-anchored canonical cleanup error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(external, ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("replacement canonical repository was mutated: %v", statErr)
	}
	if _, statErr := os.Stat(result.WorktreeDir); statErr != nil {
		t.Fatalf("canonical substitution removed worktree despite refusal: %v", statErr)
	}
	if !gitRefExists(movedCanonical, "refs/heads/"+result.Branch) {
		t.Fatal("canonical substitution deleted feature branch despite refusal")
	}
}

func TestCleanupPreservesOwnerReplacementAfterFinalAuthorization(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "cleanup-owner-double-swap")
	installMergedPullRequestFixture(t, head, mergedAt)
	parent := filepath.Dir(result.WorktreeDir)
	parkedParent := parent + "-parked"
	replacement := "successor owner\n"
	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-owner-double-swap",
		Apply:        true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
		afterCleanupParentAuthorization: func(path string) {
			if path != parent {
				t.Fatalf("authorized parent = %s, want %s", path, parent)
			}
			if renameErr := os.Rename(parent, parkedParent); renameErr != nil {
				t.Fatalf("park authorized owner directory: %v", renameErr)
			}
			if mkdirErr := os.Mkdir(parent, 0o755); mkdirErr != nil {
				t.Fatalf("replace owner directory: %v", mkdirErr)
			}
			if writeErr := os.WriteFile(filepath.Join(parent, "keep.txt"), []byte(replacement), 0o600); writeErr != nil {
				t.Fatalf("write replacement owner sentinel: %v", writeErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cleanup worktree parent path changed") {
		t.Fatalf("owner replacement cleanup error = %v", err)
	}
	if content, readErr := os.ReadFile(filepath.Join(parent, "keep.txt")); readErr != nil || string(content) != replacement {
		t.Fatalf("owner replacement was removed: content=%q err=%v", content, readErr)
	}
	if _, statErr := os.Stat(parkedParent); statErr != nil {
		t.Fatalf("original owner directory was removed after replacement: %v", statErr)
	}
}

func TestCleanupDryRunWithExplicitReportDirDoesNotMutate(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "cleanup-dry-run-report")
	installMergedPullRequestFixture(t, head, mergedAt)
	reportDir := filepath.Join(t.TempDir(), "report")
	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-dry-run-report",
		ReportDir:    reportDir,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.ReportPath != "" {
		t.Fatalf("dry-run unexpectedly wrote report %q", outcome.ReportPath)
	}
	if _, err := os.Stat(reportDir); !os.IsNotExist(err) {
		t.Fatalf("dry-run created report directory: %v", err)
	}
	if _, err := os.Stat(result.WorktreeDir); err != nil {
		t.Fatalf("dry-run removed worktree: %v", err)
	}
}

func TestCleanupAllMergedLocksOnlyCurrentTask(t *testing.T) {
	fixture := newGitFixture(t)
	first, firstHead, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-a")
	second, secondHead, _ := prepareMergedTaskInFixture(t, fixture, "cleanup-b")
	installMergedPullRequestFixtures(t, []string{firstHead, secondHead}, mergedAt)

	secondTaskPath := filepath.Join(fixture.home, "worktrees", "cleanup-b")
	probedSecondTask := false
	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		AllMerged:    true,
		Apply:        true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
		beforeCleanupWorktreeRemoval: func(path string) {
			if path != first.WorktreeDir || probedSecondTask {
				return
			}
			probedSecondTask = true
			secondTask, openErr := openAbsoluteDirectoryNoFollow(secondTaskPath, false)
			if openErr != nil {
				t.Fatalf("open next cleanup task while first is active: %v", openErr)
			}
			defer func() { _ = secondTask.Close() }()
			lock, lockErr := acquireLockAt(secondTask)
			if lockErr != nil {
				t.Fatalf("next cleanup task was locked before it was processed: %v", lockErr)
			}
			lock.release()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !probedSecondTask {
		t.Fatal("cleanup did not process the first task")
	}
	if len(outcome.Results) != 2 || !outcome.Results[0].Applied || !outcome.Results[1].Applied {
		t.Fatalf("all-merged cleanup outcome = %#v", outcome)
	}
	for _, path := range []string{first.WorktreeDir, second.WorktreeDir} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("cleaned worktree remains at %s: %v", path, statErr)
		}
	}
}

func TestCleanupRefusesTaskSwapBeforeLockWithoutMutatingExternalTarget(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "cleanup-lock-swap")
	installMergedPullRequestFixture(t, head, mergedAt)
	taskRoot := filepath.Join(fixture.home, "worktrees", "cleanup-lock-swap")
	movedTaskRoot := taskRoot + "-moved"
	external := t.TempDir()

	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-lock-swap",
		Apply:        true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
		beforeCleanupLocks: func() {
			if renameErr := os.Rename(taskRoot, movedTaskRoot); renameErr != nil {
				t.Fatalf("move cleanup task before lock: %v", renameErr)
			}
			if symlinkErr := os.Symlink(external, taskRoot); symlinkErr != nil {
				t.Fatalf("substitute cleanup task before lock: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "open cleanup task") {
		t.Fatalf("cleanup task-swap-before-lock error = %v", err)
	}
	if entries, readErr := os.ReadDir(external); readErr != nil || len(entries) != 0 {
		t.Fatalf("external cleanup task target was mutated: entries=%v err=%v", entries, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(movedTaskRoot, "acme", "app")); statErr != nil {
		t.Fatalf("moved cleanup worktree was removed: %v", statErr)
	}
	if !gitRefExists(fixture.canonical, "refs/heads/"+result.Branch) {
		t.Fatal("cleanup task-swap-before-lock removed feature branch")
	}
}

func TestCleanupRefusesWorktreeSwapBeforeRemovalWithoutMutatingExternalTarget(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "cleanup-removal-swap")
	installMergedPullRequestFixture(t, head, mergedAt)
	movedWorktree := result.WorktreeDir + "-moved"
	external := t.TempDir()

	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-removal-swap",
		Apply:        true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
		beforeCleanupWorktreeRemoval: func(worktree string) {
			if worktree != result.WorktreeDir {
				return
			}
			if renameErr := os.Rename(worktree, movedWorktree); renameErr != nil {
				t.Fatalf("move cleanup worktree before removal: %v", renameErr)
			}
			if symlinkErr := os.Symlink(external, worktree); symlinkErr != nil {
				t.Fatalf("substitute cleanup worktree before removal: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cleanup worktree path changed") {
		t.Fatalf("cleanup worktree-swap-before-removal error = %v", err)
	}
	if entries, readErr := os.ReadDir(external); readErr != nil || len(entries) != 0 {
		t.Fatalf("external cleanup worktree target was mutated: entries=%v err=%v", entries, readErr)
	}
	if _, statErr := os.Stat(movedWorktree); statErr != nil {
		t.Fatalf("moved cleanup worktree was removed: %v", statErr)
	}
	if !gitRefExists(fixture.canonical, "refs/heads/"+result.Branch) {
		t.Fatal("cleanup worktree-swap-before-removal removed feature branch")
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
	result, head, mergedAt := prepareMergedTaskInFixture(t, fixture, task)
	return fixture, result, head, mergedAt
}

func prepareMergedTaskInFixture(t *testing.T, fixture *gitFixture, task string) (CreateResult, string, time.Time) {
	t.Helper()
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
	return result, head, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
}

func installMergedPullRequestFixture(t *testing.T, head string, mergedAt time.Time) {
	installMergedPullRequestFixtures(t, []string{head}, mergedAt)
}

func installMergedPullRequestFixtures(t *testing.T, heads []string, mergedAt time.Time) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := `#!/bin/sh
set -eu
if [ "$1 $2" != "pr list" ]; then
    echo "unexpected gh command: $*" >&2
    exit 2
fi
printf '%s\n' "$WB_TEST_MERGED_PULLS"
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	pulls := make([]map[string]any, 0, len(heads))
	for index, head := range heads {
		pulls = append(pulls, map[string]any{
			"number":      index + 17,
			"url":         "https://github.com/acme/app/pull/" + string(rune('A'+index)),
			"state":       "MERGED",
			"mergedAt":    mergedAt.Format(time.RFC3339),
			"headRefOid":  head,
			"baseRefName": "main",
		})
	}
	payload, err := json.Marshal(pulls)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_MERGED_PULLS", string(payload))
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
