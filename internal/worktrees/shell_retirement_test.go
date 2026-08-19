package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// setUpShellRetirementFixture points a hermetic WB_HOME at a fresh temp
// directory and returns its worktrees root, without ever running Create or
// Cleanup: RetireTaskShells only ever inspects filesystem structure, so
// every fixture here is built directly with os.MkdirAll/os.WriteFile.
func setUpShellRetirementFixture(t *testing.T) (projectsRoot, worktreesRoot string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, ".wb")
	t.Setenv(wbhome.EnvOverride, home)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	worktreesRoot = filepath.Join(home, "worktrees")
	if err := os.MkdirAll(worktreesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "projects"), worktreesRoot
}

// writeRetiredLock creates a plain, single-link file named like the ones
// operationLock.release() (quarantineLockEntry) actually produces, so
// acquireCleanupTaskAt's claimRetiredLock can reclaim it exactly as it would
// in production.
func writeRetiredLock(t *testing.T, taskDir, suffix string) {
	t.Helper()
	name := ".wb-retired-lock-" + suffix
	if err := os.WriteFile(filepath.Join(taskDir, name), []byte("operation=x\npid=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRetireTaskShellsAppliesToEmptyPreExistingShell is the regression for
// the founder's fleet audit: 626 of 755 task directories had no real
// checkout under them, each left with an empty owner-namespace directory and
// a `.wb-retired-lock-*` file by a cleanup that predates the terminal-
// namespace-residue fix in Cleanup. Those pre-existing shells will never
// clean themselves — this sweep is the retroactive fix.
func TestRetireTaskShellsAppliesToEmptyPreExistingShell(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	taskDir := filepath.Join(worktreesRoot, "old-task")
	ownerDir := filepath.Join(taskDir, "sneat-co")
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRetiredLock(t, taskDir, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	dry, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projectsRoot})
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Results) != 1 || !dry.Results[0].Eligible || dry.Results[0].Applied {
		t.Fatalf("dry-run sweep = %#v", dry.Results)
	}
	if _, statErr := os.Stat(ownerDir); statErr != nil {
		t.Fatalf("dry run removed the owner directory: %v", statErr)
	}

	applied, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projectsRoot, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Results) != 1 || !applied.Results[0].Applied || applied.Results[0].Error != "" {
		t.Fatalf("applied sweep = %#v", applied.Results)
	}
	if info, statErr := os.Stat(taskDir); statErr != nil || !info.IsDir() {
		t.Fatalf("task root was not retained: info=%v err=%v", info, statErr)
	}
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("task namespace still has residue after retirement: %#v", entries)
	}
}

// TestRetireTaskShellsAppliesToNestedEmptyRepositoryDirectory covers the
// deeper of the two shapes it must recognize: an owner directory that still
// holds an empty <owner>/<repository> directory rather than being empty
// itself, matching a task directory retired by code older than
// removeEmptyParent's own owner-directory removal.
func TestRetireTaskShellsAppliesToNestedEmptyRepositoryDirectory(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	taskDir := filepath.Join(worktreesRoot, "old-task")
	repoDir := filepath.Join(taskDir, "sneat-co", "wb")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	applied, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projectsRoot, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Results) != 1 || !applied.Results[0].Applied {
		t.Fatalf("applied sweep = %#v", applied.Results)
	}
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("nested empty repository shell left residue: %#v", entries)
	}
}

// TestRetireTaskShellsNeverTouchesARealCheckout proves the sweep is
// conservative: an owner/repository directory that still holds anything at
// all (a stand-in for a real Git checkout, which always has a .git entry)
// must never be inspected further or removed.
func TestRetireTaskShellsNeverTouchesARealCheckout(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	taskDir := filepath.Join(worktreesRoot, "live-task")
	repoDir := filepath.Join(taskDir, "sneat-co", "wb")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	applied, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projectsRoot, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Results) != 1 || applied.Results[0].Eligible || applied.Results[0].Applied {
		t.Fatalf("a real checkout must never be eligible: %#v", applied.Results)
	}
	if _, statErr := os.Stat(filepath.Join(repoDir, ".git")); statErr != nil {
		t.Fatalf("real checkout marker was removed: %v", statErr)
	}
}

// TestRetireTaskShellsRefusesALiveLock proves the sweep never removes a lock
// belonging to a live (or merely interrupted, unresolved) operation: a
// `.lock` entry — as opposed to an already-retired `.wb-retired-lock-*` —
// must always be left for `wb worktree cleanup <task> --resume-interrupted`
// or a live operation to resolve, never treated as sweepable residue.
func TestRetireTaskShellsRefusesALiveLock(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	taskDir := filepath.Join(worktreesRoot, "locked-task")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, ".lock"), []byte("operation=locked-task\npid=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	applied, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projectsRoot, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Results) != 1 || applied.Results[0].Eligible || applied.Results[0].Applied {
		t.Fatalf("a task with a live lock must never be eligible: %#v", applied.Results)
	}
	if _, statErr := os.Stat(filepath.Join(taskDir, ".lock")); statErr != nil {
		t.Fatalf(".lock was removed instead of left for recovery: %v", statErr)
	}
}

// TestRetireTaskShellsPreservesReservedStageEntry proves a reserved
// .wb-stage-*/.wb-retired-stage-* entry is left exactly where Cleanup's own
// explicit blocking backlog expects to find it (see
// #req:internal-stage-terminalization), never swept as ordinary residue.
func TestRetireTaskShellsPreservesReservedStageEntry(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	taskDir := filepath.Join(worktreesRoot, "stage-task")
	stageDir := filepath.Join(taskDir, ".wb-stage-abc123")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}

	applied, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projectsRoot, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Results) != 1 || applied.Results[0].Eligible || applied.Results[0].Applied {
		t.Fatalf("a reserved stage entry must never be eligible: %#v", applied.Results)
	}
	if _, statErr := os.Stat(stageDir); statErr != nil {
		t.Fatalf("reserved stage entry was removed: %v", statErr)
	}
}

// TestRetireTaskShellsFilterNarrowsTheSweep matches the --filter behavior of
// every other WB fleet sweep: an unmatched task is invisible to the run.
func TestRetireTaskShellsFilterNarrowsTheSweep(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	for _, task := range []string{"keep-me", "skip-me"} {
		if err := os.MkdirAll(filepath.Join(worktreesRoot, task, "sneat-co"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	outcome, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projectsRoot, Filter: "keep"})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Task != "keep-me" {
		t.Fatalf("filtered sweep = %#v", outcome.Results)
	}
}
