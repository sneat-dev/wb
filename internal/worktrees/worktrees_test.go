package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestCreateSynchronizesCanonicalAndCreatesCentralWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	fixture.pushRemoteCommit(t, "remote change")

	results, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "issue-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v", results)
	}
	result := results[0]
	wantWorktree := filepath.Join(fixture.home, "worktrees", "issue-123", "acme", "app")
	if result.WorktreeDir != wantWorktree || result.Branch != "codex/issue-123" || result.Action != "created" {
		t.Fatalf("result = %#v", result)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main") {
		t.Fatalf("canonical HEAD %s did not synchronize with origin/main", got)
	}
	if got := gitTestOutput(t, result.WorktreeDir, "branch", "--show-current"); got != "codex/issue-123" {
		t.Fatalf("worktree branch = %q", got)
	}
	if got := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD"); got != gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD") {
		t.Fatal("new worktree was not based on synchronized main")
	}

	guarded, err := Guard(context.Background(), result.WorktreeDir, GuardOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatal(err)
	}
	if guarded.Kind != "linked" || guarded.CanonicalDir != fixture.canonical {
		t.Fatalf("guard result = %#v", guarded)
	}
}

func TestCreateRejectsSymlinkedTaskAndOwnerDirectories(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, *gitFixture, string)
	}{
		{
			name: "task",
			setup: func(t *testing.T, fixture *gitFixture, outside string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(fixture.home, "worktrees"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(fixture.home, "worktrees", "escape")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "owner",
			setup: func(t *testing.T, fixture *gitFixture, outside string) {
				t.Helper()
				ownerParent := filepath.Join(fixture.home, "worktrees", "escape")
				if err := os.MkdirAll(ownerParent, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(ownerParent, "acme")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			outside := t.TempDir()
			test.setup(t, fixture, outside)
			_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot,
				Operation:    "escape",
			})
			if err == nil || !strings.Contains(err.Error(), "symlinked") {
				t.Fatalf("Create symlink guard error = %v", err)
			}
			if entries, readErr := os.ReadDir(outside); readErr != nil || len(entries) != 0 {
				t.Fatalf("outside target was mutated: entries=%v, error=%v", entries, readErr)
			}
		})
	}
}

func TestCreateDoesNotFollowOwnerSwapDuringSecureAdd(t *testing.T) {
	fixture := newGitFixture(t)
	outside := t.TempDir()
	ownerPath := filepath.Join(fixture.home, "worktrees", "owner-swap", "acme")
	movedOwner := ownerPath + "-moved"
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "owner-swap",
		beforeSecureWorktreeAdd: func() {
			if err := os.Rename(ownerPath, movedOwner); err != nil {
				t.Fatalf("move owner during secure-add regression: %v", err)
			}
			if err := os.Symlink(outside, ownerPath); err != nil {
				t.Fatalf("swap owner for external symlink during secure-add regression: %v", err)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "owner path changed") {
		t.Fatalf("owner-swap Create error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "app")); !os.IsNotExist(statErr) {
		t.Fatalf("external target received a worktree: %v", statErr)
	}
	registered := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if strings.Contains(registered, outside) || strings.Contains(registered, movedOwner) {
		t.Fatalf("owner-swap left an external or moved worktree registration:\n%s", registered)
	}
}

func TestCreateRollsBackPartialStagedWorktreeFailure(t *testing.T) {
	fixture := newGitFixture(t)
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "partial-add",
		afterStagedWorktreeAdd: func() error {
			// Model a post-checkout hook or other Git-side failure reported after
			// Git has created the linked checkout and branch.
			return errors.New("simulated post-checkout failure")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "simulated post-checkout failure") {
		t.Fatalf("partial staged Create error = %v", err)
	}
	assertFailedCreateRolledBack(t, fixture, "partial-add")
}

func TestCreateRollsBackWhenPublishedWorktreeRepairFails(t *testing.T) {
	fixture := newGitFixture(t)
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "repair-failure",
		beforeWorktreeRepair: func() error {
			return errors.New("simulated worktree repair failure")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "simulated worktree repair failure") {
		t.Fatalf("repair-failure Create error = %v", err)
	}
	assertFailedCreateRolledBack(t, fixture, "repair-failure")
}

func assertFailedCreateRolledBack(t *testing.T, fixture *gitFixture, operation string) {
	t.Helper()
	worktree := filepath.Join(fixture.home, "worktrees", operation, "acme", "app")
	if _, err := os.Lstat(worktree); !os.IsNotExist(err) {
		t.Fatalf("failed creation left worktree %s: %v", worktree, err)
	}
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	entries, err := os.ReadDir(operationRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".wb-stage-") {
			t.Fatalf("failed creation leaked staging directory %s", entry.Name())
		}
	}
	registered := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if strings.Contains(registered, operationRoot) {
		t.Fatalf("failed creation left worktree registration:\n%s", registered)
	}
	if exists, err := localBranchExists(context.Background(), fixture.canonical, "codex/"+operation); err != nil || exists {
		t.Fatalf("failed creation left feature branch: exists=%t err=%v", exists, err)
	}
	listed, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: operation})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Results) != 0 || len(listed.Diagnostics) != 0 {
		t.Fatalf("failed creation remained inventory-visible: %#v", listed)
	}
}

func TestDefaultHomeCreatesNewWorktreeWhileLegacyWorktreeRemainsGuardable(t *testing.T) {
	fixture := newDefaultHomeGitFixture(t)
	legacy := filepath.Join(fixture.projectsRoot, ".wb", "worktrees", "legacy", "acme", "app")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "codex/legacy", legacy, "main")

	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "new-home",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := created[0].WorktreeDir, filepath.Join(fixture.home, "worktrees", "new-home", "acme", "app"); got != want {
		t.Fatalf("new worktree = %q, want authoritative default-home path %q", got, want)
	}
	if strings.HasPrefix(created[0].WorktreeDir, filepath.Join(fixture.projectsRoot, ".wb")) {
		t.Fatalf("new worktree silently reused legacy home: %s", created[0].WorktreeDir)
	}
	guarded, err := Guard(context.Background(), legacy, GuardOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatalf("legacy linked worktree was stranded: %v", err)
	}
	if got, want := guarded.WorktreesRoot, filepath.Join(fixture.projectsRoot, ".wb", "worktrees"); got != want {
		t.Fatalf("legacy guard root = %q, want %q", got, want)
	}
	listed, err := List(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed worktrees = %#v, want new + legacy", listed)
	}
}

func TestCreateRefusesUnsafeCanonicalClone(t *testing.T) {
	tests := []struct {
		name string
		trip func(*testing.T, *gitFixture)
		want string
	}{
		{
			name: "dirty",
			trip: func(t *testing.T, fixture *gitFixture) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(fixture.canonical, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "is dirty",
		},
		{
			name: "feature branch",
			trip: func(t *testing.T, fixture *gitFixture) {
				t.Helper()
				gitTest(t, fixture.canonical, "switch", "-c", "feature")
			},
			want: `is on "feature"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			test.trip(t, fixture)
			_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot,
				Operation:    "unsafe",
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCreateResumeIsExplicitAndPreservesChanges(t *testing.T) {
	fixture := newGitFixture(t)
	options := CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "resume-me"}
	first, err := Create(context.Background(), []string{"acme/app"}, options)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(first[0].WorktreeDir, "in-progress.txt")
	if err := os.WriteFile(marker, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), []string{"acme/app"}, options); err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("non-resume error = %v", err)
	}
	options.Resume = true
	resumed, err := Create(context.Background(), []string{"acme/app"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if resumed[0].Action != "resumed" {
		t.Fatalf("resume result = %#v", resumed[0])
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep\n" {
		t.Fatalf("in-progress work was not preserved: %q, %v", content, err)
	}
}

func TestGuardRejectsFeatureBranchesAndChangesInCanonicalClone(t *testing.T) {
	fixture := newGitFixture(t)
	if result, err := Guard(context.Background(), fixture.canonical, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil || result.Kind != "canonical" {
		t.Fatalf("clean main guard = %#v, %v", result, err)
	}

	gitTest(t, fixture.canonical, "switch", "-c", "feature")
	if _, err := Guard(context.Background(), fixture.canonical, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), "wb worktree create") {
		t.Fatalf("feature guard error = %v", err)
	}
	gitTest(t, fixture.canonical, "switch", "main")
	if err := os.WriteFile(filepath.Join(fixture.canonical, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Guard(context.Background(), fixture.canonical, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), "must remain clean") {
		t.Fatalf("dirty guard error = %v", err)
	}
}

func TestGuardRejectsLinkedWorktreeOutsideCentralHierarchy(t *testing.T) {
	fixture := newGitFixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature", outside, "main")
	if _, err := Guard(context.Background(), outside, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), ".wb/worktrees") {
		t.Fatalf("outside guard error = %v", err)
	}
}

func TestGuardAllowsOnlyRealTransientRebases(t *testing.T) {
	for _, mode := range []struct {
		name  string
		args  []string
		state string
	}{
		{name: "merge backend", args: []string{"rebase", "origin/main"}, state: "rebase-merge"},
		{name: "apply backend", args: []string{"rebase", "--apply", "origin/main"}, state: "rebase-apply"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "rebase"})
			if err != nil {
				t.Fatal(err)
			}
			worktree := created[0].WorktreeDir
			if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("feature\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitTest(t, worktree, "add", "README.md")
			gitTest(t, worktree, "commit", "-m", "feature change")
			if err := os.WriteFile(filepath.Join(fixture.canonical, "README.md"), []byte("main\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitTest(t, fixture.canonical, "add", "README.md")
			gitTest(t, fixture.canonical, "commit", "-m", "main change")
			gitTest(t, fixture.canonical, "push", "origin", "main")
			gitTest(t, worktree, "fetch", "origin", "main")
			if output, err := gitTestRun(worktree, mode.args...); err == nil {
				t.Fatalf("git %s unexpectedly succeeded: %s", strings.Join(mode.args, " "), output)
			}
			gitDir := gitTestOutput(t, worktree, "rev-parse", "--absolute-git-dir")
			if info, err := os.Stat(filepath.Join(gitDir, mode.state)); err != nil || !info.IsDir() {
				t.Fatalf("expected active %s state: %v", mode.state, err)
			}
			guarded, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot})
			if err != nil || !guarded.Transient || guarded.Kind != "linked" {
				t.Fatalf("guard during %s = %#v, %v", mode.state, guarded, err)
			}
			if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("resolved\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitTest(t, worktree, "add", "README.md")
			if output, err := gitTestRunEnv(worktree, []string{"GIT_EDITOR=true"}, "rebase", "--continue"); err != nil {
				t.Fatalf("finish rebase: %v\n%s", err, output)
			}
			guarded, err = Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot})
			if err != nil || guarded.Transient || guarded.Branch != "codex/rebase" {
				t.Fatalf("guard after rebase = %#v, %v", guarded, err)
			}
			gitTest(t, worktree, "checkout", "--detach", "HEAD")
			if _, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
				t.Fatalf("arbitrary detached guard error = %v", err)
			}
		})
	}
}

func TestGuardRejectsFabricatedOrSymlinkedRebaseState(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "fabricated-rebase"})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "checkout", "--detach", "HEAD")
	gitDir := gitTestOutput(t, worktree, "rev-parse", "--absolute-git-dir")
	state := filepath.Join(gitDir, "rebase-merge")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("empty rebase state guard error = %v", err)
	}
	for name, content := range map[string]string{
		"head-name":              "refs/heads/codex/fabricated-rebase\n",
		"orig-head":              strings.Repeat("a", 40) + "\n",
		"onto":                   strings.Repeat("b", 40) + "\n",
		"git-rebase-todo.backup": "pick deadbeef synthetic\n",
		"end":                    "1\n",
		"msgnum":                 "1\n",
	} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("incomplete merge rebase state guard error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(state, "git-rebase-todo"), []byte("pick deadbeef synthetic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("complete fabricated merge rebase state guard error = %v", err)
	}
	if err := os.Remove(filepath.Join(state, "onto")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "onto"), filepath.Join(state, "onto")); err != nil {
		t.Fatal(err)
	}
	if _, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("symlinked rebase state guard error = %v", err)
	}
}

func TestGuardRejectsForgedRebaseStateWithRealGitObjects(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "forged-real-rebase"})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if err := os.WriteFile(filepath.Join(worktree, "forged.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "forged.txt")
	gitTest(t, worktree, "commit", "-m", "feature used by forged rebase")
	originalHead := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	onto := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")
	gitTest(t, worktree, "checkout", "--detach", onto)

	gitDir := gitTestOutput(t, worktree, "rev-parse", "--absolute-git-dir")
	state := filepath.Join(gitDir, "rebase-merge")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"head-name":              "refs/heads/codex/forged-real-rebase\n",
		"orig-head":              originalHead + "\n",
		"onto":                   onto + "\n",
		"git-rebase-todo.backup": "pick " + originalHead + " feature used by forged rebase\n",
		"git-rebase-todo":        "pick " + originalHead + " feature used by forged rebase\n",
		"end":                    "1\n",
		"msgnum":                 "1\n",
	} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// `git rebase --show-current-patch` also consults REBASE_HEAD. Supplying
	// this real commit object makes Git's state-only check accept the forged
	// directory, which is why Guard must additionally require reflog evidence.
	gitTest(t, worktree, "update-ref", "REBASE_HEAD", originalHead)
	if output, err := gitTestRun(worktree, "rebase", "--show-current-patch"); err != nil {
		t.Fatalf("Git did not recognize fully forged rebase state: %v\n%s", err, output)
	}
	if _, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("fully forged real-object rebase state guard error = %v", err)
	}
}

type gitFixture struct {
	projectsRoot string
	canonical    string
	remote       string
	// home is this fixture's resolved WB_HOME (see wbhome.Root), the root
	// under which Create/Guard/List/Cleanup place worktrees — no longer a
	// subdirectory of projectsRoot.
	home string
}

func newGitFixture(t *testing.T) *gitFixture {
	return newGitFixtureForRepository(t, "app")
}

func newGitFixtureForRepository(t *testing.T, repository string) *gitFixture {
	t.Helper()
	root := t.TempDir()
	// Scope WB_HOME to this fixture's own root. Without this, a fresh temp
	// projectsRoot has no legacy .wb, so wbhome.Root falls through to the real
	// ~/.wb — a hermetic test must never write there. Scoping it per fixture,
	// not per package, also keeps each test's worktree root unique even when
	// two tests reuse the same operation name, matching this suite's existing
	// per-fixture isolation.
	home := filepath.Join(root, ".wb")
	t.Setenv(wbhome.EnvOverride, home)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	return newGitFixtureAtRepository(t, root, home, repository)
}

func newDefaultHomeGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	root := t.TempDir()
	homeParent := filepath.Join(root, "home")
	if err := os.MkdirAll(homeParent, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, "")
	t.Setenv(wbhome.EnvMigrationCompat, "")
	t.Setenv("HOME", homeParent)
	return newGitFixtureAt(t, root, filepath.Join(homeParent, ".wb"))
}

func newGitFixtureAt(t *testing.T, root, home string) *gitFixture {
	return newGitFixtureAtRepository(t, root, home, "app")
}

func newGitFixtureAtRepository(t *testing.T, root, home, repository string) *gitFixture {
	t.Helper()
	remote := filepath.Join(root, "remote.git")
	gitTest(t, root, "init", "--bare", "--initial-branch=main", remote)
	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "acme", repository)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "clone", remote, canonical)
	configureGitUser(t, canonical)
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, canonical, "add", "README.md")
	gitTest(t, canonical, "commit", "-m", "initial")
	gitTest(t, canonical, "push", "-u", "origin", "main")
	var err error
	projectsRoot, err = filepath.EvalSymlinks(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		t.Fatal(err)
	}
	// home's own ".wb" leaf doesn't exist until something creates a worktree,
	// so resolve the root that does exist and rejoin — matching wbhome.Root's
	// own resolution (see resolveAbs) for a path that isn't there yet.
	homeParent, err := filepath.EvalSymlinks(filepath.Dir(home))
	if err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(homeParent, filepath.Base(home))
	return &gitFixture{projectsRoot: projectsRoot, canonical: canonical, remote: remote, home: home}
}

func (fixture *gitFixture) pushRemoteCommit(t *testing.T, message string) {
	t.Helper()
	clone := filepath.Join(filepath.Dir(fixture.projectsRoot), "remote-writer")
	command := exec.Command("git", "clone", fixture.remote, clone)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone remote writer: %v\n%s", err, output)
	}
	for _, pair := range [][2]string{{"user.name", "WB Test"}, {"user.email", "wb@example.test"}} {
		command = exec.Command("git", "-C", clone, "config", pair[0], pair[1])
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("configure remote writer: %v\n%s", err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(clone, "remote.txt"), []byte(message+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "remote.txt"}, {"commit", "-m", message}, {"push", "origin", "main"}} {
		command = exec.Command("git", append([]string{"-C", clone}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
}

func configureGitUser(t *testing.T, dir string) {
	t.Helper()
	gitTest(t, dir, "config", "user.name", "WB Test")
	gitTest(t, dir, "config", "user.email", "wb@example.test")
}

func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitTestRun(dir string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func gitTestRunEnv(dir string, environment []string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func gitTestOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
