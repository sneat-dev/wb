package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

// These integration tests stub only hosted PR metadata. Every safety-relevant
// Git transition uses real bare remotes, clones, commits, merges, linked
// worktrees, local refs, and remote refs created under t.TempDir.
func TestListDefaultsToOfflineRealGitData(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "offline-list", WorkLog: WorkLogOptions{Model: "unknown"},
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

func TestLogicalCleanupTaskResolvesSessionResumeNamespace(t *testing.T) {
	fixture := newGitFixture(t)
	physical := "session-resume-resume-abc-m-001-abcdef01"
	worktree := filepath.Join(fixture.home, "worktrees", physical, "acme", "app")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "wb-session/resume-abc-m-001-abcdef01", worktree, "main")
	manifest := newCreatedManifest("logical-session-effort")
	manifest.Worktree = worktree
	manifest.Repository = "acme/app"
	manifest.Branch = "wb-session/resume-abc-m-001-abcdef01"
	if err := WriteManifest(worktree, manifest); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveLogicalCleanupTasks([]wbhome.Layout{{WorktreesRoot: filepath.Join(fixture.home, "worktrees")}}, []string{"logical-session-effort"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0] != physical {
		t.Fatalf("resolved logical cleanup task = %#v, want %q", resolved, physical)
	}
}

func TestLogicalCleanupTaskResolvesRepositoryLocalSessionResumeNamespace(t *testing.T) {
	fixture := newGitFixture(t)
	physical := "session-resume-local-abc-m-001-abcdef01"
	root := filepath.Join(fixture.canonical, ".worktrees")
	worktree := filepath.Join(root, physical)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := newCreatedManifest("logical-local-session-effort")
	manifest.Worktree = worktree
	manifest.Repository = "acme/app"
	if err := WriteManifest(worktree, manifest); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveLogicalCleanupTasks([]wbhome.Layout{{WorktreesRoot: root, Local: true}}, []string{"logical-local-session-effort"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0] != physical {
		t.Fatalf("resolved local logical cleanup task = %#v, want %q", resolved, physical)
	}
}

func TestCleanupLogicalSessionResumeApplyReportsPhysicalResolvedTasks(t *testing.T) {
	const logical = "logical-session-cleanup"
	const physical = "session-resume-resume-apply-m-001-abcdef01"
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    physical,
		WorkLog:      WorkLogOptions{EffortID: logical, Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "feature.txt"), []byte(logical+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "feature.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "feature")
	head := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)
	gitTest(t, fixture.canonical, "merge", "--no-ff", result.Branch, "-m", "merge feature")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	installMergedPullRequestFixture(t, head, mergedAt)
	now := mergedAt.Add(48 * time.Hour)
	relocatedRemote := filepath.Join(fixture.canonical, ".wb-test-remote.git")
	if err := os.Rename(fixture.remote, relocatedRemote); err != nil {
		t.Fatalf("relocate fixture remote under an authorized cleanup write root: %v", err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", relocatedRemote)

	applied, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         logical,
		Base:         "main",
		Apply:        true,
		DeleteRemote: true,
		OlderThan:    24 * time.Hour,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.ResolvedTasks) != 1 || applied.ResolvedTasks[0] != physical {
		t.Fatalf("resolved tasks = %#v, want %q", applied.ResolvedTasks, physical)
	}
	if len(applied.Results) != 1 || applied.Results[0].Task != physical || !applied.Results[0].Applied ||
		!applied.Results[0].WorktreeGone || !applied.Results[0].BranchDeleted {
		t.Fatalf("logical session-resume cleanup = %#v", applied)
	}
	if _, err := os.Stat(result.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("session-resume worktree still exists after logical cleanup: %v", err)
	}
}

func TestLogicalCleanupTaskResolvesAllSessionResumeMembers(t *testing.T) {
	fixture := newGitFixture(t)
	physicalTasks := []string{
		"session-resume-resume-multi-m-002-bbbbbbbb",
		"session-resume-resume-multi-m-001-aaaaaaaa",
	}
	for index, physical := range physicalTasks {
		worktree := filepath.Join(fixture.home, "worktrees", physical, "acme", "app")
		branch := fmt.Sprintf("wb-session/resume-multi-m-%03d", index+1)
		gitTest(t, fixture.canonical, "worktree", "add", "-b", branch, worktree, "main")
		manifest := newCreatedManifest("logical-multi-member-effort")
		manifest.Worktree = worktree
		manifest.Repository = "acme/app"
		manifest.Branch = branch
		if err := WriteManifest(worktree, manifest); err != nil {
			t.Fatal(err)
		}
	}

	resolved, err := resolveLogicalCleanupTasks([]wbhome.Layout{{WorktreesRoot: filepath.Join(fixture.home, "worktrees")}}, []string{"logical-multi-member-effort"})
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string(nil), physicalTasks...)
	sort.Strings(want)
	if !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolved logical cleanup tasks = %#v, want %#v", resolved, want)
	}
}

func TestListStopsAtLegacyDirectRepositoryRootsAndRetainsValidSiblings(t *testing.T) {
	fixture := newDefaultHomeGitFixture(t)
	legacyRoot := filepath.Join(fixture.projectsRoot, ".wb", "worktrees")
	direct := filepath.Join(legacyRoot, "ci-gates", "app")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/ci-gates", direct, "main")
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
		Operation:    "dot-directories", WorkLog: WorkLogOptions{Model: "unknown"},
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
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/hidden-worktree", hidden, "main")
	if err := os.WriteFile(filepath.Join(hidden, "hidden.txt"), []byte("hidden\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, hidden, "add", "hidden.txt")
	gitTest(t, hidden, "commit", "-m", "hidden feature")
	head := gitTestOutput(t, hidden, "rev-parse", "HEAD")
	gitTest(t, hidden, "push", "-u", "origin", "feature/hidden-worktree")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "feature/hidden-worktree", "-m", "merge hidden feature")
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

func TestCleanupResumesExactBranchAfterFailureFollowingWorktreeRemoval(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "cleanup-resume-after-remove")
	installMergedPullRequestFixture(t, head, mergedAt)
	injected := errors.New("injected crash after worktree removal")
	first, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-resume-after-remove",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now:                         func() time.Time { return mergedAt.Add(time.Hour) },
		afterCleanupWorktreeRemoval: func(string) error { return injected },
	})
	if !errors.Is(err, injected) {
		t.Fatalf("cleanup interruption = %v, want %v", err, injected)
	}
	if len(first.Results) != 1 || !first.Results[0].WorktreeGone || first.Results[0].BranchDeleted || first.Results[0].BacklogID == "" {
		t.Fatalf("interrupted cleanup result = %#v", first.Results)
	}
	if _, statErr := os.Stat(created.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("interrupted cleanup worktree remains: %v", statErr)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created.Branch); branchErr != nil || !exists {
		t.Fatalf("interrupted cleanup branch exists=%t err=%v", exists, branchErr)
	}
	if remoteHead, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, created.Branch); remoteErr != nil || remoteHead != "" {
		t.Fatalf("interrupted cleanup remote head=%q err=%v", remoteHead, remoteErr)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.home, "worktrees", "cleanup-resume-after-remove")); statErr != nil {
		t.Fatalf("interrupted cleanup must retain its logical task namespace for backlog recovery: %v", statErr)
	}

	resumed, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-resume-after-remove",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(2 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Results) != 1 || !resumed.Results[0].Applied || !resumed.Results[0].WorktreeGone || !resumed.Results[0].BranchDeleted || resumed.Results[0].BacklogID == "" {
		t.Fatalf("resumed cleanup = %#v", resumed.Results)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created.Branch); branchErr != nil || exists {
		t.Fatalf("resumed cleanup branch exists=%t err=%v", exists, branchErr)
	}
	var record lifecycleBacklogRecord
	content, readErr := os.ReadFile(filepath.Join(lifecycleBacklogDirectory(fixture.home), resumed.Results[0].BacklogID+".json"))
	if readErr != nil || json.Unmarshal(content, &record) != nil || record.Stage != lifecycleStageComplete {
		t.Fatalf("completed cleanup backlog = %#v read=%v", record, readErr)
	}
}

// TestCleanupResumedBacklogReportsRemoteDeletedTruthfully is the regression
// for the founder's cleanup.json for task wb-ops-journal: remote_deleted
// read false even though `git fetch origin --prune` and `git ls-remote`
// both proved the remote branch was genuinely gone. The remote branch is
// deleted before the worktree is removed (retiring_remote precedes
// removing_worktree in the backlog stage sequence), so a crash injected
// right after worktree removal — exactly
// TestCleanupResumesExactBranchAfterFailureFollowingWorktreeRemoval's
// fixture — leaves a durable backlog record whose remote deletion already
// happened in the interrupted run. resumeLifecycleBacklog never deletes a
// remote branch itself; it only proceeds once a fresh `git ls-remote`
// already shows the branch gone, and this backlog record's non-empty
// RemoteHeadSHA proves one existed at seal time. Before the fix, the
// backlog-resume path in Cleanup never set RemoteDeleted on the resumed
// result, so it silently reported false for a task whose remote branch was
// provably deleted — an audit trail that under-claims what WB did.
func TestCleanupResumedBacklogReportsRemoteDeletedTruthfully(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "cleanup-resume-remote-deleted")
	installMergedPullRequestFixture(t, head, mergedAt)
	injected := errors.New("injected crash after worktree removal")
	first, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-resume-remote-deleted",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now:                         func() time.Time { return mergedAt.Add(time.Hour) },
		afterCleanupWorktreeRemoval: func(string) error { return injected },
	})
	if !errors.Is(err, injected) {
		t.Fatalf("cleanup interruption = %v, want %v", err, injected)
	}
	if len(first.Results) != 1 || !first.Results[0].RemoteDeleted {
		t.Fatalf("interrupted cleanup must have already deleted the remote branch: %#v", first.Results)
	}
	if remoteHead, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, created.Branch); remoteErr != nil || remoteHead != "" {
		t.Fatalf("interrupted cleanup remote head=%q err=%v, want the remote branch already gone", remoteHead, remoteErr)
	}

	resumed, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-resume-remote-deleted",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(2 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Results) != 1 || !resumed.Results[0].Applied {
		t.Fatalf("resumed cleanup = %#v", resumed.Results)
	}
	if !resumed.Results[0].RemoteDeleted {
		t.Fatalf("resumed cleanup report claims remote_deleted=false for %s, but the remote branch was genuinely deleted", created.Branch)
	}
	if resumed.ReportPath == "" {
		t.Fatal("resumed cleanup wrote no report")
	}
	reportContent, err := os.ReadFile(resumed.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reportContent), `"remote_deleted": true`) {
		t.Fatalf("cleanup.json report does not claim remote_deleted: true:\n%s", reportContent)
	}
}

func TestCleanupCompletesWhenInvokedFromWorktreeBeingRemoved(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "cleanup-from-removed-cwd")
	installMergedPullRequestFixture(t, head, mergedAt)
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(created.WorktreeDir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if chdirErr := os.Chdir(originalDirectory); chdirErr != nil {
			t.Errorf("restore test directory: %v", chdirErr)
		}
	}()

	cleaned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-from-removed-cwd",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned.Results) != 1 || !cleaned.Results[0].Applied || !cleaned.Results[0].WorktreeGone || !cleaned.Results[0].BranchDeleted {
		t.Fatalf("cleanup from removed cwd = %#v", cleaned.Results)
	}
}

func TestCreateListAndCleanupCanonicalDotPrefixedRepository(t *testing.T) {
	fixture := newGitFixtureForRepository(t, ".github")
	created, err := Create(context.Background(), []string{"acme/.github"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "dot-repository", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0].WorktreeDir != filepath.Join(fixture.canonical, ".worktrees", "dot-repository") {
		t.Fatalf("dot-prefixed repository creation = %#v", created)
	}
	worktree := created[0].WorktreeDir
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("dot repository\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "feature.txt")
	gitTest(t, worktree, "commit", "-m", "dot repository feature")
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	gitTest(t, worktree, "push", "-u", "origin", created[0].Branch)
	gitTest(t, fixture.canonical, "merge", "--no-ff", created[0].Branch, "-m", "merge dot repository feature")
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
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/cleanup-legacy", legacy, "main")
	if err := os.WriteFile(filepath.Join(legacy, "feature.txt"), []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, legacy, "add", "feature.txt")
	gitTest(t, legacy, "commit", "-m", "legacy feature")
	head := gitTestOutput(t, legacy, "rev-parse", "HEAD")
	gitTest(t, legacy, "push", "-u", "origin", "feature/cleanup-legacy")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "feature/cleanup-legacy", "-m", "merge legacy feature")
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

	// Deleting the remote branch runs the actual push under WB's sandboxed Git
	// capability, whose write roots are the canonical repository and the
	// cleanup worktree's parent — never an arbitrary "origin" URL, since a real
	// origin is GitHub over the network and never needs local filesystem
	// authority. This fixture's origin is a local bare repository standing in
	// for GitHub, so it has to live under an authorized root for this one
	// push to succeed. The worktree parent is out, since Cleanup's own
	// managed-worktree inventory rejects any entry there that isn't a Git
	// worktree root; nest it inside the canonical repository instead, which
	// carries no such expectation.
	relocatedRemote := filepath.Join(fixture.canonical, ".wb-test-remote.git")
	if err := os.Rename(fixture.remote, relocatedRemote); err != nil {
		t.Fatalf("relocate fixture remote under an authorized cleanup write root: %v", err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", relocatedRemote)

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
	if _, statErr := os.Stat(filepath.Join(fixture.home, "worktrees", "cleanup-real")); !os.IsNotExist(statErr) {
		t.Fatalf("emptied cleanup task root was not retired: %v", statErr)
	}
	if gitRefExists(fixture.canonical, "refs/heads/"+result.Branch) {
		t.Fatal("local branch still exists after cleanup")
	}
	if got := remoteBranchForTest(t, fixture.canonical, result.Branch); got != "" {
		t.Fatalf("remote branch still exists after cleanup: %s", got)
	}
}

func TestCleanupRecoversMergedPRTargetAfterRecordedTargetDeleted(t *testing.T) {
	fixture := newGitFixture(t)
	// Model a checkout created against a feature target. Its target PR then
	// lands in main and GitHub deletes that source ref, while the checkout's
	// own immutable manifest still records the deleted target.
	gitTest(t, fixture.canonical, "branch", "deleted-target", "main")
	gitTest(t, fixture.canonical, "push", "origin", "deleted-target")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "cleanup-deleted-recorded-target",
		Base:         "deleted-target",
		WorkLog:      WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "feature.txt"), []byte("landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "feature.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "feature")
	head := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)
	gitTest(t, fixture.canonical, "merge", "--no-ff", result.Branch, "-m", "merge feature")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	gitTest(t, fixture.canonical, "push", "origin", ":deleted-target")
	if got := remoteBranchForTest(t, fixture.canonical, "deleted-target"); got != "" {
		t.Fatalf("deleted recorded target still exists remotely at %s", got)
	}
	mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	installMergedPullRequestFixture(t, head, mergedAt)

	listed, err := List(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-deleted-recorded-target",
		Base:         "main",
		GitHub:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Base != "main" || !listed[0].IntegratedAtOrigin ||
		listed[0].MergedPullRequest == nil || listed[0].MergedPullRequest.HeadSHA != head {
		t.Fatalf("merged PR target recovery = %#v", listed)
	}

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-deleted-recorded-target",
		Base:         "main",
		OlderThan:    0,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || !planned.Results[0].Eligible || planned.Results[0].Base != "main" {
		t.Fatalf("cleanup plan after deleted recorded target = %#v", planned)
	}
}

func TestMergedPullRequestTargetRequiresUnambiguousExactHead(t *testing.T) {
	mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	const head = "0123456789012345678901234567890123456789"
	merged := func(base, sha string) githubPullRequest {
		return githubPullRequest{
			Base:     githubRef{Ref: base},
			Head:     githubRef{SHA: sha},
			MergedAt: &mergedAt,
		}
	}
	if target, ok := mergedPullRequestTarget(context.Background(), []githubPullRequest{merged("main", head)}, head, "deleted-target"); !ok || target != "main" {
		t.Fatalf("exact merged PR target = %q, %t; want main, true", target, ok)
	}
	if target, ok := mergedPullRequestTarget(context.Background(), []githubPullRequest{
		merged("main", head), merged("release", head),
	}, head, "deleted-target"); ok || target != "" {
		t.Fatalf("ambiguous merged PR target = %q, %t; want empty, false", target, ok)
	}
	if target, ok := mergedPullRequestTarget(context.Background(), []githubPullRequest{merged("main", "unrelated")}, head, "deleted-target"); ok || target != "" {
		t.Fatalf("unrelated merged PR target = %q, %t; want empty, false", target, ok)
	}
}

// TestCleanupRetiresTaskNamespaceResidueOnTerminalApply is the regression
// for the founder's fleet audit: after an otherwise fully successful
// `wb worktree cleanup <task> --apply --remote` (worktree removed, local
// branch deleted, remote branch deleted, integration confirmed), the task
// directory survived containing an empty owner-namespace directory (for
// example acme/) and a `.wb-retired-lock-<32hex>` file — residue found,
// fleet-wide, under 626 of 755 task directories with no real checkout left
// under them. A terminal cleanup must leave nothing in its own task
// namespace: the owner directory is retired once its last repository's
// cleanup applies (removeEmptyParent), and the operation lock this cleanup
// transaction just released is purged once the task directory holds nothing
// but retired locks (purgeTerminalTaskLockDebris). The task root itself is
// still retained (see TestCleanupMergedTaskWithRealGitData) — only its
// contents must be gone.
func TestCleanupRetiresTaskNamespaceResidueOnTerminalApply(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "cleanup-residue")
	installMergedPullRequestFixture(t, head, mergedAt)
	now := mergedAt.Add(48 * time.Hour)

	// See TestCleanupMergedTaskWithRealGitData: the sandboxed Git capability
	// used for the actual remote push only authorizes writes under the
	// canonical repository and the cleanup worktree's parent, so the fixture
	// remote is relocated there for this one push to succeed.
	relocatedRemote := filepath.Join(fixture.canonical, ".wb-test-remote.git")
	if err := os.Rename(fixture.remote, relocatedRemote); err != nil {
		t.Fatalf("relocate fixture remote under an authorized cleanup write root: %v", err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", relocatedRemote)

	applied, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-residue",
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
	if _, err := os.Stat(result.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after cleanup: %v", err)
	}

	// The task root itself is residue too, and is now retired with everything
	// under it. It used to be retained deliberately, because removing it after
	// releasing its lock let a concurrent create build an unreachable task
	// directory at the same pathname; that race is now refused by every
	// operation instead (see TestOperationLockRefusesRetiredDirectory), so the
	// namespace no longer has to survive as an empty shell.
	taskDir := filepath.Join(fixture.home, "worktrees", "cleanup-residue")
	if _, statErr := os.Stat(taskDir); !os.IsNotExist(statErr) {
		entries, _ := os.ReadDir(taskDir)
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name()
		}
		t.Fatalf("terminal task namespace was not retired: err=%v holding=%v", statErr, names)
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
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	result, head, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-canonical-authorization")
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
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	result, head, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-owner-double-swap")
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

// A sweep must not retain every task's lock for its whole duration: it takes a
// task's lock when it reaches that task and gives it back when the task is
// done. The probe below reads the *next* task's lock while the current one is
// mid-removal, which only says anything about retention while apply is serial
// — with workers the next task may legitimately be held by one of them, or
// already finished. Pin the serial sweep here, where the property is exactly
// expressible, and assert the parallel ceiling separately in
// TestCleanupAllMergedNeverExceedsTheWorkerCeiling.
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
		Workers:      1,
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
			lock, lockErr := acquireLockAt(secondTask, "second-task")
			if lockErr != nil {
				t.Fatalf("next cleanup task was locked before it was processed: %v", lockErr)
			}
			_ = lock.release()
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
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	result, head, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-lock-swap")
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

func TestCleanupAcceptsExactDirectPushIntegrationWithoutPullRequest(t *testing.T) {
	fixture, result, head, _ := prepareMergedTask(t, "cleanup-direct-push")
	installMergedPullRequestFixtures(t, nil, time.Time{})

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "cleanup-direct-push",
		Base:         "main",
		OlderThan:    24 * time.Hour,
		Now:          func() time.Time { return time.Date(2026, time.July, 3, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || !planned.Results[0].Eligible || planned.Results[0].Applied {
		t.Fatalf("direct-push cleanup plan = %#v", planned)
	}
	if !planned.Results[0].IntegratedAtOrigin || planned.Results[0].RemoteTargetSHA == "" {
		t.Fatalf("direct-push remote evidence = %#v", planned.Results[0].ListResult)
	}
	if planned.Results[0].HeadSHA != head || planned.Results[0].WorktreeDir != result.WorktreeDir {
		t.Fatalf("direct-push cleanup identity = %#v", planned.Results[0].ListResult)
	}
}

func TestCleanupAcceptsExactRebaseMergedPullRequestReceipt(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "cleanup-rebase-receipt", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "feature.txt"), []byte("rebase merge\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "feature.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "feature source")
	head := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)

	// Model GitHub's rebase result: it has the source tree, but is a distinct
	// commit and therefore the source tip is not its ancestor.
	tree := gitTestOutput(t, fixture.canonical, "rev-parse", head+"^{tree}")
	mergeSHA := gitTestOutput(t, fixture.canonical, "commit-tree", tree, "-p", "main", "-m", "rebase feature")
	gitTest(t, fixture.canonical, "update-ref", "refs/heads/main", mergeSHA)
	gitTest(t, fixture.canonical, "push", "origin", "main")
	if merged, err := isAncestor(context.Background(), fixture.canonical, head, mergeSHA); err != nil || merged {
		t.Fatalf("source must not be an ancestor of rebase result: merged=%t err=%v", merged, err)
	}
	mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	installMergedPullRequestFixtureWithMerge(t, head, mergeSHA, mergedAt)

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-rebase-receipt", OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || !planned.Results[0].Eligible || !planned.Results[0].IntegratedAtOrigin || !planned.Results[0].RebaseMergedAtOrigin {
		t.Fatalf("rebase receipt cleanup plan = %#v", planned)
	}
	if planned.Results[0].MergedPullRequest == nil || planned.Results[0].MergedPullRequest.MergeSHA != mergeSHA {
		t.Fatalf("rebase receipt PR evidence = %#v", planned.Results[0].MergedPullRequest)
	}
}

func TestCleanupRejectsRebaseReceiptWhoseMergeTreeDiffersFromSource(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "cleanup-rebase-tree-mismatch", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "source.txt"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "source.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "source feature")
	head := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)
	if err := os.WriteFile(filepath.Join(fixture.canonical, "other.txt"), []byte("different target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "other.txt")
	gitTest(t, fixture.canonical, "commit", "-m", "unrelated target")
	mergeSHA := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	installMergedPullRequestFixtureWithMerge(t, head, mergeSHA, mergedAt)

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-rebase-tree-mismatch", OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || planned.Results[0].Eligible || planned.Results[0].IntegratedAtOrigin || planned.Results[0].RebaseMergedAtOrigin ||
		!strings.Contains(planned.Results[0].Reason, "awaiting push") {
		t.Fatalf("tree-mismatched rebase receipt must be rejected: %#v", planned)
	}
}

func TestCleanupRejectsMergedPullRequestWithoutTargetIntegration(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "cleanup-unintegrated-merged-pr", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "feature.txt"), []byte("not landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "feature.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "unintegrated source")
	head := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)
	mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	installMergedPullRequestFixture(t, head, mergedAt)

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-unintegrated-merged-pr", OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || planned.Results[0].Eligible || planned.Results[0].IntegratedAtOrigin ||
		!strings.Contains(planned.Results[0].Reason, "awaiting push") {
		t.Fatalf("merged PR without target integration must be rejected: %#v", planned)
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
		!strings.Contains(cleanup.Results[0].Reason, "awaiting push") {
		t.Fatalf("advanced cleanup result = %#v", cleanup)
	}
	if _, err := os.Stat(result.WorktreeDir); err != nil {
		t.Fatalf("advanced worktree was removed: %v", err)
	}
}

// TestCleanupFilterExcludesMismatchedCandidateOutsideSelection is the
// regression test for the "matchups renamed to competios" defect: a
// worktree whose on-disk repository-name segment no longer matches its
// canonical clone (the leftover of a real GitHub repository rename) must not
// block cleanup of an unrelated, unfiltered task elsewhere in the fleet. The
// malformed candidate here belongs to a different task and a different
// canonical repository than --filter selects, so the fix under test is that
// --filter scopes what gets validated, not merely what gets acted on: the
// mismatched candidate must never even surface as a diagnostic.
func TestCleanupFilterExcludesMismatchedCandidateOutsideSelection(t *testing.T) {
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	result, head, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-filter-in-scope")
	installMergedPullRequestFixture(t, head, mergedAt)
	stale := createMismatchedWorktree(t, fixture, "cleanup-filter-stale", "acme", "renamed-repo", "old-repo-name")

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		AllMerged:    true,
		Apply:        true,
		Filter:       "acme/app", // matches only the valid merged task's repository.
		OlderThan:    0,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("cleanup outside the malformed candidate's selection must not fail: %v", err)
	}
	if len(outcome.Diagnostics) != 0 {
		t.Fatalf("out-of-filter malformed candidate leaked into diagnostics: %#v", outcome.Diagnostics)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("in-filter task was not cleaned up: %#v", outcome.Results)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("in-filter worktree remains after cleanup: %v", statErr)
	}
	if _, statErr := os.Stat(stale); statErr != nil {
		t.Fatalf("out-of-filter malformed worktree was touched: %v", statErr)
	}
}

func TestCleanupExactRepositoryCannotActOnAnotherRepository(t *testing.T) {
	const task = "cleanup-exact-repository"
	fixture, result, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)

	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot:    fixture.projectsRoot,
		Task:            task,
		Base:            "main",
		ExactRepository: "acme/other",
		Apply:           true,
		DeleteRemote:    true,
		OlderThan:       0,
		Now:             func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err == nil || !strings.Contains(err.Error(), "has no repository") {
		t.Fatalf("wrong exact-repository cleanup error = %v", err)
	}
	if _, statErr := os.Stat(result.WorktreeDir); statErr != nil {
		t.Fatalf("wrong exact-repository cleanup touched selected task worktree: %v", statErr)
	}

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot:    fixture.projectsRoot,
		Task:            task,
		Base:            "main",
		ExactRepository: "acme/app",
		Apply:           true,
		DeleteRemote:    true,
		OlderThan:       0,
		Now:             func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Repository != "acme/app" || !outcome.Results[0].Applied {
		t.Fatalf("exact-repository cleanup outcome = %#v", outcome.Results)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("exact selected worktree remains after cleanup: %v", statErr)
	}
}

// An empty retired stage is terminal debris, not cleanup backlog. It is purged
// on the read path itself — before any plan is built, dry run included — so it
// can never become a per-invocation `info:` line again, and it can never keep a
// task that is otherwise finished from finishing. One workstation carried 55 of
// them, printing 55 lines before every `wb worktree list` for 220 KB of empty
// directories, because their removal was coupled to cleaning the task and a task
// that is never cleanable keeps its artefacts forever.
func TestReadPathPurgesExactEmptyRetiredStageWithoutPoisoningFilteredRepository(t *testing.T) {
	const task = "cleanup-retired-stage"
	fixture, result, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	retired := filepath.Join(fixture.home, "worktrees", task, ".wb-retired-stage-6b0995eef65f84dace22d24df2644b32")
	if err := os.Mkdir(retired, 0o700); err != nil {
		t.Fatal(err)
	}

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         task,
		Filter:       "acme/app",
		DeleteRemote: true,
		OlderThan:    0,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || !planned.Results[0].Eligible {
		t.Fatalf("retired stage poisoned selected repository plan: %#v", planned.Results)
	}
	for _, artifact := range planned.Artifacts {
		if artifact.Path == retired {
			t.Fatalf("an empty retired stage must not survive into a plan as backlog: %#v", artifact)
		}
	}
	var purged *PurgedArtefact
	for index := range planned.Purged {
		if planned.Purged[index].Path == retired {
			purged = &planned.Purged[index]
		}
	}
	if purged == nil || purged.Kind != purgedRetiredStage {
		t.Fatalf("purge receipt = %#v", planned.Purged)
	}
	if _, statErr := os.Stat(retired); !os.IsNotExist(statErr) {
		t.Fatalf("retired stage remains in active task: %v", statErr)
	}

	applied, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         task,
		Filter:       "acme/app",
		Apply:        true,
		DeleteRemote: true,
		OlderThan:    0,
		Now:          func() time.Time { return mergedAt.Add(2 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Results) != 1 || !applied.Results[0].Applied {
		t.Fatalf("selected repository was not cleaned: %#v", applied.Results)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("selected worktree remains after terminal cleanup: %v", statErr)
	}
}

func TestCleanupKeepsNonEmptyRetiredStageAsExplicitBlockingBacklog(t *testing.T) {
	const task = "cleanup-retired-stage-backlog"
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	result, head, mergedAt := prepareMergedTaskInFixture(t, fixture, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	retired := filepath.Join(fixture.home, "worktrees", task, ".wb-retired-stage-nonempty")
	if err := os.Mkdir(retired, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "evidence"), []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         task,
		Filter:       "acme/app",
		Apply:        true,
		DeleteRemote: true,
		OlderThan:    0,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	var blocking *LifecycleArtifact
	for index := range outcome.Artifacts {
		if outcome.Artifacts[index].Path == retired {
			blocking = &outcome.Artifacts[index]
		}
	}
	if blocking == nil || blocking.Eligible ||
		blocking.Disposition != "cleanup_backlog" ||
		!strings.Contains(blocking.Reason, "non-empty") {
		t.Fatalf("non-empty retired stage was not explicit backlog: %#v", outcome.Artifacts)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Eligible || outcome.Results[0].Applied ||
		!strings.Contains(outcome.Results[0].Reason, "lifecycle artifact cleanup backlog") {
		t.Fatalf("non-empty retired stage did not block coordinated task: %#v", outcome.Results)
	}
	if _, statErr := os.Stat(retired); statErr != nil {
		t.Fatalf("blocking retired-stage evidence was touched: %v", statErr)
	}
	if _, statErr := os.Stat(result.WorktreeDir); statErr != nil {
		t.Fatalf("worktree was removed despite artifact backlog: %v", statErr)
	}
}

// A task holding nothing but an empty retired stage is two pieces of terminal
// debris stacked on each other. The stage is purged on the read path, and the
// namespace that then holds nothing is retired by cleanup as an empty task.
func TestCleanupRetiresATaskLeftHoldingOnlyAPurgedStage(t *testing.T) {
	fixture := newGitFixture(t)
	const task = "artifact-only-empty"
	taskPath := filepath.Join(fixture.home, "worktrees", task)
	retired := filepath.Join(taskPath, ".wb-retired-stage-6b0995eef65f84dace22d24df2644b32")
	if err := os.MkdirAll(retired, 0o700); err != nil {
		t.Fatal(err)
	}

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         task,
		DeleteRemote: true,
		OlderThan:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 0 || len(planned.Purged) != 1 || planned.Purged[0].Path != retired {
		t.Fatalf("artifact-only cleanup plan = %#v", planned)
	}
	if _, statErr := os.Stat(retired); !os.IsNotExist(statErr) {
		t.Fatalf("empty retired stage survived the read path: %v", statErr)
	}
	if len(planned.Artifacts) != 1 || planned.Artifacts[0].Kind != "task_namespace" || !planned.Artifacts[0].Eligible {
		t.Fatalf("artifact-only cleanup plan artifacts = %#v", planned.Artifacts)
	}

	applied, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         task,
		Apply:        true,
		DeleteRemote: true,
		OlderThan:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Results) != 0 || len(applied.Artifacts) != 1 ||
		!applied.Artifacts[0].Applied || applied.Artifacts[0].Kind != "task_namespace" {
		t.Fatalf("artifact-only cleanup apply = %#v", applied)
	}
	if _, statErr := os.Stat(taskPath); !os.IsNotExist(statErr) {
		t.Fatalf("empty task namespace remains: %v", statErr)
	}
}

func TestCleanupKeepsNonEmptyArtifactOnlyTaskAsBlockingBacklog(t *testing.T) {
	fixture := newGitFixture(t)
	const task = "artifact-only-nonempty"
	retired := filepath.Join(fixture.home, "worktrees", task, ".wb-retired-stage-6b0995eef65f84dace22d24df2644b32")
	if err := os.MkdirAll(retired, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retired, "recovery-evidence"), []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         task,
		Apply:        true,
		DeleteRemote: true,
		OlderThan:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 0 || len(outcome.Artifacts) != 1 || outcome.Artifacts[0].Eligible ||
		outcome.Artifacts[0].Applied || outcome.Artifacts[0].Disposition != "cleanup_backlog" {
		t.Fatalf("non-empty artifact-only cleanup = %#v", outcome)
	}
	if _, statErr := os.Stat(filepath.Join(retired, "recovery-evidence")); statErr != nil {
		t.Fatalf("artifact-only backlog evidence was touched: %v", statErr)
	}
}

// TestCleanupWarnsAndSkipsMismatchedCandidateInsideSelectionButCompletesOtherTasks
// covers the two other halves of the same defect: a malformed candidate that
// IS inside the current --filter selection must surface as a warning
// (Diagnostics) rather than aborting the command, and the run must still
// complete cleanup for every other matching, eligible task.
func TestCleanupWarnsAndSkipsMismatchedCandidateInsideSelectionButCompletesOtherTasks(t *testing.T) {
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	result, head, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-filter-warn-elsewhere")
	installMergedPullRequestFixture(t, head, mergedAt)
	stale := createMismatchedWorktree(t, fixture, "cleanup-filter-warn-stale", "acme", "renamed-repo", "old-repo-name")

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		AllMerged:    true,
		Apply:        true,
		Filter:       "acme", // matches both the valid task and the malformed candidate.
		OlderThan:    0,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("cleanup must not abort for an in-scope malformed candidate: %v", err)
	}
	if len(outcome.Diagnostics) != 1 {
		t.Fatalf("in-filter malformed candidate did not surface a warning: %#v", outcome.Diagnostics)
	}
	diagnostic := outcome.Diagnostics[0]
	if diagnostic.Path != stale ||
		!strings.Contains(diagnostic.Message, `"old-repo-name"`) ||
		!strings.Contains(diagnostic.Message, `"renamed-repo"`) ||
		!strings.Contains(diagnostic.Message, "likely cause") {
		t.Fatalf("malformed candidate diagnostic = %#v, want path/expected-repo/actual-repo/cause", diagnostic)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("the run did not complete cleanup of the remaining, unrelated task: %#v", outcome.Results)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("unrelated valid worktree was not cleaned up despite an in-scope warning elsewhere: %v", statErr)
	}
	if _, statErr := os.Stat(stale); statErr != nil {
		t.Fatalf("malformed worktree was touched despite being skipped, not aborted: %v", statErr)
	}
}

// TestCleanupBlocksOnlyCoordinatedTaskOfMismatchedCandidate proves that
// skipping a malformed candidate instead of aborting the whole run does not
// weaken the existing coordinated-task safety gate: a valid, otherwise
// mergeable sibling that shares a task with a malformed candidate must still
// be blocked (not just the malformed entry itself), while a sibling task
// elsewhere is unaffected.
func TestCleanupBlocksOnlyCoordinatedTaskOfMismatchedCandidate(t *testing.T) {
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	result, head, mergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-filter-warn-elsewhere-2")
	sharedTaskResult, sharedHead, sharedMergedAt := prepareMergedTaskInFixture(t, fixture, "cleanup-shared-task")
	installMergedPullRequestFixtures(t, []string{head, sharedHead}, mergedAt)
	stale := createMismatchedWorktree(t, fixture, "cleanup-shared-task", "acme", "renamed-repo", "old-repo-name")

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		AllMerged:    true,
		Apply:        true,
		OlderThan:    0,
		Now:          func() time.Time { return sharedMergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("cleanup must not abort for a malformed candidate: %v", err)
	}
	if len(outcome.Diagnostics) != 1 {
		t.Fatalf("malformed candidate did not surface a warning: %#v", outcome.Diagnostics)
	}
	var sharedTask, otherTask *CleanupResult
	for index := range outcome.Results {
		switch outcome.Results[index].Task {
		case "cleanup-shared-task":
			sharedTask = &outcome.Results[index]
		case "cleanup-filter-warn-elsewhere-2":
			otherTask = &outcome.Results[index]
		}
	}
	if sharedTask == nil || otherTask == nil {
		t.Fatalf("expected both tasks in outcome: %#v", outcome.Results)
	}
	if sharedTask.Eligible || sharedTask.Applied || !strings.Contains(sharedTask.Reason, "malformed candidate") {
		t.Fatalf("valid sibling of a malformed candidate was not blocked: %#v", sharedTask)
	}
	if _, statErr := os.Stat(sharedTaskResult.WorktreeDir); statErr != nil {
		t.Fatalf("blocked sibling worktree was removed: %v", statErr)
	}
	if !otherTask.Applied {
		t.Fatalf("unrelated task was blocked by a malformed candidate in a different task: %#v", otherTask)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("unrelated task's worktree was not cleaned up: %v", statErr)
	}
	if _, statErr := os.Stat(stale); statErr != nil {
		t.Fatalf("malformed worktree was touched: %v", statErr)
	}
}

// TestListDiagnosticForInspectErrorFallsBackToPlainMessageForNonMismatchErrors
// proves the enriched "likely cause" wording is added only for a
// RepositoryRenameMismatchError; every other inspectLifecycleWorktree failure
// (a detached worktree, one still on the base branch, and so on) keeps
// reporting its own plain message unchanged.
func TestListDiagnosticForInspectErrorFallsBackToPlainMessageForNonMismatchErrors(t *testing.T) {
	diagnostic := listDiagnosticForInspectError(
		"/root/worktrees", "task", "/root/worktrees/task/acme/app", "acme",
		fmt.Errorf("some other inspection failure"),
	)
	if diagnostic.Message != "some other inspection failure" {
		t.Fatalf("diagnostic message = %q, want the plain error text unchanged", diagnostic.Message)
	}
	if diagnostic.Task != "task" || diagnostic.WorktreesRoot != "/root/worktrees" || diagnostic.Path != "/root/worktrees/task/acme/app" {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
}

// createMismatchedWorktree registers a linked worktree of a fresh canonical
// repository under a repository-name path segment that does not match that
// canonical repository's real name — precisely what is left behind when a
// canonical repository is renamed after a worktree for it was created (see
// RepositoryRenameMismatchError). No push or remote is needed: the mismatch
// is detected locally, from the worktree's own Git metadata, before any
// network call.
func createMismatchedWorktree(t *testing.T, fixture *gitFixture, task, owner, canonicalRepository, pathRepository string) string {
	t.Helper()
	canonical := filepath.Join(fixture.projectsRoot, owner, canonicalRepository)
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, canonical, "init", "-b", "main")
	configureGitUser(t, canonical)
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# "+canonicalRepository+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, canonical, "add", "README.md")
	gitTest(t, canonical, "commit", "-m", "initial")
	worktree := filepath.Join(fixture.home, "worktrees", task, owner, pathRepository)
	gitTest(t, canonical, "worktree", "add", "-b", "feature/"+task, worktree, "main")
	return worktree
}

func prepareMergedTask(t *testing.T, task string) (*gitFixture, CreateResult, string, time.Time) {
	t.Helper()
	fixture := newGitFixture(t)
	result, head, mergedAt := prepareMergedTaskInFixture(t, fixture, task)
	return fixture, result, head, mergedAt
}

// The public success journey is finalize first, then cleanup. Finalize seals
// the immutable claim as landed; cleanup must accept that exact authority
// without rewriting either the terminal or its outbox receipt.
func TestCleanupAcceptsAlreadyFinalizedLandedClaim(t *testing.T) {
	const task = "cleanup-finalized-landed"
	fixture, result, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)

	finalized, err := LogFinalize(context.Background(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: result.WorktreeDir,
		Result: "success", Message: "landed and ready for cleanup", Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !finalized.Applied {
		t.Fatalf("finalize did not seal the claim: %#v", finalized)
	}
	projection, err := readWorkLogProjection(result.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	terminalPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "terminals", projection.ClaimID+".json")
	outboxPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "outbox", projection.RunID+"-"+projection.ClaimID+"-sealed.json")
	terminalBefore, err := os.ReadFile(terminalPath)
	if err != nil {
		t.Fatal(err)
	}
	outboxBefore, err := os.ReadFile(outboxPath)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Apply: true, DeleteRemote: true,
		OlderThan: 0, Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("finalized cleanup outcome = %#v", outcome.Results)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("finalized worktree remains after cleanup: %v", statErr)
	}
	terminalAfter, err := os.ReadFile(terminalPath)
	if err != nil {
		t.Fatal(err)
	}
	outboxAfter, err := os.ReadFile(outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(terminalBefore, terminalAfter) || !bytes.Equal(outboxBefore, outboxAfter) {
		t.Fatal("cleanup modified immutable finalize authority")
	}
}

func TestCleanupAcceptsAlreadyFinalizedWhenHybridPointerStillActive(t *testing.T) {
	const task = "cleanup-finalized-stale-pointer"
	fixture, result, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	if _, err := LogFinalize(context.Background(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: result.WorktreeDir,
		Result: "success", Message: "landed", Apply: true,
	}); err != nil {
		t.Fatal(err)
	}
	stale, err := readWorkLogProjection(result.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	stale.Lifecycle = "active"
	if err := writeWorkLogProjection(result.WorktreeDir, stale); err != nil {
		t.Fatal(err)
	}
	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Apply: true, DeleteRemote: true,
		OlderThan: 0, Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("stale-pointer cleanup outcome = %#v", outcome.Results)
	}
}

func TestLogArchiveAfterFinalizeApply(t *testing.T) {
	fixture := newGitFixture(t)
	promptPath := writeWorkLogPromptFile(t, "finalize then archive\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "finalize-then-archive",
		WorkLog: WorkLogOptions{
			RunID: "finalize-archive-run", Model: "unknown",
			OriginalPrompt: promptPath, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	finalized, err := LogFinalize(context.Background(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Result: "success", Message: "done", Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Projection == nil || finalized.Projection.Lifecycle != "terminal" {
		t.Fatalf("finalize local projection = %#v, want terminal", finalized.Projection)
	}
	archived, err := LogArchive(context.Background(), LogArchiveOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Apply: true, Force: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !archived.Applied || archived.Event == nil || archived.Event.Type != LocalEventArchive {
		t.Fatalf("archive after finalize = %#v", archived)
	}
}

func TestCleanupRejectsAlreadyFinalizedNotLandedClaim(t *testing.T) {
	const task = "cleanup-finalized-not-landed"
	fixture, result, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	if _, err := LogFinalize(context.Background(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: result.WorktreeDir,
		Result: "failure", Message: "not landed", Apply: true,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Apply: true, DeleteRemote: true,
		OlderThan: 0, Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err == nil || !strings.Contains(err.Error(), "does not authorize cleanup") {
		t.Fatalf("not-landed cleanup error = %v", err)
	}
	if _, statErr := os.Stat(result.WorktreeDir); statErr != nil {
		t.Fatalf("not-landed cleanup touched the worktree: %v", statErr)
	}
}

func prepareMergedTaskInFixture(t *testing.T, fixture *gitFixture, task string) (CreateResult, string, time.Time) {
	t.Helper()
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    task, WorkLog: WorkLogOptions{Model: "unknown"},
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
	installMergedPullRequestFixturesWithMerge(t, []string{head}, nil, mergedAt)
}

func installMergedPullRequestFixtures(t *testing.T, heads []string, mergedAt time.Time) {
	installMergedPullRequestFixturesWithMerge(t, heads, nil, mergedAt)
}

func installMergedPullRequestFixtureWithMerge(t *testing.T, head, mergeSHA string, mergedAt time.Time) {
	installMergedPullRequestFixturesWithMerge(t, []string{head}, []string{mergeSHA}, mergedAt)
}

func installMergedPullRequestFixturesWithMerge(t *testing.T, heads, mergeSHAs []string, mergedAt time.Time) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := `#!/bin/sh
set -eu
if [ "$1 $2" != "api --paginate" ]; then
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
			"number":           index + 17,
			"html_url":         "https://github.com/acme/app/pull/" + strconv.Itoa(index+17),
			"state":            "closed",
			"merged_at":        mergedAt.Format(time.RFC3339),
			"head":             map[string]any{"ref": "feature/test", "sha": head},
			"base":             map[string]any{"ref": "main", "sha": ""},
			"merge_commit_sha": "",
		})
		if index < len(mergeSHAs) {
			pulls[index]["merge_commit_sha"] = mergeSHAs[index]
		}
	}
	payload, err := json.Marshal(pulls)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_MERGED_PULLS", string(payload))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installFailingGitHubFixture(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'gh must not run' >&2\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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

// prepareAbsorbedCandidate models the landing route a repository requiring
// linear history forces on a merger: several completed candidates are merged
// onto one integration branch, that branch is validated once, and it lands as
// a single squash commit. The candidate's own tip is then absent from the
// target by construction, and the landed commit carries more than that
// candidate's tree, so the exact-tree rebase receipt cannot describe it.
func prepareAbsorbedCandidate(t *testing.T, task string) (*gitFixture, CreateResult, string, string, time.Time) {
	t.Helper()
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    task, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "candidate.txt"), []byte(task+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "candidate.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "candidate work")
	head := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)

	// A real integration branch carries the source plus a sibling candidate.
	// Its tip is retained on origin so an explicit --absorbed-by PR receipt can
	// fetch and attest it independently of the source checkout.
	integrationBranch := "integration/" + task
	gitTest(t, fixture.canonical, "checkout", "-b", integrationBranch)
	gitTest(t, fixture.canonical, "merge", "--no-ff", result.Branch, "-m", "merge candidate into integration branch")
	if err := os.WriteFile(filepath.Join(fixture.canonical, "sibling.txt"), []byte("sibling candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "sibling.txt")
	gitTest(t, fixture.canonical, "commit", "-m", "sibling candidate")
	integrationTip := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "push", "-u", "origin", integrationBranch)
	// GitHub exposes a stable numbered pull-head ref independently of any
	// contributor branch name. Mirror that contract in the local bare origin so
	// attested --absorbed-by tests exercise the exact production fetch path.
	gitTest(t, fixture.remote, "update-ref", "refs/pull/77/head", integrationTip)

	// Land the whole batch as one squash commit, exactly as a PR-required,
	// merge-commit-rejecting target branch demands.
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "merge", "--squash", integrationBranch)
	gitTest(t, fixture.canonical, "commit", "-m", "squash integration batch (#77)")
	squashSHA := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	if merged, err := isAncestor(context.Background(), fixture.canonical, head, squashSHA); err != nil || merged {
		t.Fatalf("absorbed candidate must not be an ancestor of the squash: merged=%t err=%v", merged, err)
	}
	if gitTestOutput(t, fixture.canonical, "rev-parse", head+"^{tree}") == gitTestOutput(t, fixture.canonical, "rev-parse", squashSHA+"^{tree}") {
		t.Fatal("fixture must land more than the candidate's own tree, or it would be a plain rebase receipt")
	}
	if merged, err := isAncestor(context.Background(), fixture.canonical, head, integrationTip); err != nil || !merged {
		t.Fatalf("integration head must contain source: merged=%t err=%v", merged, err)
	}
	return fixture, result, head, squashSHA, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
}

// installAbsorbingPullRequestFixture stubs the pull request GitHub associates
// with the immutable source commit. Its head is the integration branch tip,
// never the candidate head — that difference is the whole point.
func installAbsorbingPullRequestFixture(t *testing.T, integrationHead, mergeSHA string, mergedAt time.Time) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := `#!/bin/sh
set -eu
if [ "$1 $2" = "api --paginate" ]; then
    printf '%s\n' "$WB_TEST_MERGED_PULLS"
    exit 0
fi
if [ "$1" = "api" ]; then
    printf '%s\n' "$WB_TEST_SINGLE_PULL"
    exit 0
fi
echo "unexpected gh command: $*" >&2
exit 2
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	pull := map[string]any{
		"number":           77,
		"html_url":         "https://github.com/acme/app/pull/77",
		"state":            "closed",
		"merged_at":        mergedAt.Format(time.RFC3339),
		"head":             map[string]any{"ref": "app-main-merger", "sha": integrationHead},
		"base":             map[string]any{"ref": "main", "sha": ""},
		"merge_commit_sha": mergeSHA,
	}
	list, err := json.Marshal([]map[string]any{pull})
	if err != nil {
		t.Fatal(err)
	}
	single, err := json.Marshal(pull)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_MERGED_PULLS", string(list))
	t.Setenv("WB_TEST_SINGLE_PULL", string(single))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestFetchExactRemotePullRequestHeadUsesStableRefWithoutFetchHead(t *testing.T) {
	expected := strings.Repeat("a", 40)
	var calls [][]string
	run := func(ctx context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "ls-remote":
			return expected + "\trefs/pull/77/head\n", nil
		case "fetch":
			return "", nil
		case "rev-parse":
			return expected, nil
		default:
			t.Fatalf("unexpected Git command: %q", args)
			return "", nil
		}
	}
	fetched, err := fetchExactRemotePullRequestHeadWithRun(context.Background(), "/unused", 77, expected, run)
	if err != nil || fetched != expected {
		t.Fatalf("fetch PR head = %q, %v", fetched, err)
	}
	want := [][]string{
		{"ls-remote", "--exit-code", "origin", "refs/pull/77/head"},
		{"fetch", "--no-tags", "--no-write-fetch-head", "--", "origin", "refs/pull/77/head"},
		{"rev-parse", "--verify", "--end-of-options", expected + "^{commit}"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("Git commands = %#v, want %#v", calls, want)
	}
}

// TestCleanupAcceptsAttestedSquashPullRequestWhenGenericContainmentConflicts
// models a merger that incorporated a source then amended the same file before
// squashing the integration PR. The exact source is in the PR head and the PR
// head equals the landing tree, but replaying the source onto the squash
// landing conflicts. That legacy patch-containment question must not override
// the stronger numbered-PR proof.
func TestCleanupAcceptsAttestedSquashPullRequestWhenGenericContainmentConflicts(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "cleanup-attested-pr-amendment", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "README.md"), []byte("# source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "README.md")
	gitTest(t, result.WorktreeDir, "commit", "-m", "source edits README")
	sourceHead := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)

	integrationBranch := "integration/cleanup-attested-pr-amendment"
	gitTest(t, fixture.canonical, "checkout", "-b", integrationBranch)
	gitTest(t, fixture.canonical, "merge", "--no-ff", result.Branch, "-m", "merge source into integration")
	if err := os.WriteFile(filepath.Join(fixture.canonical, "README.md"), []byte("# amended in integration\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "README.md")
	gitTest(t, fixture.canonical, "commit", "-m", "amend source in integration")
	integrationHead := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "push", "-u", "origin", integrationBranch)
	gitTest(t, fixture.remote, "update-ref", "refs/pull/77/head", integrationHead)

	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "merge", "--squash", integrationBranch)
	gitTest(t, fixture.canonical, "commit", "-m", "squash amended integration")
	squashSHA := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	contained, err := contentContained(context.Background(), fixture.canonical, sourceHead, squashSHA)
	if err != nil {
		t.Fatal(err)
	}
	if contained {
		t.Fatal("fixture must make generic source-to-squash containment fail")
	}

	mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	installAbsorbingPullRequestFixture(t, integrationHead, squashSHA, mergedAt)
	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-attested-pr-amendment", OlderThan: 0,
		AbsorbedBy: "77", Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || !planned.Results[0].Eligible || !planned.Results[0].AbsorbedAtOrigin ||
		planned.Results[0].AbsorbedBySHA != squashSHA || planned.Results[0].MergedPullRequest == nil {
		t.Fatalf("attested amended squash cleanup = %#v", planned)
	}
}

func TestCleanupAcceptsAbsorbedIntegrationBranchSquashReceipt(t *testing.T) {
	fixture, result, head, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "cleanup-absorbed-squash")
	installAbsorbingPullRequestFixture(t, strings.Repeat("a", 40), squashSHA, mergedAt)

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-absorbed-squash", OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || !planned.Results[0].Eligible {
		t.Fatalf("absorbed squash cleanup plan = %#v", planned)
	}
	entry := planned.Results[0].ListResult
	if !entry.IntegratedAtOrigin || !entry.AbsorbedAtOrigin || entry.RebaseMergedAtOrigin {
		t.Fatalf("absorbed squash evidence = %#v", entry)
	}
	if entry.AbsorbedBySHA != squashSHA {
		t.Fatalf("absorbed landing commit = %q, want %q", entry.AbsorbedBySHA, squashSHA)
	}
	if entry.MergedPullRequest == nil || entry.MergedPullRequest.Number != 77 {
		t.Fatalf("absorbed PR receipt = %#v", entry.MergedPullRequest)
	}
	if entry.HeadSHA != head || entry.WorktreeDir != result.WorktreeDir {
		t.Fatalf("absorbed cleanup identity = %#v", entry)
	}
}

func TestCleanupRejectsAbsorbedReceiptWhenTargetLaterRevertedTheWork(t *testing.T) {
	fixture, _, _, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "cleanup-absorbed-reverted")
	if err := os.Remove(filepath.Join(fixture.canonical, "candidate.txt")); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "-A")
	gitTest(t, fixture.canonical, "commit", "-m", "revert the absorbed candidate")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	installAbsorbingPullRequestFixture(t, strings.Repeat("a", 40), squashSHA, mergedAt)

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-absorbed-reverted", OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || planned.Results[0].Eligible ||
		planned.Results[0].IntegratedAtOrigin || planned.Results[0].AbsorbedAtOrigin ||
		!strings.Contains(planned.Results[0].Reason, "awaiting push") {
		t.Fatalf("reverted absorption must be rejected: %#v", planned)
	}
}

func TestCleanupRejectsAbsorbedReceiptWhenOnlyPartOfTheBranchLanded(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "cleanup-absorbed-partial", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "landed.txt"), []byte("landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "landed.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "part that landed")
	landed := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "stranded.txt"), []byte("stranded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "stranded.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "part that did not land")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)

	// The integration branch absorbed only the first commit.
	gitTest(t, fixture.canonical, "cherry-pick", landed)
	squashSHA := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	installAbsorbingPullRequestFixture(t, strings.Repeat("a", 40), squashSHA, mergedAt)

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-absorbed-partial", OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || planned.Results[0].Eligible ||
		planned.Results[0].IntegratedAtOrigin || planned.Results[0].AbsorbedAtOrigin {
		t.Fatalf("partial absorption must be rejected: %#v", planned)
	}
	reason := planned.Results[0].Reason
	if !strings.Contains(reason, "awaiting push") && !strings.Contains(reason, "residual") {
		t.Fatalf("partial absorption must explain the unlanded remainder: %q", reason)
	}
}

func TestCleanupRejectsAbsorbedReceiptWhoseMergeCommitIsNotInTarget(t *testing.T) {
	fixture, _, _, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "cleanup-absorbed-unpushed")
	// Rewind the exact origin target so the receipt's merge commit never
	// reached it: a local-only landing is awaiting_push, not integrated.
	gitTest(t, fixture.canonical, "push", "--force", "origin", squashSHA+"^:refs/heads/main")
	installAbsorbingPullRequestFixture(t, strings.Repeat("a", 40), squashSHA, mergedAt)

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-absorbed-unpushed", OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || planned.Results[0].Eligible ||
		planned.Results[0].IntegratedAtOrigin || planned.Results[0].AbsorbedAtOrigin ||
		!strings.Contains(planned.Results[0].Reason, "awaiting push") {
		t.Fatalf("unpushed absorption must be rejected: %#v", planned)
	}
}

func TestCleanupAcceptsAttestedAbsorbedLandingCommitWithoutPullRequestAssociation(t *testing.T) {
	fixture, _, _, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "cleanup-attested-absorbed")
	// GitHub associates nothing: the merger cherry-picked rather than merged.
	installMergedPullRequestFixtures(t, nil, time.Time{})

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-attested-absorbed", OlderThan: 0,
		AbsorbedBy: squashSHA,
		Now:        func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || !planned.Results[0].Eligible ||
		!planned.Results[0].IntegratedAtOrigin || !planned.Results[0].AbsorbedAtOrigin {
		t.Fatalf("attested absorption plan = %#v", planned)
	}
	if planned.Results[0].AbsorbedBySHA != squashSHA {
		t.Fatalf("attested landing commit = %q, want %q", planned.Results[0].AbsorbedBySHA, squashSHA)
	}
}

func TestCleanupRejectsAttestedLandingCommitThatDidNotIntroduceTheWork(t *testing.T) {
	fixture, _, _, _, mergedAt := prepareAbsorbedCandidate(t, "cleanup-attested-not-entry")
	if err := os.WriteFile(filepath.Join(fixture.canonical, "later.txt"), []byte("later work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "later.txt")
	gitTest(t, fixture.canonical, "commit", "-m", "unrelated later commit")
	later := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	installMergedPullRequestFixtures(t, nil, time.Time{})

	// The work is already contained in this commit's first parent, so naming
	// it cannot serve as a landing receipt. Without this, --absorbed-by main
	// would silently degrade into a bare content assertion.
	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-attested-not-entry", OlderThan: 0,
		AbsorbedBy: later,
		Now:        func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || planned.Results[0].Eligible ||
		planned.Results[0].IntegratedAtOrigin || planned.Results[0].AbsorbedAtOrigin ||
		!strings.Contains(planned.Results[0].Reason, "awaiting push") {
		t.Fatalf("non-introducing attested commit must be rejected: %#v", planned)
	}
}

func TestCleanupRejectsAttestedLandingCommitOutsideTheExactOriginTarget(t *testing.T) {
	fixture, _, _, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "cleanup-attested-outside")
	gitTest(t, fixture.canonical, "push", "--force", "origin", squashSHA+"^:refs/heads/main")
	installMergedPullRequestFixtures(t, nil, time.Time{})

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-attested-outside", OlderThan: 0,
		AbsorbedBy: squashSHA,
		Now:        func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// A refused pointer is a precise, reportable refusal of this candidate,
	// not a malformed worktree and not a fatal end to the whole sweep.
	if len(planned.Results) != 1 || planned.Results[0].Eligible ||
		planned.Results[0].IntegratedAtOrigin || planned.Results[0].AbsorbedAtOrigin ||
		!strings.Contains(planned.Results[0].Reason, "not contained in the exact fetched origin/main target") {
		t.Fatalf("attested commit outside the target must be refused with its reason: %#v", planned)
	}
}

func TestCleanupReportsUnresolvableAbsorbedByPointerAsCandidateRefusal(t *testing.T) {
	fixture, _, _, _, mergedAt := prepareAbsorbedCandidate(t, "cleanup-attested-unresolvable")
	installMergedPullRequestFixtures(t, nil, time.Time{})

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-attested-unresolvable", OlderThan: 0,
		AbsorbedBy: "no-such-landing-ref",
		Now:        func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || planned.Results[0].Eligible ||
		!strings.Contains(planned.Results[0].Reason, "does not resolve to a commit") {
		t.Fatalf("unresolvable pointer must be refused with its reason: %#v", planned)
	}
	if len(planned.Diagnostics) != 0 {
		t.Fatalf("a bad pointer is not a malformed candidate: %#v", planned.Diagnostics)
	}
}

// simulateProcessDeathLeavingLock reproduces what a killed WB leaves on disk.
// A graceful failure still runs its deferred release, which retires the lock;
// SIGKILL does not, so .lock stays and nothing holds it. Renaming the
// retirement back is exactly that on-disk state.
func simulateProcessDeathLeavingLock(t *testing.T, taskDir string) {
	t.Helper()
	entries, err := os.ReadDir(taskDir)
	if errors.Is(err, os.ErrNotExist) {
		// Repository-local cleanup can finish removing its physical checkout
		// before the process is killed, leaving no old shared task namespace.
		// Recreate the logical WB_HOME shell with the exact retired-lock shape
		// a killed lifecycle operation leaves for recovery.
		if err := os.MkdirAll(taskDir, 0o755); err != nil {
			t.Fatal(err)
		}
		retired := filepath.Join(taskDir, ".wb-retired-lock-fixture")
		contents := fmt.Sprintf("operation=%s\npid=%d\n", filepath.Base(taskDir), killedLifecycleProcessPID(t))
		if err := os.WriteFile(retired, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		entries, err = os.ReadDir(taskDir)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".wb-retired-lock-") {
			continue
		}
		if err := os.Rename(filepath.Join(taskDir, entry.Name()), filepath.Join(taskDir, ".lock")); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("no retired lock to revive in %s", taskDir)
}

func TestCleanupResumesAfterProcessDeathLeftItsLockBehind(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "cleanup-resume-after-death")
	installMergedPullRequestFixture(t, head, mergedAt)
	injected := errors.New("injected crash after worktree removal")
	if _, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-resume-after-death",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now:                         func() time.Time { return mergedAt.Add(time.Hour) },
		afterCleanupWorktreeRemoval: func(string) error { return injected },
	}); !errors.Is(err, injected) {
		t.Fatalf("cleanup interruption = %v, want %v", err, injected)
	}
	taskDir := filepath.Join(fixture.home, "worktrees", "cleanup-resume-after-death")
	simulateProcessDeathLeavingLock(t, taskDir)

	resumed, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-resume-after-death",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(2 * time.Hour) },
	})
	if err != nil {
		t.Fatalf("a killed run must not strand the task: %v", err)
	}
	if len(resumed.Results) != 1 || !resumed.Results[0].Applied || !resumed.Results[0].BranchDeleted {
		t.Fatalf("resumed cleanup after process death = %#v", resumed.Results)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created.Branch); branchErr != nil || exists {
		t.Fatalf("resumed cleanup branch exists=%t err=%v", exists, branchErr)
	}
	// The reclaimed lock must be retired again, not left for the next run.
	if _, statErr := os.Stat(filepath.Join(taskDir, ".lock")); !os.IsNotExist(statErr) {
		t.Fatalf("reclaimed lock was not retired: %v", statErr)
	}
}

func TestCleanupRefusesWhileAnotherProcessHoldsTheTaskLock(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "cleanup-live-lock-holder")
	installMergedPullRequestFixture(t, head, mergedAt)
	injected := errors.New("injected crash after worktree removal")
	if _, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-live-lock-holder",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now:                         func() time.Time { return mergedAt.Add(time.Hour) },
		afterCleanupWorktreeRemoval: func(string) error { return injected },
	}); !errors.Is(err, injected) {
		t.Fatalf("cleanup interruption = %v, want %v", err, injected)
	}
	taskDir := filepath.Join(fixture.home, "worktrees", "cleanup-live-lock-holder")
	simulateProcessDeathLeavingLock(t, taskDir)

	// A live holder keeps the kernel lock. Liveness, not mere existence, is
	// what must refuse the resume.
	holder, err := os.Open(filepath.Join(taskDir, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	_, err = Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-live-lock-holder",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(2 * time.Hour) },
	})
	if err == nil || !strings.Contains(err.Error(), "already active in another process") {
		t.Fatalf("a live lock holder must refuse the resume: %v", err)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created.Branch); branchErr != nil || !exists {
		t.Fatalf("refused resume must not delete the branch: exists=%t err=%v", exists, branchErr)
	}
}

func TestCleanupRefusesInterruptedLockWithoutBacklogRecord(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "cleanup-interrupted-no-backlog")
	installMergedPullRequestFixture(t, head, mergedAt)
	// No interruption happened, so there is no durable record of what remains.
	// A stray .lock must still block: only a describable remnant is resumable.
	taskDir := filepath.Join(fixture.home, "worktrees", "cleanup-interrupted-no-backlog")
	if err := os.WriteFile(filepath.Join(taskDir, ".lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-interrupted-no-backlog",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// A live worktree carrying a stray lock is reported locked and left alone;
	// reclaiming is reserved for a remnant a backlog record can describe, and
	// this one still has its whole checkout.
	// The lock is empty, so it carries no operation/PID metadata: WB cannot
	// establish an owner, and must say so rather than offer a recovery that
	// would itself refuse this lock.
	if len(planned.Results) != 1 || planned.Results[0].Eligible || planned.Results[0].Applied ||
		planned.Results[0].LockOwner != LockOwnerUnreadable ||
		!strings.Contains(planned.Results[0].Reason, "does not carry WB's operation/PID metadata") {
		t.Fatalf("undescribable interruption must keep demanding attention: %#v", planned.Results)
	}
	if strings.Contains(planned.Results[0].Reason, "--resume-interrupted") {
		t.Fatalf("an unestablishable owner must not advertise recovery: %q", planned.Results[0].Reason)
	}
	if _, statErr := os.Stat(created.WorktreeDir); statErr != nil {
		t.Fatalf("refused cleanup must preserve the worktree: %v", statErr)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created.Branch); branchErr != nil || !exists {
		t.Fatalf("refused cleanup must preserve the branch: exists=%t err=%v", exists, branchErr)
	}
}

func TestCleanupResumeInterruptedNamedTaskPlansThenAppliesExactDeadLock(t *testing.T) {
	const task = "cleanup-named-interrupted-recovery"
	fixture, _, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	taskDir := filepath.Join(fixture.home, "worktrees", task)
	contents := fmt.Sprintf("operation=%s\npid=%d\n", task, killedLifecycleProcessPID(t))
	lockPath := filepath.Join(taskDir, ".lock")
	if err := os.WriteFile(lockPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, ResumeInterrupted: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if planned.Recovery == nil || planned.Recovery.Disposition != "validated" || planned.Recovery.Applied || planned.Recovery.PID <= 0 ||
		len(planned.Results) != 1 || !planned.Results[0].Eligible {
		t.Fatalf("recovery plan = %#v", planned)
	}
	if got, err := os.ReadFile(lockPath); err != nil || string(got) != contents {
		t.Fatalf("dry-run changed exact lock: contents=%q err=%v", got, err)
	}

	applied, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, ResumeInterrupted: true,
		Apply: true, DeleteRemote: true, OlderThan: 0,
		ReportDir: filepath.Join(t.TempDir(), "recovery-audit"),
		Now:       func() time.Time { return mergedAt.Add(2 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Recovery == nil || applied.Recovery.Disposition != "quarantined" || !applied.Recovery.Applied ||
		len(applied.Results) != 1 || !applied.Results[0].Applied {
		t.Fatalf("recovery apply = %#v", applied)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("recovered lock remains active: %v", err)
	}
	report, err := os.ReadFile(applied.ReportPath)
	if err != nil || !strings.Contains(string(report), `"disposition": "quarantined"`) || !strings.Contains(string(report), `"pid":`) {
		t.Fatalf("recovery audit = %q err=%v", report, err)
	}
}

func TestCleanupResumeInterruptedNamedTaskPreservesAmbiguousLock(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, lockPath, task string) string
	}{
		{name: "invalid metadata", setup: func(t *testing.T, lockPath, _ string) string {
			contents := "operation=other\npid=6954\n"
			if err := os.WriteFile(lockPath, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			return contents
		}},
		{name: "live legacy owner", setup: func(t *testing.T, lockPath, task string) string {
			contents := fmt.Sprintf("operation=%s\npid=%d\n", task, os.Getpid())
			if err := os.WriteFile(lockPath, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			return contents
		}},
		{name: "symlink", setup: func(t *testing.T, lockPath, task string) string {
			contents := fmt.Sprintf("operation=%s\npid=6954\n", task)
			target := filepath.Join(t.TempDir(), "foreign-lock")
			if err := os.WriteFile(target, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, lockPath); err != nil {
				t.Fatal(err)
			}
			return contents
		}},
		{name: "hardlink", setup: func(t *testing.T, lockPath, task string) string {
			contents := fmt.Sprintf("operation=%s\npid=6954\n", task)
			target := filepath.Join(t.TempDir(), "foreign-lock")
			if err := os.WriteFile(target, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, lockPath); err != nil {
				t.Fatal(err)
			}
			return contents
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			task := "cleanup-ambiguous-" + strings.ReplaceAll(test.name, " ", "-")
			fixture, created, head, mergedAt := prepareMergedTask(t, task)
			installMergedPullRequestFixture(t, head, mergedAt)
			lockPath := logicalTaskLockPathForTest(t, fixture, created, task)
			contents := test.setup(t, lockPath, task)
			if _, err := Cleanup(context.Background(), CleanupOptions{
				ProjectsRoot: fixture.projectsRoot, Task: task, ResumeInterrupted: true, OlderThan: 0,
				Now: func() time.Time { return mergedAt.Add(time.Hour) },
			}); err == nil {
				t.Fatal("ambiguous interrupted lock was accepted")
			}
			if got, err := os.ReadFile(lockPath); err != nil || string(got) != contents {
				t.Fatalf("ambiguous lock changed: contents=%q err=%v", got, err)
			}
			if info, err := os.Lstat(lockPath); err != nil || (test.name == "symlink" && info.Mode()&os.ModeSymlink == 0) {
				t.Fatalf("ambiguous lock entry was replaced: info=%v err=%v", info, err)
			}
		})
	}
}

func TestCleanupResumeInterruptedNamedTaskRejectsLateSuccessor(t *testing.T) {
	const task = "cleanup-named-late-successor"
	fixture, created, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	taskDir := filepath.Dir(logicalTaskLockPathForTest(t, fixture, created, task))
	lockPath := filepath.Join(taskDir, ".lock")
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("operation=%s\npid=%d\n", task, killedLifecycleProcessPID(t))), 0o600); err != nil {
		t.Fatal(err)
	}
	successor := "operation=successor\npid=1\n"
	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, ResumeInterrupted: true,
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
		afterResumeInterruptedLock: func(path string) error {
			if err := os.Rename(path, filepath.Join(taskDir, ".held-before-successor")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(successor), 0o600); err != nil {
				t.Fatal(err)
			}
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "lock changed after recovery") {
		t.Fatalf("late successor recovery error = %v", err)
	}
	if got, err := os.ReadFile(lockPath); err != nil || string(got) != successor {
		t.Fatalf("late successor was changed: contents=%q err=%v", got, err)
	}
	if _, err := os.Stat(created.WorktreeDir); err != nil {
		t.Fatalf("late successor must block cleanup: %v", err)
	}
}

func TestCleanupResumeInterruptedNamedTaskRejectsSuccessorBeforeRemoteDeletion(t *testing.T) {
	const task = "cleanup-named-successor-before-remote"
	fixture, created, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	contents, lockPath := writeDeadInterruptedTaskLock(t, fixture, created, task)
	heldPath := lockPath + ".held-before-successor"
	successor := "operation=successor\npid=1\n"
	reportDir := filepath.Join(t.TempDir(), "audit")

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, ResumeInterrupted: true,
		Apply: true, DeleteRemote: true, OlderThan: 0, ReportDir: reportDir,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
		beforeCleanupNetworkBranchOperation: func(worktree string) {
			if worktree != created.WorktreeDir {
				return
			}
			installRecoveredLockSuccessor(t, lockPath, heldPath, successor)
		},
	})
	assertRecoveredLockBoundaryRefusal(t, err, outcome, lockPath, heldPath, contents, successor)
	if got := remoteBranchForTest(t, fixture.canonical, created.Branch); got != head {
		t.Fatalf("successor before remote deletion changed remote branch: got %q want %q", got, head)
	}
	if _, statErr := os.Stat(created.WorktreeDir); statErr != nil {
		t.Fatalf("successor before remote deletion removed worktree: %v", statErr)
	}
	if !gitRefExists(fixture.canonical, "refs/heads/"+created.Branch) {
		t.Fatal("successor before remote deletion removed local branch")
	}
	assertRecoveryFailureReport(t, outcome.ReportPath)
}

func TestCleanupResumeInterruptedNamedTaskRejectsSuccessorBeforeWorktreeRemoval(t *testing.T) {
	const task = "cleanup-named-successor-before-worktree-removal"
	fixture, created, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	contents, lockPath := writeDeadInterruptedTaskLock(t, fixture, created, task)
	heldPath := lockPath + ".held-before-successor"
	successor := "operation=successor\npid=1\n"
	reportDir := filepath.Join(t.TempDir(), "audit")

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, ResumeInterrupted: true,
		Apply: true, DeleteRemote: false, OlderThan: 0, ReportDir: reportDir,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
		afterCleanupGitAuthorization: func(operation string) {
			if operation == "remove worktree" {
				installRecoveredLockSuccessor(t, lockPath, heldPath, successor)
			}
		},
	})
	assertRecoveredLockBoundaryRefusal(t, err, outcome, lockPath, heldPath, contents, successor)
	if got := remoteBranchForTest(t, fixture.canonical, created.Branch); got != head {
		t.Fatalf("successor before worktree removal changed remote branch: got %q want %q", got, head)
	}
	if _, statErr := os.Stat(created.WorktreeDir); statErr != nil {
		t.Fatalf("successor before worktree removal removed worktree: %v", statErr)
	}
	if !gitRefExists(fixture.canonical, "refs/heads/"+created.Branch) {
		t.Fatal("successor before worktree removal removed local branch")
	}
	assertRecoveryFailureReport(t, outcome.ReportPath)
}

func TestCleanupResumeInterruptedNamedTaskReportsFailedQuarantineTruthfully(t *testing.T) {
	const task = "cleanup-named-quarantine-failure"
	fixture, created, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	contents, lockPath := writeDeadInterruptedTaskLock(t, fixture, created, task)
	heldPath := lockPath + ".held-before-successor"
	successor := "operation=successor\npid=1\n"
	reportDir := filepath.Join(t.TempDir(), "audit")

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, ResumeInterrupted: true,
		Apply: true, DeleteRemote: true, OlderThan: 0, ReportDir: reportDir,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
		beforeRecoveredLockQuarantine: func(path string) {
			if path != lockPath {
				t.Fatalf("quarantine path = %q, want %q", path, lockPath)
			}
			installRecoveredLockSuccessor(t, lockPath, heldPath, successor)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "quarantine recovered cleanup lock") {
		t.Fatalf("quarantine failure error = %v", err)
	}
	if outcome.Recovery == nil || outcome.Recovery.Disposition != "validated" || outcome.Recovery.Applied {
		t.Fatalf("quarantine failure recovery = %#v", outcome.Recovery)
	}
	assertRecoveredLockEvidence(t, lockPath, heldPath, contents, successor)
	assertRecoveryFailureReport(t, outcome.ReportPath)
}

func installRecoveredLockSuccessor(t *testing.T, lockPath, heldPath, successor string) {
	t.Helper()
	if err := os.Rename(lockPath, heldPath); err != nil {
		t.Fatalf("move recovered lock before successor: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte(successor), 0o600); err != nil {
		t.Fatalf("install successor lock: %v", err)
	}
}

func assertRecoveredLockBoundaryRefusal(t *testing.T, err error, outcome CleanupOutcome, lockPath, heldPath, contents, successor string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "lock changed after recovery") {
		t.Fatalf("successor boundary error = %v", err)
	}
	if outcome.Recovery == nil || outcome.Recovery.Disposition != "validated" || outcome.Recovery.Applied {
		t.Fatalf("successor boundary recovery = %#v", outcome.Recovery)
	}
	assertRecoveredLockEvidence(t, lockPath, heldPath, contents, successor)
}

func assertRecoveredLockEvidence(t *testing.T, lockPath, heldPath, wantHeld, successor string) {
	t.Helper()
	if got, err := os.ReadFile(lockPath); err != nil || string(got) != successor {
		t.Fatalf("successor lock changed: contents=%q err=%v", got, err)
	}
	if contents, err := os.ReadFile(heldPath); err != nil || string(contents) != wantHeld {
		t.Fatalf("held recovered lock evidence changed: contents=%q err=%v", contents, err)
	}
}

func assertRecoveryFailureReport(t *testing.T, path string) {
	t.Helper()
	var report cleanupReport
	contents, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(contents, &report) != nil || report.Phase != "failed" || report.Recovery == nil || report.Recovery.Disposition != "validated" || report.Recovery.Applied {
		t.Fatalf("recovery failure audit=%#v contents=%q err=%v", report, contents, err)
	}
}

func TestCleanupResumeInterruptedNamedTaskPreservesLockUntilEligibleTransaction(t *testing.T) {
	t.Run("dirty merged task", func(t *testing.T) {
		const task = "cleanup-recovery-dirty-merged"
		fixture, created, head, mergedAt := prepareMergedTask(t, task)
		installMergedPullRequestFixture(t, head, mergedAt)
		contents, lockPath := writeDeadInterruptedTaskLock(t, fixture, created, task)
		if err := os.WriteFile(filepath.Join(created.WorktreeDir, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		outcome, err := Cleanup(context.Background(), CleanupOptions{
			ProjectsRoot: fixture.projectsRoot, Task: task, ResumeInterrupted: true,
			Apply: true, DeleteRemote: true, OlderThan: 0, ReportDir: filepath.Join(t.TempDir(), "audit"),
			Now: func() time.Time { return mergedAt.Add(time.Hour) },
		})
		if err != nil || outcome.Recovery == nil || outcome.Recovery.Disposition != "validated" || outcome.Recovery.Applied || len(outcome.Results) != 1 || outcome.Results[0].Eligible {
			t.Fatalf("dirty recovery outcome=%#v err=%v", outcome, err)
		}
		assertInterruptedLockPreserved(t, lockPath, contents)
		var report cleanupReport
		content, readErr := os.ReadFile(outcome.ReportPath)
		if readErr != nil || json.Unmarshal(content, &report) != nil || report.Phase != "validated" || report.Recovery == nil || report.Recovery.Applied {
			t.Fatalf("dirty recovery audit=%#v read=%v", report, readErr)
		}
	})

	t.Run("unmerged task", func(t *testing.T) {
		const task = "cleanup-recovery-unmerged"
		fixture := newGitFixture(t)
		created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: task, WorkLog: WorkLogOptions{Model: "unknown"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "unmerged.txt"), []byte("unmerged\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitTest(t, created[0].WorktreeDir, "add", "unmerged.txt")
		gitTest(t, created[0].WorktreeDir, "commit", "-m", "unmerged")
		gitTest(t, created[0].WorktreeDir, "push", "-u", "origin", created[0].Branch)
		installMergedPullRequestFixtures(t, nil, time.Time{})
		contents, lockPath := writeDeadInterruptedTaskLock(t, fixture, created[0], task)
		outcome, cleanupErr := Cleanup(context.Background(), CleanupOptions{
			ProjectsRoot: fixture.projectsRoot, Task: task, ResumeInterrupted: true,
			Apply: true, DeleteRemote: true, OlderThan: 0,
			Now: func() time.Time { return time.Now() },
		})
		if cleanupErr != nil || outcome.Recovery == nil || outcome.Recovery.Applied || len(outcome.Results) != 1 || outcome.Results[0].Eligible {
			t.Fatalf("unmerged recovery outcome=%#v err=%v", outcome, cleanupErr)
		}
		assertInterruptedLockPreserved(t, lockPath, contents)
	})

	t.Run("filtered task", func(t *testing.T) {
		const task = "cleanup-recovery-filtered"
		fixture, created, head, mergedAt := prepareMergedTask(t, task)
		installMergedPullRequestFixture(t, head, mergedAt)
		contents, lockPath := writeDeadInterruptedTaskLock(t, fixture, created, task)
		outcome, err := Cleanup(context.Background(), CleanupOptions{
			ProjectsRoot: fixture.projectsRoot, Task: task, Filter: "does-not-match", ResumeInterrupted: true,
			Apply: true, DeleteRemote: true, OlderThan: 0,
			Now: func() time.Time { return mergedAt.Add(time.Hour) },
		})
		if err != nil || outcome.Recovery == nil || outcome.Recovery.Disposition != "validated" || outcome.Recovery.Applied {
			t.Fatalf("filtered recovery outcome=%#v err=%v", outcome, err)
		}
		assertInterruptedLockPreserved(t, lockPath, contents)
	})

	t.Run("no worktree candidate", func(t *testing.T) {
		const task = "cleanup-recovery-no-candidate"
		fixture := newGitFixture(t)
		taskDir := filepath.Join(fixture.home, "worktrees", task)
		if err := os.MkdirAll(taskDir, 0o755); err != nil {
			t.Fatal(err)
		}
		contents := fmt.Sprintf("operation=%s\npid=%d\n", task, killedLifecycleProcessPID(t))
		lockPath := filepath.Join(taskDir, ".lock")
		if err := os.WriteFile(lockPath, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: task, ResumeInterrupted: true, Apply: true, DeleteRemote: true})
		if err == nil || !strings.Contains(err.Error(), "was not found") {
			t.Fatalf("no-candidate recovery error=%v", err)
		}
		assertInterruptedLockPreserved(t, lockPath, contents)
	})

	t.Run("report directory early error", func(t *testing.T) {
		const task = "cleanup-recovery-report-directory-error"
		fixture, created, head, mergedAt := prepareMergedTask(t, task)
		installMergedPullRequestFixture(t, head, mergedAt)
		contents, lockPath := writeDeadInterruptedTaskLock(t, fixture, created, task)
		reportFile := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(reportFile, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Cleanup(context.Background(), CleanupOptions{
			ProjectsRoot: fixture.projectsRoot, Task: task, ResumeInterrupted: true, Apply: true, DeleteRemote: true,
			ReportDir: reportFile,
			Now:       func() time.Time { return mergedAt.Add(time.Hour) },
		})
		if err == nil || !strings.Contains(err.Error(), "create cleanup report directory") {
			t.Fatalf("report-directory recovery error=%v", err)
		}
		assertInterruptedLockPreserved(t, lockPath, contents)
	})
}

func writeDeadInterruptedTaskLock(t *testing.T, fixture *gitFixture, created CreateResult, task string) (string, string) {
	t.Helper()
	contents := fmt.Sprintf("operation=%s\npid=%d\n", task, killedLifecycleProcessPID(t))
	lockPath := logicalTaskLockPathForTest(t, fixture, created, task)
	if err := os.WriteFile(lockPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return contents, lockPath
}

// logicalTaskLockPathForTest follows the same placement split as Cleanup:
// repo-local and configured-shared checkouts coordinate through WB_HOME, while
// historic roots retain their physical task lock. Resolving List's observed
// layout keeps these recovery tests valid for both placement modes.
func logicalTaskLockPathForTest(t *testing.T, fixture *gitFixture, created CreateResult, task string) string {
	t.Helper()
	listed, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range listed.Results {
		if filepath.Clean(result.WorktreeDir) != filepath.Clean(created.WorktreeDir) {
			continue
		}
		root := lifecycleTaskLockRoot(fixture.home, wbhome.Layout{
			WorktreesRoot: result.WorktreesRoot,
			Local:         result.Local,
		})
		return filepath.Join(root, task, ".lock")
	}
	t.Fatalf("created worktree %s was not listed for task %s: %#v", created.WorktreeDir, task, listed.Results)
	return ""
}

func assertInterruptedLockPreserved(t *testing.T, path, want string) {
	t.Helper()
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Fatalf("interrupted lock changed: contents=%q err=%v", got, err)
	}
}

func TestCleanupResumeInterruptedRequiresOneNamedTask(t *testing.T) {
	if _, err := normalizeCleanupOptions(CleanupOptions{
		ProjectsRoot: t.TempDir(), AllMerged: true, ResumeInterrupted: true,
	}); err == nil || !strings.Contains(err.Error(), "requires one explicit task") {
		t.Fatalf("fleet recovery normalization error = %v", err)
	}
}

func killedLifecycleProcessPID(t *testing.T) int {
	t.Helper()
	process := exec.Command("sh", "-c", "sleep 60")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	pid := process.Process.Pid
	if err := process.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err == nil {
		t.Fatal("killed child unexpectedly exited without a signal")
	}
	return pid
}

// installUnknownCommitPullRequestFixture reproduces GitHub's answer for a
// commit it has never seen, which is what an unpushed local head gets.
func installUnknownCommitPullRequestFixture(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := `#!/bin/sh
set -eu
cat <<'JSON'
{
  "message": "No commit found for SHA: deadbeef",
  "documentation_url": "https://docs.github.com/rest/commits/commits",
  "status": "422"
}
JSON
echo "gh: No commit found for SHA (HTTP 422)" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCleanupTreatsUnpushedHeadAsHavingNoAssociatedPullRequest(t *testing.T) {
	fixture, _, _, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "cleanup-unpushed-head")
	installUnknownCommitPullRequestFixture(t)

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-unpushed-head", OlderThan: 0,
		AbsorbedBy: squashSHA,
		Now:        func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// A commit GitHub never saw must not hide the worktree behind a malformed
	// diagnostic; --absorbed-by exists for exactly this landing.
	if len(planned.Diagnostics) != 0 {
		t.Fatalf("an unpushed head is not a malformed candidate: %#v", planned.Diagnostics)
	}
	if len(planned.Results) != 1 || !planned.Results[0].Eligible ||
		!planned.Results[0].AbsorbedAtOrigin || planned.Results[0].AbsorbedBySHA != squashSHA {
		t.Fatalf("attested cleanup of an unpushed head = %#v", planned.Results)
	}
}

func TestCleanupStillReportsAnUnrelatedPullRequestQueryFailure(t *testing.T) {
	fixture, _, _, _, mergedAt := prepareAbsorbedCandidate(t, "cleanup-gh-broken")
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "gh"),
		[]byte("#!/bin/sh\necho 'gh: server error' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-gh-broken", OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Diagnostics) != 1 || !strings.Contains(planned.Diagnostics[0].Message, "query pull requests") {
		t.Fatalf("a genuine GitHub failure must still be reported: %#v", planned.Diagnostics)
	}
	for _, result := range planned.Results {
		if result.Eligible {
			t.Fatalf("a genuine GitHub failure must not leave a candidate eligible: %#v", result)
		}
	}
}
