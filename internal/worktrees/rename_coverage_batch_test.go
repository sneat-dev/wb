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
	defer large.Close()
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
	defer writeOnly.Close()
	if _, err := linkedWorktreeGitFileAdminName(canonical, writeOnly); err == nil || !strings.Contains(err.Error(), "read linked worktree") {
		t.Fatalf("write-only gitfile error = %v", err)
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
	defer parent.Close()
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
	defer open.Close()
	if err := retainDescriptorsAcrossGitExec(open); err != nil {
		t.Fatalf("retain open descriptor: %v", err)
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

func TestRenameCoverageVerifyRegistrationOutcomes(t *testing.T) {
	t.Parallel()

	initialPath := t.TempDir()
	initialExpected, err := os.Open(initialPath)
	if err != nil {
		t.Fatal(err)
	}
	defer initialExpected.Close()
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
			defer expected.Close()
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

func TestRenameCoverageDeleteOldBranchOutcomes(t *testing.T) {
	t.Parallel()

	canonicalPath, canonical := newRenameCoverageCanonical(t)

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
