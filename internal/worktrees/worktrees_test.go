package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == SecureCleanupGitHelperArgument {
		os.Exit(RunSecureCleanupGitHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == SecureStageGitHelperArgument {
		os.Exit(RunSecureStageGitHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == SecureCanonicalGitHelperArgument {
		os.Exit(RunSecureCanonicalGitHelper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == SecureStageCanonicalGitHelperArgument {
		os.Exit(RunSecureStageCanonicalGitHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestCreateSynchronizesCanonicalAndCreatesCentralWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	canonicalHeadBefore := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	fixture.pushRemoteCommit(t, "remote change")
	remoteHeadFields := strings.Fields(gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main"))
	if len(remoteHeadFields) != 2 {
		t.Fatalf("unexpected origin/main response: %q", remoteHeadFields)
	}
	remoteHead := remoteHeadFields[0]

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
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != canonicalHeadBefore {
		t.Fatalf("canonical HEAD changed from %s to %s", canonicalHeadBefore, got)
	}
	if got := gitTestOutput(t, result.WorktreeDir, "branch", "--show-current"); got != "codex/issue-123" {
		t.Fatalf("worktree branch = %q", got)
	}
	if got := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD"); got != remoteHead {
		t.Fatalf("new worktree head = %s, want fetched origin/main %s", got, remoteHead)
	}

	guarded, err := Guard(context.Background(), result.WorktreeDir, GuardOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatal(err)
	}
	if guarded.Kind != "linked" || guarded.CanonicalDir != fixture.canonical {
		t.Fatalf("guard result = %#v", guarded)
	}
}

// TestCreateFetchesOriginBaseWithoutChangingCanonicalCheckout proves the
// regression fixed after WB refused creation just because a clean canonical
// clone was temporarily on a feature branch. The protected local main branch
// is deliberately checked out in another linked worktree and left stale: WB
// must create from the freshly fetched origin/main commit without switching or
// advancing either existing checkout.
func TestCreateFetchesOriginBaseWithoutChangingCanonicalCheckout(t *testing.T) {
	fixture := newGitFixture(t)
	canonicalFeature := "feat/canonical-in-use"
	gitTest(t, fixture.canonical, "checkout", "-b", canonicalFeature)
	canonicalHeadBefore := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	baseHolder := filepath.Join(filepath.Dir(fixture.projectsRoot), "main-holder")
	gitTest(t, fixture.canonical, "worktree", "add", baseHolder, "main")
	t.Cleanup(func() { _ = os.RemoveAll(baseHolder) })
	mainHeadBefore := gitTestOutput(t, baseHolder, "rev-parse", "HEAD")
	fixture.pushRemoteCommit(t, "remote main after canonical feature")
	remoteHead := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main")
	remoteHead = strings.Fields(remoteHead)[0]

	results, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "from-fetched-main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v", results)
	}
	if got := gitTestOutput(t, fixture.canonical, "branch", "--show-current"); got != canonicalFeature {
		t.Fatalf("canonical branch = %q, want %q", got, canonicalFeature)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != canonicalHeadBefore {
		t.Fatalf("canonical HEAD changed from %s to %s", canonicalHeadBefore, got)
	}
	if got := gitTestOutput(t, baseHolder, "branch", "--show-current"); got != "main" {
		t.Fatalf("base-holder branch = %q, want main", got)
	}
	if got := gitTestOutput(t, baseHolder, "rev-parse", "HEAD"); got != mainHeadBefore {
		t.Fatalf("local main changed from %s to %s", mainHeadBefore, got)
	}
	if got := gitTestOutput(t, results[0].WorktreeDir, "rev-parse", "HEAD"); got != remoteHead {
		t.Fatalf("new worktree HEAD = %s, want fetched origin/main %s", got, remoteHead)
	}
}

func TestCreateFailsBeforeMutationWhenRequestedOriginBaseCannotBeFetched(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "feat/canonical-in-use")
	canonicalHeadBefore := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")

	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "missing-remote-base",
		Base:         "release",
	})
	if err == nil || !strings.Contains(err.Error(), "fetch verified origin base acme/app/release") {
		t.Fatalf("Create missing remote base error = %v", err)
	}
	if got := gitTestOutput(t, fixture.canonical, "branch", "--show-current"); got != "feat/canonical-in-use" {
		t.Fatalf("canonical branch changed to %q", got)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != canonicalHeadBefore {
		t.Fatalf("canonical HEAD changed from %s to %s", canonicalHeadBefore, got)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, "codex/missing-remote-base"); branchErr != nil || exists {
		t.Fatalf("missing remote base left feature branch: exists=%t err=%v", exists, branchErr)
	}
}

func TestCreateDoesNotFollowSubstitutedWBHomeBeforeInitialOpen(t *testing.T) {
	fixture := newGitFixture(t)
	external := t.TempDir()
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "home-before-open-swap",
		beforeHomeDirectoryOpen: func() {
			if symlinkErr := os.Symlink(external, fixture.home); symlinkErr != nil {
				t.Fatalf("substitute WB home before initial open: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("substituted WB home Create error = %v", err)
	}
	if entries, readErr := os.ReadDir(external); readErr != nil || len(entries) != 0 {
		t.Fatalf("substituted WB home mutated external target: entries=%v err=%v", entries, readErr)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, "codex/home-before-open-swap"); branchErr != nil || exists {
		t.Fatalf("substituted WB home left feature branch: exists=%t err=%v", exists, branchErr)
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

func TestCreateDoesNotFollowWorktreesAncestorSwapDuringSecureAdd(t *testing.T) {
	fixture := newGitFixture(t)
	outside := t.TempDir()
	worktreesPath := filepath.Join(fixture.home, "worktrees")
	movedWorktrees := worktreesPath + "-moved"
	operation := "worktrees-ancestor-swap"
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		beforeSecureWorktreeAdd: func() {
			if renameErr := os.Rename(worktreesPath, movedWorktrees); renameErr != nil {
				t.Fatalf("move worktrees ancestor during secure-add regression: %v", renameErr)
			}
			if symlinkErr := os.Symlink(outside, worktreesPath); symlinkErr != nil {
				t.Fatalf("substitute worktrees ancestor during secure-add regression: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "secure staging directory path changed") {
		t.Fatalf("worktrees-ancestor-swap Create error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, operation, "acme", "app")); !os.IsNotExist(statErr) {
		t.Fatalf("worktrees ancestor swap created external checkout: %v", statErr)
	}
	registered := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if strings.Contains(registered, outside) || strings.Contains(registered, movedWorktrees) {
		t.Fatalf("worktrees ancestor swap left registration:\n%s", registered)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, "codex/"+operation); branchErr != nil || exists {
		t.Fatalf("worktrees ancestor swap left feature branch: exists=%t err=%v", exists, branchErr)
	}
}

func TestCreateDoesNotFollowWorktreesAncestorSwapBeforePlanning(t *testing.T) {
	fixture := newGitFixture(t)
	external := t.TempDir()
	worktreesPath := filepath.Join(fixture.home, "worktrees")
	movedWorktrees := worktreesPath + "-moved"
	operation := "worktrees-ancestor-plan-swap"
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		afterOperationRootPrepared: func() {
			if renameErr := os.Rename(worktreesPath, movedWorktrees); renameErr != nil {
				t.Fatalf("move worktrees ancestor before planning regression: %v", renameErr)
			}
			if symlinkErr := os.Symlink(external, worktreesPath); symlinkErr != nil {
				t.Fatalf("substitute worktrees ancestor before planning regression: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "operation path changed before planning") {
		t.Fatalf("worktrees-ancestor-plan-swap Create error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(external, operation)); !os.IsNotExist(statErr) {
		t.Fatalf("worktrees ancestor swap created an external operation path: %v", statErr)
	}
	registered := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if strings.Contains(registered, external) || strings.Contains(registered, movedWorktrees) {
		t.Fatalf("worktrees ancestor plan swap left registration:\n%s", registered)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, "codex/"+operation); branchErr != nil || exists {
		t.Fatalf("worktrees ancestor plan swap left feature branch: exists=%t err=%v", exists, branchErr)
	}
}

func TestCreateRejectsOwnerSwapAfterWorktreeRepair(t *testing.T) {
	fixture := newGitFixture(t)
	outside := t.TempDir()
	operation := "owner-swap-after-repair"
	ownerPath := filepath.Join(fixture.home, "worktrees", operation, "acme")
	movedOwner := ownerPath + "-moved"
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		afterWorktreeRepair: func() {
			if renameErr := os.Rename(ownerPath, movedOwner); renameErr != nil {
				t.Fatalf("move owner after repair: %v", renameErr)
			}
			if symlinkErr := os.Symlink(outside, ownerPath); symlinkErr != nil {
				t.Fatalf("substitute owner after repair: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "owner path changed during repair") {
		t.Fatalf("owner-swap-after-repair Create error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "app")); !os.IsNotExist(statErr) {
		t.Fatalf("owner swap after repair created external checkout: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(movedOwner, "app")); !os.IsNotExist(statErr) {
		t.Fatalf("owner swap after repair left moved checkout: %v", statErr)
	}
	registered := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if strings.Contains(registered, outside) || strings.Contains(registered, movedOwner) {
		t.Fatalf("owner swap after repair left registration:\n%s", registered)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, "codex/"+operation); branchErr != nil || exists {
		t.Fatalf("owner swap after repair left feature branch: exists=%t err=%v", exists, branchErr)
	}
}

func TestCreateRejectsStageRootSwapWithoutLeakingCheckoutOrBranch(t *testing.T) {
	fixture := newGitFixture(t)
	outside := t.TempDir()
	operation := "stage-swap"
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	stageRoot := ""
	parkedStage := ""
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		beforeSecureWorktreeAdd: func() {
			entries, readErr := os.ReadDir(operationRoot)
			if readErr != nil {
				t.Fatalf("read staging parent: %v", readErr)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".wb-stage-") {
					stageRoot = filepath.Join(operationRoot, entry.Name())
					break
				}
			}
			if stageRoot == "" {
				t.Fatal("secure staging directory was not created")
			}
			parkedStage = filepath.Join(operationRoot, "parked-trusted-stage")
			if renameErr := os.Rename(stageRoot, parkedStage); renameErr != nil {
				t.Fatalf("park secure staging directory: %v", renameErr)
			}
			if symlinkErr := os.Symlink(outside, stageRoot); symlinkErr != nil {
				t.Fatalf("substitute secure staging directory: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "secure staging directory path changed") {
		t.Fatalf("stage-swap Create error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "checkout")); !os.IsNotExist(statErr) {
		t.Fatalf("stage swap created external checkout: %v", statErr)
	}
	assertFailedCreateRolledBack(t, fixture, operation)
	if info, statErr := os.Lstat(stageRoot); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("substituted stage path was not preserved as a symlink: info=%v err=%v", info, statErr)
	}
	if _, statErr := os.Lstat(parkedStage); !os.IsNotExist(statErr) {
		t.Fatalf("parked trusted stage remains: %v", statErr)
	}
}

func TestCreateCleansStageWhenOpeningItFails(t *testing.T) {
	fixture := newGitFixture(t)
	operation := "stage-open-failure"
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	parkedStage := filepath.Join(operationRoot, "parked-stage-before-open")
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		afterSecureStageDirectoryCreated: func() {
			entries, readErr := os.ReadDir(operationRoot)
			if readErr != nil {
				t.Fatalf("read newly-created stage: %v", readErr)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".wb-stage-") {
					stagePath := filepath.Join(operationRoot, entry.Name())
					if renameErr := os.Rename(stagePath, parkedStage); renameErr != nil {
						t.Fatalf("park newly-created stage before open: %v", renameErr)
					}
					if writeErr := os.WriteFile(stagePath, []byte("not a directory\n"), 0o600); writeErr != nil {
						t.Fatalf("replace stage with regular file: %v", writeErr)
					}
					return
				}
			}
			t.Fatal("secure staging directory was not created")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "open secure worktree staging directory") {
		t.Fatalf("stage-open failure Create error = %v", err)
	}
	entries, readErr := os.ReadDir(operationRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	replacementFound := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".wb-stage-") {
			replacementFound = true
			if entry.IsDir() {
				t.Fatalf("replacement stage entry is unexpectedly a directory: %s", entry.Name())
			}
			content, readErr := os.ReadFile(filepath.Join(operationRoot, entry.Name()))
			if readErr != nil {
				t.Fatalf("read replacement stage: %v", readErr)
			}
			if string(content) != "not a directory\n" {
				t.Fatalf("replacement stage content = %q", content)
			}
		}
	}
	if !replacementFound {
		t.Fatal("substituted stage entry was removed")
	}
	if _, statErr := os.Lstat(parkedStage); !os.IsNotExist(statErr) {
		t.Fatalf("parked stage artifact remains after Openat failure: %v", statErr)
	}
}

func TestCreateRefusesLateSecureDestinationWithoutClobberingIt(t *testing.T) {
	fixture := newGitFixture(t)
	operation := "late-destination"
	destination := filepath.Join(fixture.home, "worktrees", operation, "acme", "app")
	foreign := "foreign destination\n"
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		afterSecureDestinationValidation: func() {
			if mkdirErr := os.Mkdir(destination, 0o755); mkdirErr != nil {
				t.Fatalf("create late destination: %v", mkdirErr)
			}
			if writeErr := os.WriteFile(filepath.Join(destination, "keep.txt"), []byte(foreign), 0o600); writeErr != nil {
				t.Fatalf("write late destination sentinel: %v", writeErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "publish secure worktree") {
		t.Fatalf("late destination Create error = %v", err)
	}
	content, readErr := os.ReadFile(filepath.Join(destination, "keep.txt"))
	if readErr != nil {
		t.Fatalf("read late destination sentinel: %v", readErr)
	}
	if string(content) != foreign {
		t.Fatalf("late destination was clobbered: %q", content)
	}
	registered := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if strings.Contains(registered, destination) {
		t.Fatalf("late destination left worktree registration:\n%s", registered)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, "codex/"+operation); branchErr != nil || exists {
		t.Fatalf("late destination left feature branch: exists=%t err=%v", exists, branchErr)
	}
}

func TestCreateRefusesLateStagedCheckoutSubstitutionWithoutPublishingIt(t *testing.T) {
	fixture := newGitFixture(t)
	operation := "late-checkout-substitution"
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	parkedCheckout := filepath.Join(t.TempDir(), "parked-checkout")
	foreign := "foreign staged checkout\n"
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		afterSecureCheckoutAuthorization: func() {
			checkout := filepath.Join(testStageRoot(t, operationRoot), "checkout")
			if renameErr := os.Rename(checkout, parkedCheckout); renameErr != nil {
				t.Fatalf("park authorized checkout: %v", renameErr)
			}
			if mkdirErr := os.Mkdir(checkout, 0o755); mkdirErr != nil {
				t.Fatalf("replace checkout directory: %v", mkdirErr)
			}
			if writeErr := os.WriteFile(filepath.Join(checkout, "keep.txt"), []byte(foreign), 0o600); writeErr != nil {
				t.Fatalf("write replacement checkout sentinel: %v", writeErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "publish secure worktree") {
		t.Fatalf("late checkout substitution error = %v", err)
	}
	checkout := filepath.Join(testStageRoot(t, operationRoot), "checkout")
	content, readErr := os.ReadFile(filepath.Join(checkout, "keep.txt"))
	if readErr != nil || string(content) != foreign {
		t.Fatalf("late staged checkout replacement was not preserved: content=%q err=%v", content, readErr)
	}
	if _, statErr := os.Stat(parkedCheckout); statErr != nil {
		t.Fatalf("original checkout was deleted after substitution: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(operationRoot, "acme", "app")); !os.IsNotExist(statErr) {
		t.Fatalf("late staged checkout was published: %v", statErr)
	}
}

func TestCreateRefusesLatePublishedWorktreeSubstitutionBeforeRepair(t *testing.T) {
	fixture := newGitFixture(t)
	operation := "late-published-substitution"
	finalPath := filepath.Join(fixture.home, "worktrees", operation, "acme", "app")
	parkedWorktree := filepath.Join(t.TempDir(), "parked-worktree")
	foreign := "foreign published worktree\n"
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		afterPublishedWorktreeAuthorization: func() {
			if renameErr := os.Rename(finalPath, parkedWorktree); renameErr != nil {
				t.Fatalf("park authorized final worktree: %v", renameErr)
			}
			if mkdirErr := os.Mkdir(finalPath, 0o755); mkdirErr != nil {
				t.Fatalf("replace final worktree: %v", mkdirErr)
			}
			if writeErr := os.WriteFile(filepath.Join(finalPath, "keep.txt"), []byte(foreign), 0o600); writeErr != nil {
				t.Fatalf("write replacement final sentinel: %v", writeErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "repair published worktree metadata") {
		t.Fatalf("late published worktree substitution error = %v", err)
	}
	content, readErr := os.ReadFile(filepath.Join(finalPath, "keep.txt"))
	if readErr != nil || string(content) != foreign {
		t.Fatalf("late final worktree replacement was not preserved: content=%q err=%v", content, readErr)
	}
	if _, statErr := os.Stat(parkedWorktree); statErr != nil {
		t.Fatalf("original final worktree was deleted after substitution: %v", statErr)
	}
}

func TestOperationLockReleasePreservesLateReplacement(t *testing.T) {
	directoryPath := t.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(directoryPath); resolveErr == nil {
		directoryPath = resolved
	}
	directory, err := openAbsoluteDirectoryNoFollow(directoryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	lock, err := acquireLockAt(directory)
	if err != nil {
		t.Fatal(err)
	}
	replacement := "successor lock\n"
	lock.beforeRelease = func() {
		temporary, createErr := os.CreateTemp(directoryPath, ".lock-replacement-*")
		if createErr != nil {
			t.Fatalf("create replacement lock: %v", createErr)
		}
		temporaryPath := temporary.Name()
		if _, writeErr := temporary.WriteString(replacement); writeErr != nil {
			_ = temporary.Close()
			t.Fatalf("write replacement lock: %v", writeErr)
		}
		if closeErr := temporary.Close(); closeErr != nil {
			t.Fatalf("close replacement lock: %v", closeErr)
		}
		if renameErr := os.Rename(temporaryPath, filepath.Join(directoryPath, ".lock")); renameErr != nil {
			t.Fatalf("replace operation lock after authorization: %v", renameErr)
		}
	}
	lock.release()
	content, readErr := os.ReadFile(filepath.Join(directoryPath, ".lock"))
	if readErr != nil || string(content) != replacement {
		t.Fatalf("late lock replacement was removed: content=%q err=%v", content, readErr)
	}
	if _, acquireErr := acquireLockAt(directory); acquireErr == nil {
		t.Fatal("late lock replacement no longer blocks a second operation")
	}
}

func TestSecureStageReusesEmptyRetirementWithoutDeletingIt(t *testing.T) {
	operationRoot := t.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(operationRoot); resolveErr == nil {
		operationRoot = resolved
	}
	directory, err := openAbsoluteDirectoryNoFollow(operationRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	retiredName := ".wb-retired-stage-empty"
	if err := os.Mkdir(filepath.Join(operationRoot, retiredName), 0o700); err != nil {
		t.Fatal(err)
	}
	name, err := makeSecureStageDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, ".wb-stage-") {
		t.Fatalf("claimed stage name = %q", name)
	}
	if _, statErr := os.Stat(filepath.Join(operationRoot, retiredName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("retired name should be atomically claimed, stat err = %v", statErr)
	}
	if info, statErr := os.Stat(filepath.Join(operationRoot, name)); statErr != nil || !info.IsDir() {
		t.Fatalf("claimed stage is unavailable: info=%v err=%v", info, statErr)
	}
}

func TestSecureStagePoolSkipsReplacementAndDoesNotCapExhaustedEntries(t *testing.T) {
	operationRoot := t.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(operationRoot); resolveErr == nil {
		operationRoot = resolved
	}
	directory, err := openAbsoluteDirectoryNoFollow(operationRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	for index := 0; index < 300; index++ {
		name := fmt.Sprintf(".wb-retired-stage-busy-%03d", index)
		if err := os.Mkdir(filepath.Join(operationRoot, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(operationRoot, name, "keep"), []byte("do not reap\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	replacement := filepath.Join(operationRoot, ".wb-retired-stage-replacement")
	if err := os.WriteFile(replacement, []byte("successor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := makeSecureStageDirectory(directory)
	if err != nil {
		t.Fatalf("exhausted retired pool must create a fresh stage, got %v", err)
	}
	if content, readErr := os.ReadFile(replacement); readErr != nil || string(content) != "successor\n" {
		t.Fatalf("retirement-name replacement was changed: content=%q err=%v", content, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(operationRoot, ".wb-retired-stage-busy-299", "keep")); statErr != nil {
		t.Fatalf("busy retirement was reaped: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(operationRoot, name)); statErr != nil {
		t.Fatalf("fresh secure stage missing: %v", statErr)
	}
}

func TestOperationLockReusesRetirementWithoutAccumulating(t *testing.T) {
	directoryPath := t.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(directoryPath); resolveErr == nil {
		directoryPath = resolved
	}
	directory, err := openAbsoluteDirectoryNoFollow(directoryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	first, err := acquireLockAt(directory)
	if err != nil {
		t.Fatal(err)
	}
	first.release()
	second, err := acquireLockAt(directory)
	if err != nil {
		t.Fatal(err)
	}
	second.release()
	entries, err := os.ReadDir(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	retired := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".wb-retired-lock-") {
			retired++
		}
	}
	if retired != 1 {
		t.Fatalf("retired locks = %d, want one reusable entry", retired)
	}
}

func TestOperationLockClaimNeverMutatesHardLinkedRetirement(t *testing.T) {
	directoryPath := t.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(directoryPath); resolveErr == nil {
		directoryPath = resolved
	}
	directory, err := openAbsoluteDirectoryNoFollow(directoryPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	external := filepath.Join(t.TempDir(), "important.txt")
	const wanted = "do not overwrite this file\n"
	if err := os.WriteFile(external, []byte(wanted), 0o600); err != nil {
		t.Fatal(err)
	}
	retired := filepath.Join(directoryPath, ".wb-retired-lock-hardlink")
	if err := os.Link(external, retired); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLockAt(directory)
	if err != nil {
		t.Fatal(err)
	}
	if content, readErr := os.ReadFile(external); readErr != nil || string(content) != wanted {
		lock.release()
		t.Fatalf("claim mutated hard-linked external file: content=%q err=%v", content, readErr)
	}
	lock.release()
	if _, statErr := os.Lstat(retired); statErr != nil {
		t.Fatalf("hard-linked retirement was claimed or removed: %v", statErr)
	}
	if content, readErr := os.ReadFile(external); readErr != nil || string(content) != wanted {
		t.Fatalf("release mutated hard-linked external file: content=%q err=%v", content, readErr)
	}
}

func TestGitEnvironmentUsesOnlyScopedGitAndTemporaryDirectories(t *testing.T) {
	t.Setenv("GIT_DIR", "/ambient/git")
	t.Setenv("GIT_WORK_TREE", "/ambient/worktree")
	t.Setenv("TMPDIR", "/ambient/tmp")
	environment := gitEnvironmentWithHeldGitDir("/retained/tmp")
	values := map[string]string{}
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		_, scoped := map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true, "TMPDIR": true}[key]
		if _, duplicate := values[key]; scoped && duplicate {
			t.Fatalf("scoped environment has duplicate %s entries: %#v", key, environment)
		}
		values[key] = value
	}
	if values["GIT_DIR"] != "." || values["GIT_WORK_TREE"] != ".." || values["TMPDIR"] != "/retained/tmp" {
		t.Fatalf("scoped Git environment = %#v", values)
	}
}

func TestCleanupGitEnvironmentPinsCanonicalWorkTreeForHooks(t *testing.T) {
	environment := gitEnvironmentWithHeldGitDirAndWorkTree("/retained/git", "/retained/repository")
	values := map[string]string{}
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if found {
			values[key] = value
		}
	}
	if values["GIT_DIR"] != "." || values["GIT_WORK_TREE"] != "/retained/repository" || values["TMPDIR"] != "/retained/git" {
		t.Fatalf("cleanup Git environment = %#v", values)
	}
}

func TestCreatePreservesDoubleSwapAcrossSecureCheckoutPublish(t *testing.T) {
	fixture := newGitFixture(t)
	operation := "checkout-double-swap"
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	stageRoot := ""
	parkedOriginal := filepath.Join(t.TempDir(), "parked-original-checkout")
	firstForeign := "first foreign checkout\n"
	secondForeign := "second foreign checkout\n"
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		afterSecureCheckoutAuthorization: func() {
			entries, readErr := os.ReadDir(operationRoot)
			if readErr != nil {
				t.Fatalf("read operation root: %v", readErr)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".wb-stage-") {
					stageRoot = filepath.Join(operationRoot, entry.Name())
					break
				}
			}
			if stageRoot == "" {
				t.Fatal("secure stage was not found")
			}
			checkout := filepath.Join(stageRoot, "checkout")
			if renameErr := os.Rename(checkout, parkedOriginal); renameErr != nil {
				t.Fatalf("park original checkout: %v", renameErr)
			}
			if mkdirErr := os.Mkdir(checkout, 0o755); mkdirErr != nil {
				t.Fatalf("create first replacement: %v", mkdirErr)
			}
			if writeErr := os.WriteFile(filepath.Join(checkout, "keep.txt"), []byte(firstForeign), 0o600); writeErr != nil {
				t.Fatalf("write first replacement: %v", writeErr)
			}
		},
		afterSecureCheckoutMove: func() {
			checkout := filepath.Join(stageRoot, "checkout")
			if mkdirErr := os.Mkdir(checkout, 0o755); mkdirErr != nil {
				t.Fatalf("create second replacement: %v", mkdirErr)
			}
			if writeErr := os.WriteFile(filepath.Join(checkout, "keep.txt"), []byte(secondForeign), 0o600); writeErr != nil {
				t.Fatalf("write second replacement: %v", writeErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "ambiguous replacement state") {
		t.Fatalf("double-swap creation error = %v", err)
	}
	final := filepath.Join(operationRoot, "acme", "app")
	if content, readErr := os.ReadFile(filepath.Join(final, "keep.txt")); readErr != nil || string(content) != firstForeign {
		t.Fatalf("first replacement was not preserved at destination: content=%q err=%v", content, readErr)
	}
	if content, readErr := os.ReadFile(filepath.Join(stageRoot, "checkout", "keep.txt")); readErr != nil || string(content) != secondForeign {
		t.Fatalf("second replacement was not preserved at source: content=%q err=%v", content, readErr)
	}
	if _, statErr := os.Stat(parkedOriginal); statErr != nil {
		t.Fatalf("original checkout was retired after ambiguous publish: %v", statErr)
	}
}

func TestCreateChildRefusesCanonicalGitDirectorySwapAfterAuthorization(t *testing.T) {
	fixture := newGitFixture(t)
	parkedGit := fixture.canonical + ".git-parked"
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "canonical-git-swap",
		afterCanonicalGitAuthorization: func() {
			if renameErr := os.Rename(filepath.Join(fixture.canonical, ".git"), parkedGit); renameErr != nil {
				t.Fatalf("park canonical Git directory after authorization: %v", renameErr)
			}
			if mkdirErr := os.Mkdir(filepath.Join(fixture.canonical, ".git"), 0o755); mkdirErr != nil {
				t.Fatalf("replace canonical Git directory: %v", mkdirErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "canonical Git directory changed before Git operation") {
		t.Fatalf("canonical Git swap error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.canonical, ".git", "HEAD")); !os.IsNotExist(statErr) {
		t.Fatalf("replacement canonical Git directory was mutated: %v", statErr)
	}
	if _, statErr := os.Stat(parkedGit); statErr != nil {
		t.Fatalf("original canonical Git directory was removed: %v", statErr)
	}
}

func TestCreateUsesHeldCanonicalRootAfterAuthorizationSwap(t *testing.T) {
	fixture := newGitFixture(t)
	movedCanonical := fixture.canonical + "-parked"
	external := t.TempDir()
	called := false
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "canonical-root-swap",
		afterCanonicalGitAuthorization: func() {
			if called {
				return
			}
			called = true
			if renameErr := os.Rename(fixture.canonical, movedCanonical); renameErr != nil {
				t.Fatalf("park canonical root after authorization: %v", renameErr)
			}
			if symlinkErr := os.Symlink(external, fixture.canonical); symlinkErr != nil {
				t.Fatalf("replace canonical root after authorization: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "canonical repository path changed before Git operation") {
		t.Fatalf("canonical root swap error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(external, ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("replacement canonical root was mutated: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(movedCanonical, ".git")); statErr != nil {
		t.Fatalf("held canonical root was removed: %v", statErr)
	}
}

func TestCreateDoesNotFollowStageRootSwapAfterValidation(t *testing.T) {
	fixture := newGitFixture(t)
	outside := t.TempDir()
	operation := "stage-swap-after-validation"
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	stageRoot := ""
	parkedStage := ""
	results, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		afterSecureStageValidation: func() {
			entries, readErr := os.ReadDir(operationRoot)
			if readErr != nil {
				t.Fatalf("read staging parent: %v", readErr)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".wb-stage-") {
					stageRoot = filepath.Join(operationRoot, entry.Name())
					break
				}
			}
			if stageRoot == "" {
				t.Fatal("secure staging directory was not created")
			}
			parkedStage = filepath.Join(operationRoot, "parked-trusted-stage")
			if renameErr := os.Rename(stageRoot, parkedStage); renameErr != nil {
				t.Fatalf("park validated staging directory: %v", renameErr)
			}
			if symlinkErr := os.Symlink(outside, stageRoot); symlinkErr != nil {
				t.Fatalf("substitute validated staging directory: %v", symlinkErr)
			}
		},
	})
	if err != nil {
		t.Fatalf("Create after validated stage swap: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Create results = %#v", results)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "checkout")); !os.IsNotExist(statErr) {
		t.Fatalf("post-validation stage swap created external checkout: %v", statErr)
	}
	if info, statErr := os.Lstat(stageRoot); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("substituted stage path was not preserved as a symlink: info=%v err=%v", info, statErr)
	}
	if _, statErr := os.Lstat(parkedStage); !os.IsNotExist(statErr) {
		t.Fatalf("parked trusted stage remains: %v", statErr)
	}
	registered := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if strings.Contains(registered, outside) || strings.Contains(registered, parkedStage) {
		t.Fatalf("post-validation stage swap left external or parked registration:\n%s", registered)
	}
	if !strings.Contains(registered, results[0].WorktreeDir) {
		t.Fatalf("post-validation stage swap did not register published worktree %s:\n%s", results[0].WorktreeDir, registered)
	}
	if got := gitTestOutput(t, results[0].WorktreeDir, "branch", "--show-current"); got != "codex/"+operation {
		t.Fatalf("post-validation stage swap branch = %q", got)
	}
}

func TestCreateRejectsStageRootMovedOutsideOperationAfterValidation(t *testing.T) {
	fixture := newGitFixture(t)
	outside := t.TempDir()
	operation := "stage-outside-after-validation"
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	stageRoot := ""
	parkedStage := filepath.Join(outside, "parked-trusted-stage")
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		afterSecureStageValidation: func() {
			stageRoot = testStageRoot(t, operationRoot)
			if renameErr := os.Rename(stageRoot, parkedStage); renameErr != nil {
				t.Fatalf("move validated staging directory outside operation: %v", renameErr)
			}
			if symlinkErr := os.Symlink(outside, stageRoot); symlinkErr != nil {
				t.Fatalf("substitute validated staging directory: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "outside trusted operation root") {
		t.Fatalf("external stage move Create error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(parkedStage, "checkout")); !os.IsNotExist(statErr) {
		t.Fatalf("external stage move created checkout outside WB home: %v", statErr)
	}
	assertFailedCreateRolledBack(t, fixture, operation)
	if info, statErr := os.Lstat(stageRoot); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("substituted stage path was not preserved as a symlink: info=%v err=%v", info, statErr)
	}
	registered := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if strings.Contains(registered, outside) {
		t.Fatalf("external stage move left registration:\n%s", registered)
	}
}

func TestCreatePublishesAfterExternalStageMoveFollowingFinalVerification(t *testing.T) {
	fixture := newGitFixture(t)
	outside := t.TempDir()
	operation := "stage-outside-publish"
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	stageRoot := ""
	parkedStage := filepath.Join(outside, "parked-trusted-stage")
	results, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		afterSecureStageVerification: func() {
			stageRoot = testStageRoot(t, operationRoot)
			if renameErr := os.Rename(stageRoot, parkedStage); renameErr != nil {
				t.Fatalf("move verified staging directory outside operation: %v", renameErr)
			}
			if symlinkErr := os.Symlink(outside, stageRoot); symlinkErr != nil {
				t.Fatalf("substitute verified staging directory: %v", symlinkErr)
			}
		},
	})
	if err != nil {
		t.Fatalf("Create after final external stage move: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Create results = %#v", results)
	}
	if _, statErr := os.Stat(filepath.Join(parkedStage, "checkout")); !os.IsNotExist(statErr) {
		t.Fatalf("external stage move left checkout outside WB home: %v", statErr)
	}
	if info, statErr := os.Lstat(stageRoot); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("substituted stage path was not preserved as a symlink: info=%v err=%v", info, statErr)
	}
	registered := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if strings.Contains(registered, outside) || !strings.Contains(registered, results[0].WorktreeDir) {
		t.Fatalf("external stage move registration =\n%s", registered)
	}
}

func TestCreateRollsBackExternalStageAfterPublishedRepairFailure(t *testing.T) {
	fixture := newGitFixture(t)
	outside := t.TempDir()
	operation := "stage-outside-repair-failure"
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	stageRoot := ""
	parkedStage := filepath.Join(outside, "parked-trusted-stage")
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		afterSecureStageVerification: func() {
			stageRoot = testStageRoot(t, operationRoot)
			if renameErr := os.Rename(stageRoot, parkedStage); renameErr != nil {
				t.Fatalf("move verified staging directory outside operation: %v", renameErr)
			}
			if symlinkErr := os.Symlink(outside, stageRoot); symlinkErr != nil {
				t.Fatalf("substitute verified staging directory: %v", symlinkErr)
			}
		},
		beforeWorktreeRepair: func() error {
			return errors.New("simulated repair failure after external stage move")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "simulated repair failure") {
		t.Fatalf("external stage repair failure Create error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(parkedStage, "checkout")); !os.IsNotExist(statErr) {
		t.Fatalf("external stage repair failure left checkout outside WB home: %v", statErr)
	}
	assertFailedCreateRolledBack(t, fixture, operation)
	if info, statErr := os.Lstat(stageRoot); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("substituted stage path was not preserved as a symlink: info=%v err=%v", info, statErr)
	}
	registered := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	if strings.Contains(registered, outside) {
		t.Fatalf("external stage repair failure left registration:\n%s", registered)
	}
}

func TestCreateResolvesGitBeforeEnteringStageDirectory(t *testing.T) {
	fixture := newGitFixture(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory := t.TempDir()
	trustedGit := filepath.Join(workingDirectory, "git")
	if err := os.WriteFile(trustedGit, []byte("#!/bin/sh\nexec \"$WB_TEST_REAL_GIT\" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workingDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	t.Setenv("PATH", ".")
	t.Setenv("GODEBUG", "execerrdot=0")
	t.Setenv("WB_TEST_REAL_GIT", realGit)
	operation := "stage-path-git"
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	fakeWasRun := filepath.Join(workingDirectory, "stage-git-ran")
	results, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		afterSecureStageValidation: func() {
			fakeGit := filepath.Join(testStageRoot(t, operationRoot), "git")
			contents := "#!/bin/sh\ntouch \"" + fakeWasRun + "\"\nexit 99\n"
			if writeErr := os.WriteFile(fakeGit, []byte(contents), 0o755); writeErr != nil {
				t.Fatalf("write staged fake git: %v", writeErr)
			}
		},
	})
	if err != nil {
		t.Fatalf("Create with PATH=. and staged fake git: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Create results = %#v", results)
	}
	if _, statErr := os.Stat(fakeWasRun); !os.IsNotExist(statErr) {
		t.Fatalf("staged fake git ran: %v", statErr)
	}
}

func testStageRoot(t *testing.T, operationRoot string) string {
	t.Helper()
	entries, err := os.ReadDir(operationRoot)
	if err != nil {
		t.Fatalf("read staging parent: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".wb-stage-") {
			return filepath.Join(operationRoot, entry.Name())
		}
	}
	t.Fatal("secure staging directory was not created")
	return ""
}

func TestCreateRejectsWhitespaceEquivalentRepositoryBeforeMutation(t *testing.T) {
	fixture := newGitFixture(t)
	_, err := Create(context.Background(), []string{"acme/app", " acme/app "}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "whitespace-duplicate",
	})
	if err == nil || !strings.Contains(err.Error(), "surrounding whitespace") {
		t.Fatalf("whitespace-equivalent Create error = %v", err)
	}
	if _, statErr := os.Stat(fixture.home); !os.IsNotExist(statErr) {
		t.Fatalf("invalid slug created WB home before rejection: %v", statErr)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, "codex/whitespace-duplicate"); branchErr != nil || exists {
		t.Fatalf("invalid slug created feature branch: exists=%t err=%v", exists, branchErr)
	}
	if status := gitTestOutput(t, fixture.canonical, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("invalid slug mutated canonical clone: %q", status)
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

func TestCreateRollsBackWhenContextIsCancelledAfterStagedAdd(t *testing.T) {
	fixture := newGitFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "cancelled-after-add",
		afterStagedWorktreeAdd: func() error {
			cancel()
			return errors.New("simulated cancellation after staged add")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "simulated cancellation after staged add") {
		t.Fatalf("cancelled Create error = %v", err)
	}
	assertFailedCreateRolledBack(t, fixture, "cancelled-after-add")
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
		if !strings.HasPrefix(entry.Name(), ".wb-stage-") {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil {
			t.Fatalf("inspect staging entry %s: %v", entry.Name(), statErr)
		}
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
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

func TestGuardAllowsLongRealRebaseHistory(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "long-rebase"})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	for index := 0; index < 64; index++ {
		name := fmt.Sprintf("feature-%03d.txt", index)
		if err := os.WriteFile(filepath.Join(worktree, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitTest(t, worktree, "add", name)
		gitTest(t, worktree, "commit", "-m", "feature "+name)
	}
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("feature conflict\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "README.md")
	gitTest(t, worktree, "commit", "-m", "conflicting feature tail")
	if err := os.WriteFile(filepath.Join(fixture.canonical, "README.md"), []byte("main conflict\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "README.md")
	gitTest(t, fixture.canonical, "commit", "-m", "conflicting main tail")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	gitTest(t, worktree, "fetch", "origin", "main")
	if output, rebaseErr := gitTestRun(worktree, "rebase", "origin/main"); rebaseErr == nil {
		t.Fatalf("long rebase unexpectedly succeeded: %s", output)
	}
	guarded, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil || !guarded.Transient {
		t.Fatalf("guard during long rebase = %#v, %v", guarded, err)
	}
}

func TestGuardAllowsInteractiveRebaseAmend(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "interactive-amend"})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "feature.txt")
	gitTest(t, worktree, "commit", "-m", "interactive feature")
	if err := os.WriteFile(filepath.Join(fixture.canonical, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "main.txt")
	gitTest(t, fixture.canonical, "commit", "-m", "advance main")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	gitTest(t, worktree, "fetch", "origin", "main")
	gitTest(t, worktree, "config", "rebase.abbreviateCommands", "true")
	sequenceEditor := filepath.Join(t.TempDir(), "sequence-editor")
	if err := os.WriteFile(sequenceEditor, []byte("#!/bin/sh\nsed 's/^p /e /' \"$1\" > \"$1.wb\" && mv \"$1.wb\" \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if output, rebaseErr := gitTestRunEnv(worktree, []string{"GIT_SEQUENCE_EDITOR=" + sequenceEditor}, "rebase", "-i", "origin/main"); rebaseErr != nil {
		t.Fatalf("start interactive edit rebase: %v\n%s", rebaseErr, output)
	}
	if guarded, guardErr := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); guardErr != nil || !guarded.Transient {
		t.Fatalf("guard before interactive amend = %#v, %v", guarded, guardErr)
	}
	if err := os.WriteFile(filepath.Join(worktree, "amended.txt"), []byte("amended\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "amended.txt")
	gitTest(t, worktree, "commit", "--amend", "--no-edit")
	if subject := gitTestOutput(t, worktree, "reflog", "show", "--format=%gs", "-1", "HEAD"); !strings.HasPrefix(subject, "commit (amend): ") {
		t.Fatalf("interactive amend reflog subject = %q", subject)
	}
	if guarded, guardErr := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); guardErr != nil || !guarded.Transient {
		t.Fatalf("guard after interactive amend = %#v, %v", guarded, guardErr)
	}
	if output, continueErr := gitTestRunEnv(worktree, []string{"GIT_EDITOR=true"}, "rebase", "--continue"); continueErr != nil {
		t.Fatalf("finish interactive amend rebase: %v\n%s", continueErr, output)
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
