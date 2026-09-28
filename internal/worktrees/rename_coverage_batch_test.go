package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/unixcompat"
)

const renameCoverageHead = "0123456789abcdef0123456789abcdef01234567"

func TestRenameCoverageSecureDescriptorErrors(t *testing.T) {
	t.Parallel()

	if err := runSecureRenameGit(t.Context(), filepath.Join(t.TempDir(), "missing"), t.TempDir(), filepath.Join(t.TempDir(), "missing"), "status"); err == nil || !strings.Contains(err.Error(), "open managed worktree") {
		t.Fatalf("missing worktree error = %v", err)
	}

	canonicalPath, _ := newRenameCoverageCanonical(t)
	worktreesRoot := t.TempDir()
	worktreePath := filepath.Join(worktreesRoot, "worktree")
	if err := os.Mkdir(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree, err := os.Open(worktreePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worktree.Close() })

	if _, err := runSecureRenameGitBytesWithHeldWorktree(t.Context(), filepath.Join(t.TempDir(), "missing"), worktreesRoot, worktreePath, worktree, "status"); err == nil {
		t.Fatal("missing canonical repository was accepted")
	}
	if _, err := runSecureRenameGitBytesWithHeldWorktree(t.Context(), canonicalPath, filepath.Join(t.TempDir(), "missing"), worktreePath, worktree, "status"); err == nil || !strings.Contains(err.Error(), "open managed worktrees root") {
		t.Fatalf("missing worktrees root error = %v", err)
	}
	movedPath := worktreePath + "-moved"
	if err := os.Rename(worktreePath, movedPath); err != nil {
		t.Fatal(err)
	}
	if _, err := runSecureRenameGitBytesWithHeldWorktree(t.Context(), canonicalPath, worktreesRoot, worktreePath, worktree, "status"); err == nil || !strings.Contains(err.Error(), "managed rename path changed") {
		t.Fatalf("changed worktree path error = %v", err)
	}
	if err := os.Rename(movedPath, worktreePath); err != nil {
		t.Fatal(err)
	}
	if _, err := runSecureRenameGitBytesWithHeldWorktree(t.Context(), canonicalPath, worktreesRoot, worktreePath, worktree, "status"); err == nil || !strings.Contains(err.Error(), "retain linked worktree Git metadata") {
		t.Fatalf("missing linked metadata error = %v", err)
	}
}

func TestRenameCoverageLinkedGitDescriptors(t *testing.T) {
	t.Parallel()

	var nilLinked *linkedWorktreeGitDir
	nilLinked.close()
	if _, err := openLinkedWorktreeGitDir(nil, nil); err == nil {
		t.Fatal("nil linked-worktree descriptors were accepted")
	}

	canonicalPath, canonical := newRenameCoverageCanonical(t)
	worktreePath := t.TempDir()
	worktree, err := os.Open(worktreePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worktree.Close() })
	if _, err := openLinkedWorktreeGitDir(canonical, worktree); err == nil || !strings.Contains(err.Error(), "open worktree .git") {
		t.Fatalf("missing .git error = %v", err)
	}
	if _, err := linkedWorktreeGitFileAdminName(nil, nil); err == nil || !strings.Contains(err.Error(), "descriptors are unavailable") {
		t.Fatalf("nil linked gitfile error = %v", err)
	}

	adminName := "coverage-admin"
	gitFilePath := filepath.Join(worktreePath, ".git")
	if err := os.WriteFile(gitFilePath, []byte("gitdir: "+filepath.Join(canonicalPath, ".git", "worktrees", adminName)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openLinkedWorktreeGitDir(canonical, worktree); err == nil || !strings.Contains(err.Error(), "metadata root") {
		t.Fatalf("missing admin root error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(canonicalPath, ".git", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openLinkedWorktreeGitDir(canonical, worktree); err == nil || !strings.Contains(err.Error(), "Git directory") {
		t.Fatalf("missing admin directory error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(canonicalPath, ".git", "worktrees", adminName), 0o755); err != nil {
		t.Fatal(err)
	}
	linked, err := openLinkedWorktreeGitDir(canonical, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if linked.adminName != adminName || linked.gitFile == nil || linked.adminRoot == nil || linked.admin == nil {
		t.Fatalf("linked descriptors = %#v", linked)
	}
	linked.close()
}

func TestRenameCoverageLinkedGitDescriptorSubstitution(t *testing.T) {
	t.Parallel()

	canonicalPath, canonical := newRenameCoverageCanonical(t)
	worktreePath := t.TempDir()
	worktree := wtLifeCovOpenDirectory(t, worktreePath)
	adminName := "coverage-admin"
	adminRoot := filepath.Join(canonicalPath, ".git", "worktrees")
	adminPath := filepath.Join(adminRoot, adminName)
	if err := os.MkdirAll(adminPath, 0o755); err != nil {
		t.Fatal(err)
	}
	gitFilePath := filepath.Join(worktreePath, ".git")
	if err := os.WriteFile(gitFilePath, []byte("gitdir: "+adminPath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	linked, err := openLinkedWorktreeGitDir(canonical, worktree, func() {
		if renameErr := os.Rename(gitFilePath, gitFilePath+".held"); renameErr != nil {
			t.Fatal(renameErr)
		}
		if writeErr := os.WriteFile(gitFilePath, []byte("replacement"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	})
	if linked != nil || err == nil || !strings.Contains(err.Error(), "metadata changed") {
		t.Fatalf("substituted linked metadata = %#v, %v", linked, err)
	}
}

func TestRenameCoverageLinkedGitDescriptorRejectsUnsafeGitFile(t *testing.T) {
	t.Parallel()

	_, canonical := newRenameCoverageCanonical(t)
	worktreePath := t.TempDir()
	worktree := wtLifeCovOpenDirectory(t, worktreePath)
	if err := os.WriteFile(filepath.Join(worktreePath, ".git"), []byte("gitdir: relative\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if linked, err := openLinkedWorktreeGitDir(canonical, worktree); linked != nil || err == nil || !strings.Contains(err.Error(), "unsafe gitdir") {
		t.Fatalf("unsafe linked gitfile = %#v, %v", linked, err)
	}
}

func TestRenameCoverageLinkedGitFileFailures(t *testing.T) {
	t.Parallel()

	canonicalPath, canonical := newRenameCoverageCanonical(t)
	closedPath := filepath.Join(t.TempDir(), ".git")
	if err := os.WriteFile(closedPath, []byte("gitdir: /tmp/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	closed, err := os.Open(closedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := linkedWorktreeGitFileAdminName(canonical, closed); err == nil || !strings.Contains(err.Error(), "inspect linked worktree") {
		t.Fatalf("closed gitfile error = %v", err)
	}

	largePath := filepath.Join(t.TempDir(), ".git")
	if err := os.WriteFile(largePath, make([]byte, maxLinkedWorktreeGitFileSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	large, err := os.Open(largePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = large.Close() })
	if _, err := linkedWorktreeGitFileAdminName(canonical, large); err == nil || !strings.Contains(err.Error(), "regular file no larger") {
		t.Fatalf("oversized gitfile error = %v", err)
	}

	writeOnlyPath := filepath.Join(t.TempDir(), ".git")
	if err := os.WriteFile(writeOnlyPath, []byte("gitdir: "+filepath.Join(canonicalPath, ".git", "worktrees", "admin")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeOnly, err := os.OpenFile(writeOnlyPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writeOnly.Close() })
	if _, err := linkedWorktreeGitFileAdminName(canonical, writeOnly); err == nil || !strings.Contains(err.Error(), "read linked worktree") {
		t.Fatalf("write-only gitfile error = %v", err)
	}

	unsafePath := filepath.Join(t.TempDir(), ".git")
	if err := os.WriteFile(unsafePath, []byte("gitdir: relative\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsafe, err := os.Open(unsafePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unsafe.Close() })
	if _, err := linkedWorktreeGitFileAdminName(canonical, unsafe); err == nil || !strings.Contains(err.Error(), "unsafe gitdir") {
		t.Fatalf("unsafe gitfile error = %v", err)
	}
	outsidePath := filepath.Join(t.TempDir(), ".git")
	if err := os.WriteFile(outsidePath, []byte("gitdir: "+filepath.Join(t.TempDir(), "admin")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside, err := os.Open(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outside.Close() })
	if _, err := linkedWorktreeGitFileAdminName(canonical, outside); err == nil || !strings.Contains(err.Error(), "points outside") {
		t.Fatalf("outside gitfile error = %v", err)
	}

	closedAfterStatPath := filepath.Join(t.TempDir(), ".git")
	if err := os.WriteFile(closedAfterStatPath, []byte("gitdir: "+filepath.Join(canonicalPath, ".git", "worktrees", "admin")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	closedAfterStat, err := os.Open(closedAfterStatPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := linkedWorktreeGitFileAdminName(canonical, closedAfterStat, func() { _ = closedAfterStat.Close() }); err == nil || !strings.Contains(err.Error(), "rewind linked worktree") {
		t.Fatalf("closed-after-stat gitfile error = %v", err)
	}

	growingPath := filepath.Join(t.TempDir(), ".git")
	if err := os.WriteFile(growingPath, []byte("gitdir: "+filepath.Join(canonicalPath, ".git", "worktrees", "admin")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	growing, err := os.Open(growingPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = growing.Close() })
	if _, err := linkedWorktreeGitFileAdminName(canonical, growing, func() {
		if writeErr := os.WriteFile(growingPath, make([]byte, maxLinkedWorktreeGitFileSize+1), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("growing gitfile error = %v", err)
	}
}

func TestRenameCoverageRegularFileIdentityAndDescriptorRetention(t *testing.T) {
	t.Parallel()

	if regularFileEntryStillMatches(nil, ".git", nil) {
		t.Fatal("nil file identity matched")
	}
	parentPath := t.TempDir()
	parent, err := os.Open(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	if regularFileEntryStillMatches(parent, ".git", parent) {
		t.Fatal("missing child matched")
	}
	filePath := filepath.Join(parentPath, ".git")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !regularFileEntryStillMatches(parent, ".git", expected) {
		t.Fatal("same regular file did not match")
	}
	if err := expected.Close(); err != nil {
		t.Fatal(err)
	}
	if regularFileEntryStillMatches(parent, ".git", expected) {
		t.Fatal("closed expected file matched")
	}

	if err := retainDescriptorsAcrossGitExec(nil); err == nil || !strings.Contains(err.Error(), "missing inherited descriptor") {
		t.Fatalf("nil descriptor error = %v", err)
	}
	closed, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := retainDescriptorsAcrossGitExec(closed); err == nil {
		t.Fatal("closed descriptor was retained")
	}
	open, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = open.Close() })
	if err := retainDescriptorsAcrossGitExec(open); err != nil {
		t.Fatalf("retain open descriptor: %v", err)
	}
	wantSetError := errors.New("set descriptor flags")
	calls := 0
	if err := retainDescriptorsAcrossGitExecWith(func(uintptr, int, int) (int, error) {
		calls++
		if calls == 2 {
			return 0, wantSetError
		}
		return unix.FD_CLOEXEC, nil
	}, open); !errors.Is(err, wantSetError) {
		t.Fatalf("set descriptor flags error = %v", err)
	}
}

func TestRenameCoverageNormalizeReportPathFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("resolve report path")
	_, err := normalizeRenameOptions(RenameOptions{
		ProjectsRoot: t.TempDir(), OldTask: "old-task", NewTask: "new-task", Base: "main",
		ReportDir: "relative", WorkLog: WorkLogOptions{Model: "unknown"},
		absoluteReportDir: func(string) (string, error) { return "", want },
	})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "resolve rename report directory") {
		t.Fatalf("report path error = %v", err)
	}
}

func TestRenameCoveragePlanningAndPreflightFailures(t *testing.T) {
	t.Parallel()

	if _, _, err := resolveRenameBranch(t.Context(), RenameOptions{Base: "main"}, ListResult{CanonicalDir: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing canonical repository resolved a rename branch")
	}
	canonicalPath, _ := newRenameCoverageCanonical(t)
	ctx := withCanonicalGitInterceptor(t.Context(), func(context.Context, []string, func() ([]byte, error)) ([]byte, error) {
		return nil, errors.New("injected synchronization failure")
	})
	if _, _, err := resolveRenameBranch(ctx, RenameOptions{Base: "main"}, ListResult{CanonicalDir: canonicalPath, Repository: "acme/app"}); err == nil || !strings.Contains(err.Error(), "injected synchronization failure") {
		t.Fatalf("synchronization error = %v", err)
	}
	if err := applyRename(t.Context(), t.TempDir(), RenameOptions{}, &renamePlan{entry: ListResult{Repository: "invalid"}}); err == nil {
		t.Fatal("invalid repository reached rename mutation")
	}
	badPlan := &renamePlan{entry: ListResult{Repository: "acme/app", WorktreeDir: filepath.Join(t.TempDir(), "missing")}}
	if err := preflightRename(t.Context(), RenameOptions{ProjectsRoot: t.TempDir(), OldTask: "old", Base: "main"}, badPlan); err == nil || !strings.Contains(err.Error(), "preflight acme/app") {
		t.Fatalf("missing preflight worktree error = %v", err)
	}

	local := &renamePlan{destinationLocal: true, entry: ListResult{CanonicalDir: filepath.Join(t.TempDir(), "missing")}}
	if err := prepareRenamePhysicalDestination(t.Context(), "new", local); err == nil {
		t.Fatal("missing local canonical destination was prepared")
	}
	if err := preflightRenamePhysicalDestination(t.Context(), "new", local); err == nil {
		t.Fatal("missing local canonical destination passed preflight")
	}
	shared := &renamePlan{destinationRoot: t.TempDir(), result: RenameResult{NewWorktreeDir: filepath.Join(t.TempDir(), "elsewhere")}}
	if err := prepareRenamePhysicalDestination(t.Context(), "new", shared); err == nil || !strings.Contains(err.Error(), "not below its task directory") {
		t.Fatalf("malformed shared destination error = %v", err)
	}
}

func TestRenameCoverageRollbackAggregation(t *testing.T) {
	t.Parallel()

	plan := renamePlan{
		entry:  ListResult{Repository: "acme/app", CanonicalDir: filepath.Join(t.TempDir(), "missing"), Branch: "old", HeadSHA: renameCoverageHead},
		result: RenameResult{Applied: true},
	}
	plan.oldBranchDeleted = true
	if err := rollbackRenamePlan(t.Context(), t.TempDir(), &plan); err == nil {
		t.Fatal("rollback unexpectedly opened a missing canonical repository")
	}
	if err := rollbackAppliedRenames(t.Context(), t.TempDir(), []renamePlan{plan}); err == nil || !strings.Contains(err.Error(), "acme/app") {
		t.Fatalf("aggregated rollback error = %v", err)
	}
}

func TestRenameCoverageRecycleStateFailures(t *testing.T) {
	t.Parallel()

	const worktree = "/fixture/worktree"
	for name, result := range map[string]struct {
		output string
		err    error
	}{
		"git failure": {err: errors.New("injected clean failure")},
		"residue":     {output: "Would remove credentials"},
	} {
		name, result := name, result
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			fake.Expect(func(call runnertest.Call) bool {
				return ordinaryGitCall(call, worktree, "clean")
			}, runner.Result{CombinedOutput: result.output}, result.err)
			err := verifyRecycleState(withGitRunner(t.Context(), fake), worktree, []string{"node_modules"})
			if err == nil {
				t.Fatalf("verifyRecycleState(%s) succeeded", name)
			}
		})
	}
}

func TestRenameCoverageMoveWorktreeStages(t *testing.T) {
	t.Parallel()

	if outcome, err := moveWorktree(t.Context(), "", "", filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "new"), worktreeMoveHooks{}); err == nil || outcome.Moved {
		t.Fatalf("missing source move = (%#v, %v)", outcome, err)
	}

	cases := []struct {
		name         string
		hooks        worktreeMoveHooks
		wantRepaired bool
		wantError    string
	}{
		{
			name: "before repair",
			hooks: worktreeMoveHooks{
				beforeRepair: func() error { return errors.New("before repair") },
			},
			wantError: "before repair",
		},
		{
			name: "repair",
			hooks: worktreeMoveHooks{
				repair: func(context.Context, string, string, string, *os.File) error { return errors.New("repair failed") },
			},
			wantError: "repair failed",
		},
		{
			name: "before verify",
			hooks: worktreeMoveHooks{
				repair:                   func(context.Context, string, string, string, *os.File) error { return nil },
				beforeRegistrationVerify: func() error { return errors.New("before verify") },
			},
			wantRepaired: true,
			wantError:    "before verify",
		},
		{
			name: "verify",
			hooks: worktreeMoveHooks{
				repair: func(context.Context, string, string, string, *os.File) error { return nil },
				verify: func(context.Context, string, string, string, *os.File) error { return errors.New("verify failed") },
			},
			wantRepaired: true,
			wantError:    "verify failed",
		},
		{
			name: "success",
			hooks: worktreeMoveHooks{
				afterAuthorization:       func() {},
				beforeRepair:             func() error { return nil },
				repair:                   func(context.Context, string, string, string, *os.File) error { return nil },
				beforeRegistrationVerify: func() error { return nil },
				verify:                   func(context.Context, string, string, string, *os.File) error { return nil },
			},
			wantRepaired: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			oldPath, newPath, root := newRenameCoverageMovePaths(t)
			outcome, err := moveWorktree(t.Context(), filepath.Join(t.TempDir(), "canonical"), root, oldPath, newPath, tc.hooks)
			if !outcome.Moved || outcome.Repaired != tc.wantRepaired {
				t.Fatalf("outcome = %#v", outcome)
			}
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
		})
	}

	t.Run("default repair", func(t *testing.T) {
		t.Parallel()
		oldPath, newPath, root := newRenameCoverageMovePaths(t)
		outcome, err := moveWorktree(t.Context(), filepath.Join(t.TempDir(), "missing"), root, oldPath, newPath, worktreeMoveHooks{})
		if err == nil || !outcome.Moved || outcome.Repaired || !strings.Contains(err.Error(), "repair Git registration") {
			t.Fatalf("default repair = (%#v, %v)", outcome, err)
		}
	})

	t.Run("default verify", func(t *testing.T) {
		t.Parallel()
		canonicalPath, _ := newRenameCoverageCanonical(t)
		oldPath, newPath, root := newRenameCoverageMovePaths(t)
		ctx := withCanonicalGitInterceptor(t.Context(), func(_ context.Context, args []string, _ func() ([]byte, error)) ([]byte, error) {
			if strings.Join(args, " ") != "worktree list --porcelain" {
				t.Fatalf("unexpected canonical Git args: %q", args)
			}
			return []byte("worktree " + newPath + "\n"), nil
		})
		outcome, err := moveWorktree(ctx, canonicalPath, root, oldPath, newPath, worktreeMoveHooks{
			repair: func(context.Context, string, string, string, *os.File) error { return nil },
		})
		if err != nil || !outcome.Moved || !outcome.Repaired {
			t.Fatalf("default verify = (%#v, %v)", outcome, err)
		}
	})
}

func TestRenameCoverageMoveDirectoryOpenFailures(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if _, err := moveRenameDirectory(filepath.Join(root, "missing", "old"), filepath.Join(root, "new"), nil); err == nil {
		t.Fatal("missing old parent was accepted")
	}
	oldParent := filepath.Join(root, "old-parent")
	if err := os.Mkdir(oldParent, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := moveRenameDirectory(filepath.Join(oldParent, "old"), filepath.Join(root, "missing", "new"), nil); err == nil {
		t.Fatal("missing new parent was accepted")
	}
	newParent := filepath.Join(root, "new-parent")
	if err := os.Mkdir(newParent, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := moveRenameDirectory(filepath.Join(oldParent, "old"), filepath.Join(newParent, "new"), nil); err == nil {
		t.Fatal("missing old directory was accepted")
	}
}

func TestRenameCoverageMoveDirectoryRaceStages(t *testing.T) {
	t.Parallel()

	newPaths := func(t *testing.T) (string, string, string, string) {
		t.Helper()
		root := t.TempDir()
		oldParent := filepath.Join(root, "old-parent")
		newParent := filepath.Join(root, "new-parent")
		oldPath := filepath.Join(oldParent, "old")
		newPath := filepath.Join(newParent, "new")
		if err := os.MkdirAll(oldPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(newParent, 0o755); err != nil {
			t.Fatal(err)
		}
		return oldParent, newParent, oldPath, newPath
	}

	t.Run("parent substitution", func(t *testing.T) {
		t.Parallel()
		oldParent, _, oldPath, newPath := newPaths(t)
		moved, err := moveRenameDirectoryWithHooks(oldPath, newPath, moveRenameDirectoryHooks{beforeIdentityCheck: func() {
			if renameErr := os.Rename(oldParent, oldParent+"-held"); renameErr != nil {
				t.Fatal(renameErr)
			}
			if mkdirErr := os.Mkdir(oldParent, 0o755); mkdirErr != nil {
				t.Fatal(mkdirErr)
			}
		}})
		if moved != nil || err == nil || !strings.Contains(err.Error(), "parent changed") {
			t.Fatalf("parent substitution = %v, %v", moved, err)
		}
	})

	t.Run("source substitution", func(t *testing.T) {
		t.Parallel()
		_, _, oldPath, newPath := newPaths(t)
		moved, err := moveRenameDirectoryWithHooks(oldPath, newPath, moveRenameDirectoryHooks{beforeAuthorization: func() {
			if renameErr := os.Rename(oldPath, oldPath+"-held"); renameErr != nil {
				t.Fatal(renameErr)
			}
			if mkdirErr := os.Mkdir(oldPath, 0o755); mkdirErr != nil {
				t.Fatal(mkdirErr)
			}
		}})
		if moved != nil || err == nil || !strings.Contains(err.Error(), "rename path changed") {
			t.Fatalf("source substitution = %v, %v", moved, err)
		}
	})

	t.Run("unopenable moved destination", func(t *testing.T) {
		t.Parallel()
		_, _, oldPath, newPath := newPaths(t)
		moved, err := moveRenameDirectoryWithHooks(oldPath, newPath, moveRenameDirectoryHooks{afterMove: func() {
			if chmodErr := os.Chmod(newPath, 0); chmodErr != nil {
				t.Fatal(chmodErr)
			}
		}})
		if err == nil || moved == nil {
			t.Fatalf("unopenable moved destination = %v, %v", moved, err)
		}
		_ = moved.Close()
	})

	t.Run("destination collision", func(t *testing.T) {
		t.Parallel()
		_, _, oldPath, newPath := newPaths(t)
		if err := os.Mkdir(newPath, 0o755); err != nil {
			t.Fatal(err)
		}
		moved, err := moveRenameDirectoryWithHooks(oldPath, newPath, moveRenameDirectoryHooks{})
		if moved != nil || !errors.Is(err, os.ErrExist) {
			t.Fatalf("destination collision = %v, %v", moved, err)
		}
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		_, _, oldPath, newPath := newPaths(t)
		moved, err := moveRenameDirectoryWithHooks(oldPath, newPath, moveRenameDirectoryHooks{})
		if err != nil || moved == nil {
			t.Fatalf("successful directory move = %v, %v", moved, err)
		}
		_ = moved.Close()
	})
}

func TestRenameCoverageVerifyRegistrationOutcomes(t *testing.T) {
	t.Parallel()

	initialPath := t.TempDir()
	initialExpected, err := os.Open(initialPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = initialExpected.Close() })
	oldPath := filepath.Join(t.TempDir(), "old")
	if err := verifyWorktreeRegistered(t.Context(), "", oldPath, initialPath, nil); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("nil identity error = %v", err)
	}
	if err := verifyWorktreeRegistered(t.Context(), filepath.Join(t.TempDir(), "missing"), oldPath, initialPath, initialExpected); err == nil {
		t.Fatal("missing canonical repository was accepted")
	}

	for _, tc := range []struct {
		name       string
		listedPath string
		fail       error
		want       string
	}{
		{name: "git failure", fail: errors.New("list failed"), want: "verify worktree registration"},
		{name: "old path", listedPath: oldPath, want: "still lists the old path"},
		{name: "missing new path", listedPath: "/other", want: "does not list"},
		{name: "success", listedPath: "new"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			newPath := t.TempDir()
			expected, err := os.Open(newPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = expected.Close() })
			listedPath := tc.listedPath
			if listedPath == "new" {
				listedPath = newPath
			}
			canonicalPath, _ := newRenameCoverageCanonical(t)
			ctx := withCanonicalGitInterceptor(t.Context(), func(context.Context, []string, func() ([]byte, error)) ([]byte, error) {
				output := ""
				if listedPath != "" {
					output = "worktree " + listedPath + "\n"
				}
				return []byte(output), tc.fail
			})
			err = verifyWorktreeRegistered(ctx, canonicalPath, oldPath, newPath, expected)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRenameCoverageVerifyRegistrationDetectsLateReplacement(t *testing.T) {
	t.Parallel()

	oldPath := filepath.Join(t.TempDir(), "old")
	newPath := t.TempDir()
	expected := wtLifeCovOpenDirectory(t, newPath)
	canonicalPath, _ := newRenameCoverageCanonical(t)
	ctx := withCanonicalGitInterceptor(t.Context(), func(context.Context, []string, func() ([]byte, error)) ([]byte, error) {
		if err := os.Rename(newPath, newPath+"-held"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(newPath, 0o755); err != nil {
			t.Fatal(err)
		}
		return []byte("worktree " + newPath + "\n"), nil
	})
	if err := verifyWorktreeRegistered(ctx, canonicalPath, oldPath, newPath, expected); err == nil ||
		!strings.Contains(err.Error(), "identity changed during") {
		t.Fatalf("late replacement error = %v", err)
	}
}

func TestRenameCoverageDeleteOldBranchOutcomes(t *testing.T) {
	t.Parallel()

	canonicalPath, canonical := newRenameCoverageCanonical(t)
	if deleted, reason, err := deleteOldBranchIfSafe(t.Context(), canonical, "old", "head", "old", "main", false); err != nil || deleted || !strings.Contains(reason, "unchanged") {
		t.Fatalf("unchanged branch deletion = (%t, %q, %v)", deleted, reason, err)
	}

	failedAncestor := runnertest.New(t)
	failedAncestor.Expect(func(call runnertest.Call) bool {
		return renameCoverageAncestryCall(call, canonicalPath)
	}, runner.Result{ExitCode: 2}, errors.New("ancestor failed"))
	if _, _, err := deleteOldBranchIfSafe(withGitRunner(t.Context(), failedAncestor), canonical, "old", "head", "new", "main", false); err == nil || !strings.Contains(err.Error(), "ancestor failed") {
		t.Fatalf("ancestor error = %v", err)
	}

	unmerged := runnertest.New(t)
	unmerged.Expect(func(call runnertest.Call) bool {
		return renameCoverageAncestryCall(call, canonicalPath)
	}, runner.Result{ExitCode: 1}, errors.New("not an ancestor"))
	if deleted, reason, err := deleteOldBranchIfSafe(withGitRunner(t.Context(), unmerged), canonical, "old", "head", "new", "main", false); err != nil || deleted || !strings.Contains(reason, "not merged") {
		t.Fatalf("unmerged branch = (%t, %q, %v)", deleted, reason, err)
	}

	ctx := withCanonicalGitInterceptor(t.Context(), func(context.Context, []string, func() ([]byte, error)) ([]byte, error) {
		return nil, errors.New("delete failed")
	})
	if _, _, err := deleteOldBranchIfSafe(ctx, canonical, "old", "origin/main", "new", "main", false); err == nil || !strings.Contains(err.Error(), "delete old branch") {
		t.Fatalf("delete error = %v", err)
	}

	ctx = withCanonicalGitInterceptor(t.Context(), func(context.Context, []string, func() ([]byte, error)) ([]byte, error) {
		return nil, nil
	})
	if deleted, reason, err := deleteOldBranchIfSafe(ctx, canonical, "old", "origin/main", "new", "main", false); err != nil || !deleted || reason != "" {
		t.Fatalf("merged branch deletion = (%t, %q, %v)", deleted, reason, err)
	}
}

func TestRenameCoveragePreserveCacheNormalization(t *testing.T) {
	t.Parallel()

	if paths, err := normalizePreserveCachePaths(nil); err != nil || paths != nil {
		t.Fatalf("nil cache paths = %#v, %v", paths, err)
	}
	paths, err := normalizePreserveCachePaths([]string{" node_modules ", "cache/data", "node_modules"})
	if err != nil || len(paths) != 2 || paths[0] != "cache/data" || paths[1] != "node_modules" {
		t.Fatalf("normalized cache paths = %#v, %v", paths, err)
	}
	for _, path := range []string{"", "/absolute", "double//slash", "cache/../secret"} {
		if _, err := normalizePreserveCachePaths([]string{path}); err == nil {
			t.Fatalf("unsafe cache path %q accepted", path)
		}
	}
}

func TestRenameCoverageSharedPhysicalDestination(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	plan := &renamePlan{
		destinationRoot: root,
		result:          RenameResult{NewWorktreeDir: filepath.Join(root, "new-task", "github.com", "acme", "app")},
	}
	if err := preflightRenamePhysicalDestination(t.Context(), "new-task", plan); err != nil {
		t.Fatalf("preflight shared destination: %v", err)
	}
	if err := prepareRenamePhysicalDestination(t.Context(), "new-task", plan); err != nil {
		t.Fatalf("prepare shared destination: %v", err)
	}
	if err := os.Mkdir(plan.result.NewWorktreeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareRenamePhysicalDestination(t.Context(), "new-task", plan); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("occupied shared destination error = %v", err)
	}

	blockedRoot := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blockedRoot, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := &renamePlan{destinationRoot: blockedRoot, result: RenameResult{NewWorktreeDir: filepath.Join(blockedRoot, "new-task", "app")}}
	if err := preflightRenamePhysicalDestination(t.Context(), "new-task", blocked); err == nil || !strings.Contains(err.Error(), "open shared rename destination root") {
		t.Fatalf("blocked shared preflight error = %v", err)
	}
}

func renameCoverageAncestryCall(call runnertest.Call, canonicalPath string) bool {
	return call.Op == "RunOpts" && call.Dir == "" && call.Name == "git" &&
		len(call.Args) >= 3 && call.Args[0] == "-C" && call.Args[1] == canonicalPath && call.Args[2] == "merge-base"
}

func newRenameCoverageCanonical(t *testing.T) (string, *canonicalRepository) {
	t.Helper()
	canonicalPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(canonicalPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	canonical, err := openCanonicalRepository(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(canonical.close)
	return canonicalPath, canonical
}

func newRenameCoverageMovePaths(t *testing.T) (oldPath, newPath, root string) {
	t.Helper()
	root = t.TempDir()
	oldParent := filepath.Join(root, "old-parent")
	newParent := filepath.Join(root, "new-parent")
	if err := os.MkdirAll(filepath.Join(oldParent, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(newParent, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(oldParent, "old"), filepath.Join(newParent, "new"), newParent
}
