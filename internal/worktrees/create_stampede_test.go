package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestCreateStampedeOnNewTaskSlugHasExactlyOneWinnerAndNoPartialState
// reproduces issue #169: N concurrent `wb worktree create` invocations racing
// on the same brand-new task slug, each for its own distinct repository, the
// natural agent-fleet pattern. Before the fix, every losing invocation
// durably reserved (and never cleaned up) its own Work Log run directory
// under WB_HOME before it ever contended for the per-task lock, leaking one
// orphaned prompt archive per loser on every stampede. This test proves the
// current code has exactly one winner, that every loser fails clean with an
// unambiguous "claim already held" error, and that no partial state survives
// anywhere WB_HOME could be inspected: no orphaned Work Log run, no
// half-created worktree beyond the winner's own.
func TestCreateStampedeOnNewTaskSlugHasExactlyOneWinnerAndNoPartialState(t *testing.T) {
	fixture := newGitFixture(t)
	const n = 9
	repositories := make([]string, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("repo%d", i)
		repositories[i] = "acme/" + name
		gitTest(t, fixture.projectsRoot, "clone", fixture.remote, filepath.Join(fixture.projectsRoot, "acme", name))
	}

	const task = "stampede-task"
	results := make([]error, n)
	worktreeDirs := make([]string, n)

	var ready sync.WaitGroup
	var start sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(n)
	start.Add(1)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer done.Done()
			promptPath := filepath.Join(t.TempDir(), "prompt.txt")
			if err := os.WriteFile(promptPath, []byte(fmt.Sprintf("create repo %d for the stampede\n", i)), 0o600); err != nil {
				results[i] = fmt.Errorf("write prompt fixture: %w", err)
				ready.Done()
				start.Wait()
				return
			}
			workLog := WorkLogOptions{Model: "unknown", OriginalPrompt: promptPath, RequireOriginalPrompt: true}
			prepared, prepareErr := PrepareWorkLogOptions(fixture.projectsRoot, task, workLog)
			ready.Done()
			start.Wait()
			if prepareErr != nil {
				results[i] = prepareErr
				return
			}
			res, err := Create(context.Background(), []string{repositories[i]}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot,
				Operation:    task,
				WorkLog:      prepared,
			})
			results[i] = err
			if err == nil && len(res) == 1 {
				worktreeDirs[i] = res[0].WorktreeDir
			}
		}(i)
	}
	// Release every goroutine only once all of them have finished their own
	// preflight (prompt file write + snapshot) and are blocked on the gate, so
	// the actual Create() calls race as close to simultaneously as this
	// process can arrange.
	ready.Wait()
	start.Done()
	done.Wait()

	succeeded := 0
	var refusalSamples []string
	for i, err := range results {
		if err == nil {
			succeeded++
			continue
		}
		if !strings.Contains(err.Error(), "claim already held by a concurrent create") {
			t.Errorf("repo %d: unexpected failure shape: %v", i, err)
			continue
		}
		refusalSamples = append(refusalSamples, err.Error())
	}
	if succeeded != 1 {
		t.Fatalf("succeeded = %d, want exactly 1 winner; results=%#v", succeeded, results)
	}
	if len(refusalSamples) != n-1 {
		t.Fatalf("clean refusals = %d, want %d; results=%#v", len(refusalSamples), n-1, results)
	}
	if !strings.Contains(refusalSamples[0], fmt.Sprintf("task %q", task)) {
		t.Fatalf("refusal does not name the contended task: %q", refusalSamples[0])
	}

	// No partial worktree state: exactly one physical checkout exists for the
	// task, and it is a real, usable checkout. Repository-local placement has
	// one task directory per canonical repository, so there is no shared owner
	// directory to count.
	var winnerDir string
	for i, dir := range worktreeDirs {
		if dir != "" {
			winnerDir = dir
			_ = i
		}
	}
	if winnerDir == "" {
		t.Fatalf("winner reported no worktree directory")
	}
	if info, statErr := os.Stat(winnerDir); statErr != nil || !info.IsDir() {
		t.Fatalf("winner worktree missing or not a directory: %v", statErr)
	}
	if _, err := os.Stat(filepath.Join(winnerDir, ".git")); err != nil {
		t.Fatalf("winner worktree is not a usable git checkout: %v", err)
	}
	listed, listErr := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: task})
	if listErr != nil {
		t.Fatalf("list stampede task: %v", listErr)
	}
	if len(listed.Results) != 1 || listed.Results[0].WorktreeDir != winnerDir || len(listed.Diagnostics) != 0 {
		t.Fatalf("stampede inventory = %#v diagnostics=%#v, want exactly the winner", listed.Results, listed.Diagnostics)
	}

	// No orphaned claim/run: exactly one Work Log run exists for this task,
	// and it carries a full claim plus run index, not just a stranded prompt
	// archive from a loser that never won the lock.
	runsRoot := filepath.Join(fixture.home, "worklogs", task, "runs")
	runEntries, err := os.ReadDir(runsRoot)
	if err != nil {
		t.Fatalf("read work-log runs directory: %v", err)
	}
	if len(runEntries) != 1 {
		names := make([]string, len(runEntries))
		for i, e := range runEntries {
			names[i] = e.Name()
		}
		t.Fatalf("work-log runs = %d, want exactly 1 (no orphaned reservation from a losing create): %v", len(runEntries), names)
	}
	runDir := filepath.Join(runsRoot, runEntries[0].Name())
	if _, err := os.Stat(filepath.Join(runDir, "run.json")); err != nil {
		t.Fatalf("winning run is missing its run index: %v", err)
	}
	claimsDir := filepath.Join(runDir, "claims")
	claimEntries, err := os.ReadDir(claimsDir)
	if err != nil || len(claimEntries) != 1 {
		t.Fatalf("winning run must have exactly one published claim: entries=%v err=%v", claimEntries, err)
	}
}

// TestCreateStampedeOnSameRepositoryHasExactlyOneWinner covers the degenerate
// case where every concurrent invocation targets the identical (task,
// repository) pair, e.g. a retry storm rather than a fan-out over distinct
// repositories. Exactly one must win; the rest must fail clean, without ever
// invoking Git against the shared destination.
func TestCreateStampedeOnSameRepositoryHasExactlyOneWinner(t *testing.T) {
	fixture := newGitFixture(t)
	const n = 6
	const task = "stampede-same-repo"

	results := make([]error, n)
	var ready, start, done sync.WaitGroup
	ready.Add(n)
	start.Add(1)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer done.Done()
			promptPath := filepath.Join(t.TempDir(), "prompt.txt")
			if err := os.WriteFile(promptPath, []byte(fmt.Sprintf("attempt %d\n", i)), 0o600); err != nil {
				results[i] = err
				ready.Done()
				start.Wait()
				return
			}
			workLog := WorkLogOptions{Model: "unknown", OriginalPrompt: promptPath, RequireOriginalPrompt: true}
			prepared, prepareErr := PrepareWorkLogOptions(fixture.projectsRoot, task, workLog)
			ready.Done()
			start.Wait()
			if prepareErr != nil {
				results[i] = prepareErr
				return
			}
			_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot,
				Operation:    task,
				WorkLog:      prepared,
			})
			results[i] = err
		}(i)
	}
	ready.Wait()
	start.Done()
	done.Wait()

	succeeded, refused := 0, 0
	for i, err := range results {
		switch {
		case err == nil:
			succeeded++
		case strings.Contains(err.Error(), "claim already held by a concurrent create"):
			refused++
		case errors.Is(err, os.ErrExist), strings.Contains(err.Error(), "worktree already exists"):
			// A racer that lost the lock but observed the winner's already-
			// published worktree on a later pass is also an acceptable clean
			// outcome, never corruption; the assertions below still require
			// exactly one success.
			refused++
		default:
			t.Errorf("repo %d: unexpected failure shape: %v", i, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("succeeded = %d, want exactly 1 winner; results=%#v", succeeded, results)
	}
	if succeeded+refused != n {
		t.Fatalf("succeeded(%d)+refused(%d) != n(%d); results=%#v", succeeded, refused, n, results)
	}

	runsRoot := filepath.Join(fixture.home, "worklogs", task, "runs")
	runEntries, err := os.ReadDir(runsRoot)
	if err != nil {
		t.Fatalf("read work-log runs directory: %v", err)
	}
	if len(runEntries) != 1 {
		t.Fatalf("work-log runs = %d, want exactly 1 (no orphaned reservation from a losing create)", len(runEntries))
	}
}
