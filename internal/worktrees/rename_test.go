package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecureRenameHelperRejectsSubstitutedDescriptorsAndGitMetadata(t *testing.T) {
	t.Run("linked .git redirect", func(t *testing.T) {
		fixture, canonical, root, worktree, linked := newSecureRenameHelperFixture(t)
		defer canonical.close()
		defer func() { _ = root.Close() }()
		defer func() { _ = worktree.Close() }()
		defer linked.close()
		if err := os.WriteFile(filepath.Join(fixture.worktreePath, ".git"), []byte("gitdir: /tmp/redirected-linked-git\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		output, err := runSecureRenameHelperForTest(canonical, root, worktree, linked, fixture.worktreesRoot, fixture.worktreePath)
		if err == nil || !strings.Contains(output, "linked worktree Git metadata changed") {
			t.Fatalf("redirected .git helper result: err=%v output=%s", err, output)
		}
	})

	t.Run("worktree path alias", func(t *testing.T) {
		fixture, canonical, root, worktree, linked := newSecureRenameHelperFixture(t)
		defer canonical.close()
		defer func() { _ = root.Close() }()
		defer func() { _ = worktree.Close() }()
		defer linked.close()
		alias := filepath.Join(t.TempDir(), "checkout-alias")
		if err := os.Symlink(fixture.worktreePath, alias); err != nil {
			t.Fatal(err)
		}
		output, err := runSecureRenameHelperForTest(canonical, root, worktree, linked, fixture.worktreesRoot, alias)
		if err == nil || !strings.Contains(output, "managed worktree changed") {
			t.Fatalf("aliased worktree helper result: err=%v output=%s", err, output)
		}
	})

	t.Run("substituted worktrees root", func(t *testing.T) {
		fixture, canonical, root, worktree, linked := newSecureRenameHelperFixture(t)
		defer canonical.close()
		defer func() { _ = root.Close() }()
		defer func() { _ = worktree.Close() }()
		defer linked.close()
		heldRoot := filepath.Join(t.TempDir(), "held-worktrees")
		if err := os.Rename(fixture.worktreesRoot, heldRoot); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(fixture.worktreesRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		output, err := runSecureRenameHelperForTest(canonical, root, worktree, linked, fixture.worktreesRoot, fixture.worktreePath)
		if err == nil || !strings.Contains(output, "managed worktree changed") {
			t.Fatalf("substituted worktrees-root helper result: err=%v output=%s", err, output)
		}
	})

	t.Run("substituted canonical git directory", func(t *testing.T) {
		fixture, canonical, root, worktree, linked := newSecureRenameHelperFixture(t)
		defer canonical.close()
		defer func() { _ = root.Close() }()
		defer func() { _ = worktree.Close() }()
		defer linked.close()
		canonicalGit := filepath.Join(canonical.path, ".git")
		if err := os.Rename(canonicalGit, filepath.Join(t.TempDir(), "held-canonical-git")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(canonicalGit, 0o700); err != nil {
			t.Fatal(err)
		}
		output, err := runSecureRenameHelperForTest(canonical, root, worktree, linked, fixture.worktreesRoot, fixture.worktreePath)
		if err == nil || !strings.Contains(output, "canonical repository changed") {
			t.Fatalf("substituted canonical Git helper result: err=%v output=%s", err, output)
		}
	})

	t.Run("substituted linked admin root", func(t *testing.T) {
		fixture, canonical, root, worktree, linked := newSecureRenameHelperFixture(t)
		defer canonical.close()
		defer func() { _ = root.Close() }()
		defer func() { _ = worktree.Close() }()
		defer linked.close()
		adminRoot := filepath.Join(canonical.path, ".git", "worktrees")
		if err := os.Rename(adminRoot, filepath.Join(t.TempDir(), "held-linked-admin-root")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(adminRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		output, err := runSecureRenameHelperForTest(canonical, root, worktree, linked, fixture.worktreesRoot, fixture.worktreePath)
		if err == nil || !strings.Contains(output, "linked worktree Git metadata changed") {
			t.Fatalf("substituted linked admin-root helper result: err=%v output=%s", err, output)
		}
	})
}

// TestSecureRenameHelperRejectsLateAdminPathRedirection makes the Git
// executable itself attempt the administrative-path replacement after the
// helper's final reauthorization. The lexical GIT_DIR is valid only while the
// capability holds that exact admin descriptor: the swap must be denied, or
// Git must fail closed rather than accepting the replacement.
func TestSecureRenameHelperRejectsLateAdminPathRedirection(t *testing.T) {
	fixture, canonical, root, worktree, linked := newSecureRenameHelperFixture(t)
	defer canonical.close()
	defer func() { _ = root.Close() }()
	defer func() { _ = worktree.Close() }()
	defer linked.close()
	scriptPath := filepath.Join(t.TempDir(), "late-admin-swap-git")
	adminPath := filepath.Join(canonical.path, ".git", "worktrees", linked.adminName)
	movedPath := filepath.Join(filepath.Dir(adminPath), linked.adminName+"-held")
	gitExecutable, err := trustedGitExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte(`#!/bin/sh
set -eu
if mv "$WB_TEST_RENAME_ADMIN" "$WB_TEST_RENAME_HELD" 2>/dev/null && mkdir "$WB_TEST_RENAME_ADMIN" 2>/dev/null; then
  echo wb-test-admin-swap-succeeded >&2
else
  echo wb-test-admin-swap-blocked >&2
fi
exec "$WB_TEST_RENAME_GIT" "$@"
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_RENAME_ADMIN", adminPath)
	t.Setenv("WB_TEST_RENAME_HELD", movedPath)
	t.Setenv("WB_TEST_RENAME_GIT", gitExecutable)
	output, err := runSecureRenameHelperWithGitForTest(canonical, root, worktree, linked, fixture.worktreesRoot, fixture.worktreePath, scriptPath, "status", "--porcelain")
	switch {
	case strings.Contains(output, "wb-test-admin-swap-blocked"):
		if err != nil {
			t.Fatalf("blocked late admin replacement still broke Git: %v\n%s", err, output)
		}
	case strings.Contains(output, "wb-test-admin-swap-succeeded"):
		if err == nil {
			t.Fatalf("late admin replacement was accepted by Git instead of failing closed:\n%s", output)
		}
	default:
		t.Fatalf("late admin replacement had no auditable result: err=%v\n%s", err, output)
	}
}

// TestSecureRenameHelperRejectsLateCommonPathRedirection proves the same
// fail-closed behavior for the lexical GIT_COMMON_DIR spelling. Darwin freezes
// its parent; Landlock binds the capability to the retained common directory.
func TestSecureRenameHelperRejectsLateCommonPathRedirection(t *testing.T) {
	fixture, canonical, root, worktree, linked := newSecureRenameHelperFixture(t)
	defer canonical.close()
	defer func() { _ = root.Close() }()
	defer func() { _ = worktree.Close() }()
	defer linked.close()
	scriptPath := filepath.Join(t.TempDir(), "late-common-swap-git")
	commonPath := filepath.Join(canonical.path, ".git")
	gitExecutable, err := trustedGitExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte(`#!/bin/sh
set -eu
if mv "$WB_TEST_RENAME_COMMON" "$WB_TEST_RENAME_HELD" 2>/dev/null && mkdir "$WB_TEST_RENAME_COMMON" 2>/dev/null; then
  echo wb-test-common-swap-succeeded >&2
else
  echo wb-test-common-swap-blocked >&2
fi
exec "$WB_TEST_RENAME_GIT" "$@"
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_RENAME_COMMON", commonPath)
	t.Setenv("WB_TEST_RENAME_HELD", commonPath+"-held")
	t.Setenv("WB_TEST_RENAME_GIT", gitExecutable)
	output, err := runSecureRenameHelperWithGitForTest(canonical, root, worktree, linked, fixture.worktreesRoot, fixture.worktreePath, scriptPath, "status", "--porcelain")
	switch {
	case strings.Contains(output, "wb-test-common-swap-blocked"):
		if err != nil {
			t.Fatalf("blocked late common replacement still broke Git: %v\n%s", err, output)
		}
	case strings.Contains(output, "wb-test-common-swap-succeeded"):
		if err == nil {
			t.Fatalf("late common replacement was accepted by Git instead of failing closed:\n%s", output)
		}
	default:
		t.Fatalf("late common replacement had no auditable result: err=%v\n%s", err, output)
	}
}

// TestSecureRenameHelperUsesHeldWorktreeAfterLatePathSwap proves that the
// checkout itself is descriptor-anchored: a replacement at its public path
// cannot redirect GIT_WORK_TREE=., which is resolved from the helper's held
// cwd.
func TestSecureRenameHelperUsesHeldWorktreeAfterLatePathSwap(t *testing.T) {
	fixture, canonical, root, worktree, linked := newSecureRenameHelperFixture(t)
	defer canonical.close()
	defer func() { _ = root.Close() }()
	defer func() { _ = worktree.Close() }()
	defer linked.close()
	scriptPath := filepath.Join(t.TempDir(), "late-worktree-swap-git")
	gitExecutable, err := trustedGitExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte(`#!/bin/sh
set -eu
if mv "$WB_TEST_RENAME_WORKTREE" "$WB_TEST_RENAME_HELD" 2>/dev/null && mkdir "$WB_TEST_RENAME_WORKTREE" 2>/dev/null; then
  echo wb-test-worktree-swap-succeeded >&2
else
  echo wb-test-worktree-swap-blocked >&2
fi
exec "$WB_TEST_RENAME_GIT" "$@"
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_RENAME_WORKTREE", fixture.worktreePath)
	t.Setenv("WB_TEST_RENAME_HELD", fixture.worktreePath+"-held")
	t.Setenv("WB_TEST_RENAME_GIT", gitExecutable)
	output, err := runSecureRenameHelperWithGitForTest(canonical, root, worktree, linked, fixture.worktreesRoot, fixture.worktreePath, scriptPath, "status", "--porcelain")
	if err != nil {
		t.Fatalf("late worktree swap helper failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "wb-test-worktree-swap-succeeded") {
		t.Fatalf("late worktree replacement was not exercised:\n%s", output)
	}
}

// TestSecureRenameHelperIgnoresLateCommondirReplacement replaces the mutable
// linked admin commondir file after final authorization. Explicit
// GIT_COMMON_DIR must make this ordinary status operation continue against the
// retained canonical common capability instead of consulting the hostile file.
func TestSecureRenameHelperIgnoresLateCommondirReplacement(t *testing.T) {
	fixture, canonical, root, worktree, linked := newSecureRenameHelperFixture(t)
	defer canonical.close()
	defer func() { _ = root.Close() }()
	defer func() { _ = worktree.Close() }()
	defer linked.close()
	scriptPath := filepath.Join(t.TempDir(), "late-commondir-swap-git")
	commonDirPath := filepath.Join(canonical.path, ".git", "worktrees", linked.adminName, "commondir")
	gitExecutable, err := trustedGitExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte(`#!/bin/sh
set -eu
if printf '%s\\n' /wb-test-hostile-common-dir > "$WB_TEST_RENAME_COMMONDIR" 2>/dev/null; then
  echo wb-test-commondir-swap-succeeded >&2
else
  echo wb-test-commondir-swap-blocked >&2
fi
exec "$WB_TEST_RENAME_GIT" "$@"
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_RENAME_COMMONDIR", commonDirPath)
	t.Setenv("WB_TEST_RENAME_GIT", gitExecutable)
	output, err := runSecureRenameHelperWithGitForTest(canonical, root, worktree, linked, fixture.worktreesRoot, fixture.worktreePath, scriptPath, "status", "--porcelain")
	if err != nil {
		t.Fatalf("late commondir swap helper failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "wb-test-commondir-swap-succeeded") {
		t.Fatalf("late commondir replacement did not execute:\n%s", output)
	}
}

type secureRenameHelperFixture struct {
	worktreesRoot string
	worktreePath  string
}

func newSecureRenameHelperFixture(t *testing.T) (secureRenameHelperFixture, *canonicalRepository, *os.File, *os.File, *linkedWorktreeGitDir) {
	t.Helper()
	gitFixture := newGitFixture(t)
	temporaryRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worktreesRoot := filepath.Join(temporaryRoot, "managed-worktrees")
	if err := os.Mkdir(worktreesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(worktreesRoot, "checkout")
	gitTest(t, gitFixture.canonical, "worktree", "add", "-b", "feature/secure-rename-helper", worktreePath, "main")
	canonical, err := openCanonicalRepository(gitFixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	root, err := openAbsoluteDirectoryNoFollow(worktreesRoot, false)
	if err != nil {
		canonical.close()
		t.Fatal(err)
	}
	worktree, err := openAbsoluteDirectoryNoFollow(worktreePath, false)
	if err != nil {
		_ = root.Close()
		canonical.close()
		t.Fatal(err)
	}
	linked, err := openLinkedWorktreeGitDir(canonical, worktree)
	if err != nil {
		_ = worktree.Close()
		_ = root.Close()
		canonical.close()
		t.Fatal(err)
	}
	return secureRenameHelperFixture{worktreesRoot: worktreesRoot, worktreePath: worktreePath}, canonical, root, worktree, linked
}

func runSecureRenameHelperForTest(canonical *canonicalRepository, root, worktree *os.File, linked *linkedWorktreeGitDir, worktreesRoot, worktreePath string) (string, error) {
	return runSecureRenameHelperWithGitForTest(canonical, root, worktree, linked, worktreesRoot, worktreePath, "/bin/false", "status")
}

func runSecureRenameHelperWithGitForTest(canonical *canonicalRepository, root, worktree *os.File, linked *linkedWorktreeGitDir, worktreesRoot, worktreePath, executable string, gitArgs ...string) (string, error) {
	command := exec.Command(os.Args[0], append([]string{
		SecureRenameGitHelperArgument,
		canonical.path,
		worktreePath,
		worktreesRoot,
		linked.adminName,
		executable,
	}, gitArgs...)...)
	command.ExtraFiles = []*os.File{canonical.root, canonical.common, root, worktree, linked.gitFile, linked.adminRoot, linked.admin}
	output, err := command.CombinedOutput()
	return string(output), err
}

// TestRenameApplyMovesWorktreePreservesExplicitCacheAndSwitchesBranch proves
// an explicitly allowed cache (standing in for node_modules) may survive the
// move, while Git's own
// bookkeeping (`git worktree list`) must report the new path, and the
// worktree must land on a freshly created branch that both `wb worktree
// guard` and Git itself accept.
func TestRenameApplyMovesWorktreePreservesExplicitCacheAndSwitchesBranch(t *testing.T) {
	fixture := newGitFixture(t)
	// A real project ignores node_modules; without that, plain `git status`
	// would call the worktree dirty for the exact directory this verb exists
	// to preserve, which is what every "Clean" check across Create/List/
	// Cleanup already keys off.
	if err := os.WriteFile(filepath.Join(fixture.canonical, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", ".gitignore")
	gitTest(t, fixture.canonical, "commit", "-m", "ignore node_modules")
	gitTest(t, fixture.canonical, "push", "origin", "main")

	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "old-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldWorktree := created[0].WorktreeDir
	untracked := filepath.Join(oldWorktree, "node_modules", "left-behind.txt")
	if err := os.MkdirAll(filepath.Dir(untracked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untracked, []byte("expensive to rebuild\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot:       fixture.projectsRoot,
		OldTask:            "old-task",
		NewTask:            "new-task",
		PreserveCachePaths: []string{"node_modules"},
		DeleteRemote:       true,
		Apply:              true,
	})
	if err != nil {
		t.Fatalf("Rename apply: %v\nresults=%#v", err, outcome.Results)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("rename outcome = %#v", outcome.Results)
	}
	result := outcome.Results[0]
	wantNewWorktree := filepath.Join(fixture.home, "worktrees", "new-task", "acme", "app")
	if result.NewWorktreeDir != wantNewWorktree {
		t.Fatalf("new worktree dir = %s, want %s", result.NewWorktreeDir, wantNewWorktree)
	}
	if result.NewBranch != "new-task" {
		t.Fatalf("default new branch = %q, want new-task", result.NewBranch)
	}
	if result.Repaired {
		t.Fatalf("a healthy `git worktree move` must not need repair: %#v", result)
	}

	// The untracked file — node_modules stand-in — must have made the trip.
	moved := filepath.Join(wantNewWorktree, "node_modules", "left-behind.txt")
	content, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("untracked file did not survive the rename: %v", err)
	}
	if string(content) != "expensive to rebuild\n" {
		t.Fatalf("untracked file content = %q", content)
	}

	// The old path must be gone, and Git's own registration must know it.
	if _, err := os.Stat(oldWorktree); !os.IsNotExist(err) {
		t.Fatalf("old worktree still present: %v", err)
	}
	listing := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if !strings.Contains(listing, "worktree "+wantNewWorktree) {
		t.Fatalf("git worktree list does not report the new path:\n%s", listing)
	}
	if strings.Contains(listing, "worktree "+oldWorktree) {
		t.Fatalf("git worktree list still reports the old path:\n%s", listing)
	}

	// The worktree must be on the new branch, and satisfy Guard.
	branch := gitTestOutput(t, wantNewWorktree, "branch", "--show-current")
	if branch != "new-task" {
		t.Fatalf("checked-out branch = %q, want new-task", branch)
	}
	if _, err := Guard(context.Background(), wantNewWorktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil {
		t.Fatalf("renamed worktree failed guard: %v", err)
	}

	// The old task root is retained (matching Cleanup's convention), not
	// deleted outright, and its lock was retired rather than left held.
	oldTaskRoot := filepath.Join(fixture.home, "worktrees", "old-task")
	if info, statErr := os.Stat(oldTaskRoot); statErr != nil || !info.IsDir() {
		t.Fatalf("old task root should remain in place: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(oldTaskRoot, ".lock")); !os.IsNotExist(statErr) {
		t.Fatalf(".lock must not remain held after rename: %v", statErr)
	}
	entries, err := os.ReadDir(oldTaskRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".wb-retired-lock-") {
			return // Retired, not held or deleted — matches Cleanup's bookkeeping.
		}
	}
	t.Fatalf("old task root has no retired lock sentinel: %#v", entries)
}

func TestRenameSecondRepositoryFailureRollsItBackAndPreservesPartialEvidence(t *testing.T) {
	fixture := newGitFixture(t)
	storageCanonical := filepath.Join(fixture.projectsRoot, "acme", "storage")
	gitTest(t, fixture.projectsRoot, "clone", fixture.remote, storageCanonical)
	created, err := Create(context.Background(), []string{"acme/app", "acme/storage"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "multi-recycle-old",
		WorkLog:      WorkLogOptions{RunID: "multi-recycle-run"},
	})
	if err != nil {
		t.Fatal(err)
	}
	oldProjections := make([]workLogProjection, len(created))
	for index := range created {
		oldProjections[index], err = readWorkLogProjection(created[index].WorktreeDir)
		if err != nil {
			t.Fatal(err)
		}
	}
	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "multi-recycle-old",
		NewTask:      "multi-recycle-new",
		DeleteRemote: true,
		Apply:        true,
		beforeRenameBind: func(repository string) error {
			if repository == "acme/storage" {
				return errors.New("injected second-repository bind failure")
			}
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "injected second-repository bind failure") {
		t.Fatalf("rename error = %v", err)
	}
	if len(outcome.Results) != 2 || outcome.Results[0].Applied || outcome.Results[1].Applied {
		t.Fatalf("coordinated rollback evidence = %#v", outcome.Results)
	}
	if outcome.ReportPath == "" {
		t.Fatal("partial rename did not preserve a durable report path")
	}
	for index, create := range created {
		if _, err := os.Stat(create.WorktreeDir); err != nil {
			t.Fatalf("repository %s was not restored to source: %v", create.Repository, err)
		}
		if _, err := os.Stat(outcome.Results[index].NewWorktreeDir); !os.IsNotExist(err) {
			t.Fatalf("repository %s remains at destination: %v", create.Repository, err)
		}
		canonical := fixture.canonical
		if create.Repository == "acme/storage" {
			canonical = storageCanonical
		}
		if !gitRefExists(canonical, "refs/heads/"+create.Branch) || gitRefExists(canonical, "refs/heads/multi-recycle-new") {
			t.Fatalf("%s rollback did not restore exact old/remove new branch refs", create.Repository)
		}
		recoveryProjection, projectionErr := readWorkLogProjection(create.WorktreeDir)
		if projectionErr != nil {
			t.Fatal(projectionErr)
		}
		if recoveryProjection.Lifecycle != "active" || recoveryProjection.ClaimID == oldProjections[index].ClaimID {
			t.Fatalf("%s recovery claim = %#v, old = %#v", create.Repository, recoveryProjection, oldProjections[index])
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.home, "worktrees", "multi-recycle-new")); !os.IsNotExist(err) {
		t.Fatalf("coordinated rollback stranded destination task: %v", err)
	}

	// The same operation is retryable without manually deleting a partial
	// destination or repairing branch refs.
	retried, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "multi-recycle-old",
		NewTask:      "multi-recycle-new",
		DeleteRemote: true,
		Apply:        true,
	})
	if err != nil {
		t.Fatalf("retry coordinated rename: %v\nresults=%#v", err, retried.Results)
	}
	for _, result := range retried.Results {
		if !result.Applied {
			t.Fatalf("retry left repository unapplied: %#v", retried.Results)
		}
	}
}

// TestRenameFallsBackToMoveAndRepairWhenGitWorktreeMoveRefuses is the
// regression test for the founder's own production incident: a bare
// filesystem move leaves Git's gitdir pointer stale, and it must never be the
// first choice. This test forces `git worktree move` itself to refuse — by
// taking a real Git-level worktree lock, independent of WB's own task
// `.lock` — so the fallback path (plain move + `git worktree repair`) is the
// one actually exercised and verified, not merely present as dead code.
func TestRenameFallsBackToMoveAndRepairWhenGitWorktreeMoveRefuses(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "locked-move-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "lock", created[0].WorktreeDir, "--reason", "simulate git worktree move refusing")
	if output, moveErr := gitTestRun(fixture.canonical, "worktree", "move", created[0].WorktreeDir, filepath.Join(fixture.home, "elsewhere")); moveErr == nil {
		t.Fatalf("test setup assumption broken: a locked worktree must refuse `git worktree move`, output=%s", output)
	}

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "locked-move-task",
		NewTask:      "locked-move-task-renamed",
		DeleteRemote: true,
		Apply:        true,
	})
	if err != nil {
		t.Fatalf("Rename apply: %v\nresults=%#v", err, outcome.Results)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied || !outcome.Results[0].Repaired {
		t.Fatalf("fallback rename outcome = %#v", outcome.Results)
	}
	newWorktree := outcome.Results[0].NewWorktreeDir
	listing := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if !strings.Contains(listing, "worktree "+newWorktree) {
		t.Fatalf("git worktree list does not report the repaired path:\n%s", listing)
	}
	if _, guardErr := Guard(context.Background(), newWorktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); guardErr != nil {
		t.Fatalf("repaired worktree failed guard: %v", guardErr)
	}
}

// TestRenamePlanDoesNotMutateAnything proves the dry-run default is truly
// read-only: no directory moves, no branch changes, no report file.
func TestRenamePlanDoesNotMutateAnything(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "plan-only",
	})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "plan-only",
		NewTask:      "plan-only-renamed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Eligible || outcome.Results[0].Applied {
		t.Fatalf("plan outcome = %#v", outcome.Results)
	}
	if outcome.ReportPath != "" {
		t.Fatalf("a dry-run plan must not write a report: %q", outcome.ReportPath)
	}
	if _, err := os.Stat(created[0].WorktreeDir); err != nil {
		t.Fatalf("dry-run moved the worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.home, "worktrees", "plan-only-renamed")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created the destination task directory: %v", err)
	}
}

func TestRenameApplyRequiresExplicitRemoteRetirementBeforeMutation(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "remote-retirement-required",
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "remote-retirement-required",
		NewTask:      "remote-retirement-required-next",
		Apply:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "--remote") {
		t.Fatalf("missing remote authorization error = %v, results=%#v", err, outcome.Results)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("missing --remote mutated source: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.home, "worktrees", "remote-retirement-required-next")); !os.IsNotExist(statErr) {
		t.Fatalf("missing --remote created destination: %v", statErr)
	}
}

// TestRenameRefusesDirtyWorktree matches Cleanup's own safety posture: a
// worktree with local changes must never be renamed out from under whatever
// process is relying on its current path.
func TestRenameRefusesDirtyWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "dirty-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "dirty-task",
		NewTask:      "dirty-task-renamed",
		DeleteRemote: true,
		Apply:        true,
	})
	if err == nil {
		t.Fatalf("dirty worktree rename unexpectedly succeeded: %#v", outcome.Results)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Eligible || outcome.Results[0].Applied {
		t.Fatalf("dirty rename outcome = %#v", outcome.Results)
	}
	if !strings.Contains(outcome.Results[0].Reason, "local changes") {
		t.Fatalf("dirty rename reason = %q", outcome.Results[0].Reason)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("dirty worktree was moved despite refusal: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.home, "worktrees", "dirty-task-renamed")); !os.IsNotExist(statErr) {
		t.Fatalf("destination task directory was created despite refusal: %v", statErr)
	}
}

// TestRenameRefusesLockedTask mirrors List/Cleanup's own `.lock`-presence
// check: a task with an active or interrupted operation must never be
// renamed out from under it.
func TestRenameRefusesLockedTask(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "locked-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(fixture.home, "worktrees", "locked-task", ".lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "locked-task",
		NewTask:      "locked-task-renamed",
		DeleteRemote: true,
		Apply:        true,
	})
	if err == nil {
		t.Fatalf("locked task rename unexpectedly succeeded: %#v", outcome.Results)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Eligible || !strings.Contains(outcome.Results[0].Reason, "locked") {
		t.Fatalf("locked rename outcome = %#v", outcome.Results)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("locked worktree was moved despite refusal: %v", statErr)
	}
}

// TestRenameRefusesDestinationCollision proves a task directory that already
// exists at the destination name blocks the whole rename atomically, rather
// than partially adopting some repositories into someone else's task.
func TestRenameRefusesDestinationCollision(t *testing.T) {
	fixture := newGitFixture(t)
	if _, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "source-task",
	}); err != nil {
		t.Fatal(err)
	}
	// Occupy the destination name with an unrelated task.
	if _, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "taken-task",
		Resume:       false,
	}); err != nil {
		t.Fatal(err)
	}

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "source-task",
		NewTask:      "taken-task",
		DeleteRemote: true,
		Apply:        true,
	})
	if err == nil {
		t.Fatalf("collision rename unexpectedly succeeded: %#v", outcome.Results)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Eligible || !strings.Contains(outcome.Results[0].Reason, "already exists") {
		t.Fatalf("collision rename outcome = %#v", outcome.Results)
	}
	source := filepath.Join(fixture.home, "worktrees", "source-task", "acme", "app")
	if _, statErr := os.Stat(source); statErr != nil {
		t.Fatalf("source worktree was moved despite collision refusal: %v", statErr)
	}
}

// TestRenameDeleteOldBranchRequiresForceUnlessMerged is the regression test
// for the founder's explicit "unmerged work must never be silently lost"
// requirement: recycle refuses an unmerged branch before any move unless
// --force explicitly authorizes discarded work.
func TestRenameDeleteOldBranchRequiresForceUnlessMerged(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "unmerged-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "feature.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, created[0].WorktreeDir, "add", "feature.txt")
	gitTest(t, created[0].WorktreeDir, "commit", "-m", "unmerged work")

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "unmerged-task",
		NewTask:      "unmerged-task-renamed",
		DeleteRemote: true,
		Apply:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "not integrated") {
		t.Fatalf("unmerged Rename error = %v, results=%#v", err, outcome.Results)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("unmerged worktree moved despite refusal: %v", statErr)
	}
	if !localBranchExistsIn(t, fixture.canonical, "unmerged-task") {
		t.Fatal("unmerged old branch was deleted despite missing --force")
	}

	// The explicit discarded-work retry must recycle and delete the old branch.
	outcome, err = Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "unmerged-task",
		NewTask:      "unmerged-task-final",
		Branch:       "feature/unmerged-task-final",
		Force:        true,
		DeleteRemote: true,
		Apply:        true,
	})
	if err != nil {
		t.Fatalf("forced Rename apply: %v\nresults=%#v", err, outcome.Results)
	}
	if !outcome.Results[0].Applied || !outcome.Results[0].OldBranchDeleted || outcome.Results[0].NewBranch != "feature/unmerged-task-final" {
		t.Fatalf("forced delete did not remove the unmerged branch: %#v", outcome.Results[0])
	}
	if localBranchExistsIn(t, fixture.canonical, "unmerged-task") {
		t.Fatal("unmerged old branch survived --force")
	}
}

// TestRenameDeleteOldBranchWithoutForceWhenMerged proves the safety check is
// specifically about merge state, not a blanket refusal: a branch that is
// already merged into base is deleted without needing --force.
func TestRenameDeleteOldBranchWithoutForceWhenMerged(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "merged-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "feature.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, created[0].WorktreeDir, "add", "feature.txt")
	gitTest(t, created[0].WorktreeDir, "commit", "-m", "merged work")
	gitTest(t, created[0].WorktreeDir, "push", "-u", "origin", "merged-task")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "merged-task", "-m", "merge feature")
	gitTest(t, fixture.canonical, "push", "origin", "main")

	outcome, err := Rename(context.Background(), RenameOptions{
		ProjectsRoot:    fixture.projectsRoot,
		OldTask:         "merged-task",
		NewTask:         "merged-task-renamed",
		DeleteOldBranch: true,
		DeleteRemote:    true,
		Apply:           true,
	})
	if err != nil {
		t.Fatalf("Rename apply: %v\nresults=%#v", err, outcome.Results)
	}
	if !outcome.Results[0].Applied || !outcome.Results[0].OldBranchDeleted || !outcome.Results[0].OldRemoteDeleted {
		t.Fatalf("merged local/remote branch was not retired without --force: %#v", outcome.Results[0])
	}
	if localBranchExistsIn(t, fixture.canonical, "merged-task") {
		t.Fatal("merged old branch survived deletion")
	}
	if remoteHead, err := remoteBranchHead(context.Background(), fixture.canonical, "merged-task"); err != nil || remoteHead != "" {
		t.Fatalf("merged old remote branch remains at %q: %v", remoteHead, err)
	}
}

func localBranchExistsIn(t *testing.T, canonicalDir, branch string) bool {
	t.Helper()
	exists, err := localBranchExists(context.Background(), canonicalDir, branch)
	if err != nil {
		t.Fatal(err)
	}
	return exists
}

// TestRenameEligibilityAndCoordination is a fast, non-Git unit test of the
// all-or-nothing coordination rule: one ineligible or malformed repository
// blocks every sibling in the same rename, exactly like Cleanup's
// blockDiagnosedTasks/blockUnsafeTasks — moving part of a task and leaving
// the rest behind would strand the very recycling this verb exists for.
func TestRenameEligibilityAndCoordination(t *testing.T) {
	clean := ListResult{Repository: "acme/app", Clean: true}
	if eligible, reason := renameEligibility(clean); !eligible || reason != "" {
		t.Fatalf("clean entry eligibility = %v, %q", eligible, reason)
	}
	dirty := ListResult{Repository: "acme/app", Clean: false}
	if eligible, reason := renameEligibility(dirty); eligible || !strings.Contains(reason, "local changes") {
		t.Fatalf("dirty entry eligibility = %v, %q", eligible, reason)
	}
	locked := ListResult{Repository: "acme/app", Clean: true, Locked: true}
	if eligible, reason := renameEligibility(locked); eligible || !strings.Contains(reason, "locked") {
		t.Fatalf("locked entry eligibility = %v, %q", eligible, reason)
	}

	plans := []renamePlan{
		{entry: clean, result: RenameResult{Repository: "acme/app", Eligible: true}},
		{entry: dirty, result: RenameResult{Repository: "acme/api", Eligible: false, Reason: "worktree has local changes"}},
	}
	blockRenameTask(plans, nil, "")
	for _, plan := range plans {
		if plan.result.Eligible {
			t.Fatalf("one ineligible repository must block every sibling: %#v", plans)
		}
	}
	if !strings.Contains(plans[0].result.Reason, "coordinated task blocked by") {
		t.Fatalf("blocked sibling reason = %q", plans[0].result.Reason)
	}
	if plans[1].result.Reason != "worktree has local changes" {
		t.Fatalf("original culprit reason must stay unprefixed: %q", plans[1].result.Reason)
	}

	// A destination collision is an absolute, unconditional blocker.
	plans = []renamePlan{{entry: clean, result: RenameResult{Repository: "acme/app", Eligible: true}}}
	blockRenameTask(plans, nil, "destination task already exists: /wb/worktrees/taken")
	if plans[0].result.Eligible || plans[0].result.Reason != "destination task already exists: /wb/worktrees/taken" {
		t.Fatalf("destination collision result = %#v", plans[0].result)
	}
}

func TestDefaultRenameReportDir(t *testing.T) {
	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	got := DefaultRenameReportDir("/home/.wb", now)
	want := filepath.Join("/home/.wb", "reports", "worktree-rename", "20260810T120000.000000000Z")
	if got != want {
		t.Fatalf("DefaultRenameReportDir = %s, want %s", got, want)
	}
}
