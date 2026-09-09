package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func worktreesRootOf(fixture *gitFixture) string {
	return filepath.Join(fixture.home, "worktrees")
}

func makeEmptyTaskNamespace(t *testing.T, fixture *gitFixture, task string) string {
	t.Helper()
	path := filepath.Join(worktreesRootOf(fixture), task)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func taskNamespaceArtifact(t *testing.T, outcome CleanupOutcome, path string) LifecycleArtifact {
	t.Helper()
	for _, artifact := range outcome.Artifacts {
		if artifact.Path == path {
			return artifact
		}
	}
	t.Fatalf("no artifact reported for %s: %#v", path, outcome.Artifacts)
	return LifecycleArtifact{}
}

// TestCleanupRetiresTheTaskNamespaceItEmpties closes the last level of
// residue. removeEmptyParent already retires <task>/<owner> once its last
// repository is gone; the task directory above it was left behind on every
// terminal cleanup, accumulating one empty shell per finished task.
func TestCleanupRetiresTheTaskNamespaceItEmpties(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "cleanup-retires-namespace")
	installMergedPullRequestFixture(t, head, mergedAt)
	// Repository-local placement has no shared physical task namespace:
	// <canonical>/.worktrees/<task> is the task directory itself. The WB_HOME
	// shell remains the logical lock namespace and must be retired too once this
	// is the final member.
	physicalNamespace := created.WorktreeDir
	logicalNamespace := filepath.Join(fixture.home, "worktrees", "cleanup-retires-namespace")

	cleaned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-retires-namespace",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned.Results) != 1 || !cleaned.Results[0].Applied {
		t.Fatalf("cleanup = %#v", cleaned.Results)
	}
	for _, namespace := range []string{physicalNamespace, logicalNamespace} {
		if _, statErr := os.Stat(namespace); !os.IsNotExist(statErr) {
			entries, _ := os.ReadDir(namespace)
			t.Fatalf("terminal cleanup left the task namespace behind: %s: %v %#v", namespace, statErr, entries)
		}
	}
}

// TestCleanupRetiresPreExistingEmptyTaskNamespace clears what earlier releases
// accumulated: a namespace no cleanup will ever run for again, because its
// repositories are long gone.
func TestCleanupRetiresPreExistingEmptyTaskNamespace(t *testing.T) {
	fixture := newGitFixture(t)
	namespace := makeEmptyTaskNamespace(t, fixture, "abandoned-namespace")

	cleaned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "abandoned-namespace",
		Apply: true, OlderThan: 0,
		Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact := taskNamespaceArtifact(t, cleaned, namespace)
	if !artifact.Eligible || !artifact.Applied {
		t.Fatalf("artifact = %#v, want eligible and applied", artifact)
	}
	if _, statErr := os.Stat(namespace); !os.IsNotExist(statErr) {
		t.Fatalf("empty task namespace remains: %v", statErr)
	}
}

// TestCleanupPlanReportsEmptyTaskNamespaceWithoutRetiringIt keeps the dry-run
// contract: a plan states what it would do and mutates nothing.
func TestCleanupPlanReportsEmptyTaskNamespaceWithoutRetiringIt(t *testing.T) {
	fixture := newGitFixture(t)
	namespace := makeEmptyTaskNamespace(t, fixture, "planned-namespace")

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "planned-namespace",
		OlderThan: 0, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact := taskNamespaceArtifact(t, planned, namespace)
	if !artifact.Eligible || artifact.Applied {
		t.Fatalf("artifact = %#v, want eligible and not applied", artifact)
	}
	if _, statErr := os.Stat(namespace); statErr != nil {
		t.Fatalf("dry run mutated the namespace: %v", statErr)
	}
}

// TestCleanupFilterReportsEmptyTaskNamespaceWithoutRetiringIt covers the one
// scoping question this artifact raises. --filter selects by owner/repository
// slug, and an empty namespace has no repository to match, so a filtered run
// must report it rather than silently act outside the selection the operator
// asked for.
func TestCleanupFilterReportsEmptyTaskNamespaceWithoutRetiringIt(t *testing.T) {
	fixture := newGitFixture(t)
	namespace := makeEmptyTaskNamespace(t, fixture, "filtered-namespace")

	filtered, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "filtered-namespace",
		Filter: "acme", Apply: true, OlderThan: 0,
		Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact := taskNamespaceArtifact(t, filtered, namespace)
	if artifact.Eligible || artifact.Applied {
		t.Fatalf("artifact = %#v, want neither eligible nor applied under --filter", artifact)
	}
	if _, statErr := os.Stat(namespace); statErr != nil {
		t.Fatalf("filtered run retired a namespace outside its selection: %v", statErr)
	}
}

// TestCleanupLeavesTaskNamespaceHoldingAnythingAtAll is the safety boundary:
// "empty" means empty. Anything at all inside — a stray file a person left, a
// half-created checkout — keeps the directory.
func TestCleanupLeavesTaskNamespaceHoldingAnythingAtAll(t *testing.T) {
	fixture := newGitFixture(t)
	namespace := makeEmptyTaskNamespace(t, fixture, "occupied-namespace")
	if err := os.WriteFile(filepath.Join(namespace, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleaned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "occupied-namespace",
		Apply: true, OlderThan: 0, Now: time.Now,
	})
	if err == nil {
		for _, artifact := range cleaned.Artifacts {
			if artifact.Path == namespace {
				t.Fatalf("a non-empty namespace was reported as retirable: %#v", artifact)
			}
		}
	}
	if _, statErr := os.Stat(filepath.Join(namespace, "notes.txt")); statErr != nil {
		t.Fatalf("cleanup removed a file inside a task namespace: %v", statErr)
	}
}

// TestOperationLockRefusesRetiredDirectory is the invariant that makes
// retirement safe. An operation can open a task directory and only then take
// its lock; if the namespace is retired in that gap, the operation must refuse
// rather than build a task hierarchy under a pathname nothing can reach.
func TestOperationLockRefusesRetiredDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "task")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	lock, err := acquireLockAt(directory, "retired-namespace")
	if err == nil {
		_ = lock.release()
		t.Fatal("operation lock accepted a directory that was retired while it was starting")
	}
}
