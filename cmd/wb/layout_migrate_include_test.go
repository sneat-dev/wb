package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// fetchableFixtureAt builds a real, fetchable local-origin clone at
// clonePath, mirroring dalgoFetchableFixtureAt/dalgo2sqlFetchableFixtureAt
// for an arbitrary repository: worktrees.Create needs a reachable origin to
// fetch and verify against, and the origin is repointed to its conceptual
// forge identity only after every claim already exists.
func fetchableFixtureAt(t *testing.T, root, originName, clonePath, content string) {
	t.Helper()
	origin := filepath.Join(root, originName)
	if _, statErr := os.Stat(origin); statErr != nil {
		runGit(t, root, "init", "--bare", "-b", "main", origin)
	}
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(clonePath), "clone", origin, filepath.Base(clonePath))
	runGit(t, clonePath, "config", "user.email", "wb@example.test")
	runGit(t, clonePath, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(clonePath, "README.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clonePath, "add", ".")
	runGit(t, clonePath, "commit", "-m", "init")
	runGit(t, clonePath, "push", "-u", "origin", "main")
}

// TestLayoutMigrateIncludesNamedActiveTasks encodes
// projects-root-layout#ac:migrate-includes-named-active-tasks: acme/one is
// held by an active claim (t-one) recorded under a legacy $HOME/.wb-style
// home, and acme/two is held by an active claim (t-two) through a worktree
// nested inside the clone. --include-task t-one lifts only acme/one's
// live-claim refusal; acme/two, whose task is not included, is still
// skipped.
func TestLayoutMigrateIncludesNamedActiveTasks(t *testing.T) {
	// Not t.Parallel(): uses t.Setenv (XDG_CONFIG_HOME) to select the store
	// mode while creating claims, which testing forbids alongside
	// t.Parallel.
	root := t.TempDir()
	legacyHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(legacyHome, ".wb", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}

	cloneOne := filepath.Join(root, "acme", "one")
	fetchableFixtureAt(t, root, "one-origin.git", cloneOne, "acme/one\n")
	cloneTwo := filepath.Join(root, "acme", "two")
	fetchableFixtureAt(t, root, "two-origin.git", cloneTwo, "acme/two\n")

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "wb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "worktrees.yaml")
	// repository-local while creating t-two: its worktree lands nested
	// inside clone acme/two, per the AC's "worktree inside the clone".
	if err := os.WriteFile(configPath, []byte("version: 1\nworktrees:\n  store: repository-local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	createdTwo, err := worktrees.Create(context.Background(), []string{"acme/two"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "t-two", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create t-two: %v", err)
	}
	twoWorktree := createdTwo[0].WorktreeDir // active: never finalized

	// Reset to central default before creating t-one, whose own worktree
	// location does not matter to this AC -- only that its claim resolves
	// through the legacy home after being moved there.
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	createdOne, err := worktrees.Create(context.Background(), []string{"acme/one"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "t-one", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create t-one: %v", err)
	}
	oneWorktree := createdOne[0].WorktreeDir // active: never finalized
	moveWorklogTaskToLegacyHome(t, filepath.Join(root, ".wb"), filepath.Join(legacyHome, ".wb"), "t-one")

	runGit(t, cloneOne, "remote", "set-url", "origin", "git@github.com:acme/one.git")
	runGit(t, cloneTwo, "remote", "set-url", "origin", "git@github.com:acme/two.git")

	destinationOne := filepath.Join(root, "github.com", "acme", "one")

	// Dry run: acme/one is planned only because of the inclusion, naming
	// t-one; acme/two is still skipped for its own (not included) claim.
	dry := runWBWithHome(t, legacyHome, "layout", "migrate", "--projects-root", root, "--include-task", "t-one", "--format", "json")
	if dry.exitCode != exitFindings {
		t.Fatalf("dry-run exit = %d, want findings (acme/two still skipped); stderr=%s stdout=%s", dry.exitCode, dry.stderr, dry.stdout)
	}
	plan := decodeMigrateReport(t, dry.stdout)
	planIndexOne, found := findMigrateClone(plan, "acme/one")
	if !found || plan.Clones[planIndexOne].Status != "planned" || !strings.Contains(plan.Clones[planIndexOne].Reason, "included: active task t-one") {
		t.Fatalf("dry run clone acme/one = %+v, want planned naming the inclusion", plan.Clones)
	}
	planIndexTwo, found := findMigrateClone(plan, "acme/two")
	if !found || plan.Clones[planIndexTwo].Status != "skipped" || !strings.Contains(plan.Clones[planIndexTwo].Reason, "t-two") {
		t.Fatalf("dry run clone acme/two = %+v, want skipped naming t-two's live claim", plan.Clones)
	}

	applied := runWBWithHome(t, legacyHome, "layout", "migrate", "--projects-root", root, "--include-task", "t-one", "--apply", "--format", "json")
	if applied.exitCode != exitFindings {
		t.Fatalf("apply exit = %d, want findings (acme/two still skipped); stderr=%s stdout=%s", applied.exitCode, applied.stderr, applied.stdout)
	}
	report := decodeMigrateReport(t, applied.stdout)
	cloneIndexOne, found := findMigrateClone(report, "acme/one")
	if !found || report.Clones[cloneIndexOne].Status != "done" {
		t.Fatalf("apply clone acme/one = %+v, want done", report.Clones)
	}
	cloneIndexTwo, found := findMigrateClone(report, "acme/two")
	if !found || report.Clones[cloneIndexTwo].Status != "skipped" || !strings.Contains(report.Clones[cloneIndexTwo].Reason, "t-two") {
		t.Fatalf("apply clone acme/two = %+v, want still skipped naming t-two's live claim", report.Clones)
	}
	if _, err := os.Stat(cloneTwo); err != nil {
		t.Fatalf("acme/two must not have moved: %v", err)
	}

	// acme/one moved; its worktree (outside the clone, in the legacy home)
	// is repointed in place, not relocated, and its claim resolves at the
	// clone's new path -- the same resolution `wb worktree land`'s
	// claim-authority check depends on (see
	// worktrees.RecordCloneMoveRelocationIntents).
	if _, err := os.Stat(oneWorktree); err != nil {
		t.Fatalf("t-one's worktree must remain exactly where it was: %v", err)
	}
	gitOutput(t, oneWorktree, "status", "--porcelain=v1")
	listed, err := worktrees.List(context.Background(), worktrees.ListOptions{ProjectsRoot: root, Task: "t-one"})
	if err != nil {
		t.Fatalf("list t-one after migration: %v", err)
	}
	if len(listed) != 1 || listed[0].CanonicalDir != destinationOne {
		t.Fatalf("t-one resolution = %+v, want exactly one result naming the clone's new path %s", listed, destinationOne)
	}
	if _, err := os.Stat(twoWorktree); err != nil {
		t.Fatalf("t-two's worktree must remain exactly where it was: %v", err)
	}
}

// TestLayoutMigrateIncludeUnknownTaskIsUsageError encodes the AC's second
// When/Then: an --include-task name matching no live claim in any resolved
// home fails the run with the usage exit code before anything moves.
func TestLayoutMigrateIncludeUnknownTaskIsUsageError(t *testing.T) {
	root := t.TempDir()
	clone := filepath.Join(root, "acme", "unrelated")
	fetchableFixtureAt(t, root, "unrelated-origin.git", clone, "acme/unrelated\n")
	runGit(t, clone, "remote", "set-url", "origin", "git@github.com:acme/unrelated.git")

	result := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--include-task", "no-such-task", "--format", "json")
	if result.exitCode != exitUsage {
		t.Fatalf("exit = %d, want usage; stderr=%s stdout=%s", result.exitCode, result.stderr, result.stdout)
	}
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("clone must remain exactly in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "github.com", "acme", "unrelated")); !os.IsNotExist(err) {
		t.Fatalf("nothing must have moved: err=%v", err)
	}
}

// TestLayoutMigrateIncludeActiveTasksStillSkipsBusyCheckout encodes the AC's
// third When/Then: --include-active-tasks lifts the live-claim refusal, but
// a busy process with its working directory inside the claimed worktree
// still refuses the clone.
func TestLayoutMigrateIncludeActiveTasksStillSkipsBusyCheckout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("busy-process detection only runs on linux")
	}
	root := t.TempDir()
	clone := filepath.Join(root, "acme", "busy")
	fetchableFixtureAt(t, root, "busy-origin.git", clone, "acme/busy\n")

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "wb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "worktrees.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\nworktrees:\n  store: repository-local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := worktrees.Create(context.Background(), []string{"acme/busy"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "t-busy", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create t-busy: %v", err)
	}
	worktree := created[0].WorktreeDir
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "remote", "set-url", "origin", "git@github.com:acme/busy.git")

	cmd := exec.Command("sleep", "60")
	cmd.Dir = worktree
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	result := runWBHomeIsolated(t, "layout", "migrate", "--projects-root", root, "--include-active-tasks", "--apply", "--format", "json")
	if result.exitCode != exitFindings {
		t.Fatalf("exit = %d, want findings; stderr=%s stdout=%s", result.exitCode, result.stderr, result.stdout)
	}
	report := decodeMigrateReport(t, result.stdout)
	cloneIndex, found := findMigrateClone(report, "acme/busy")
	if !found || report.Clones[cloneIndex].Status != "skipped" || !strings.Contains(report.Clones[cloneIndex].Reason, "process") {
		t.Fatalf("clone = %+v, want skipped naming the busy process", report.Clones)
	}
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("clone must not have moved: %v", err)
	}
}
