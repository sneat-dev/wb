//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

type dirtyLegacyNativeQuery struct {
	runner.Runner
	observe func(string, string, []string, runner.Result, error)
}

func (r dirtyLegacyNativeQuery) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
	r.observe(dir, name, args, result, err)
	return result, err
}

//nolint:paralleltest // Existing native Git fixture configures process-wide Git/WB environment.
func TestE2EDirtyLegacyUntrackedQueryRetainsActualNativeFailure(t *testing.T) {
	fixture := newGitFixture(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitDir := filepath.Join(fixture.canonical, ".git")
	retained := gitDir + "-retained"
	changed := false
	var cause error
	var nativeDetail string
	ctx := withGitRunner(t.Context(), dirtyLegacyNativeQuery{Runner: runner.New(), observe: func(dir, name string, args []string, result runner.Result, err error) {
		if name != "git" || dir != fixture.canonical {
			return
		}
		if len(args) > 2 && args[2] == "diff" {
			if err != nil {
				t.Fatalf("actual tracked query prerequisite failed: %v", err)
			}
			if err := os.Rename(gitDir, retained); err != nil {
				t.Fatal(err)
			}
			changed = true
		}
		if len(args) > 2 && args[2] == "ls-files" {
			cause = err
			nativeDetail = strings.TrimSpace(result.CombinedOutput)
		}
	}})
	paths, err := dirtyCapturePaths(ctx, fixture.canonical)
	if !changed || cause == nil || paths != nil || nativeDetail == "" || !strings.Contains(err.Error(), nativeDetail) || !strings.HasPrefix(err.Error(), "inspect untracked dirty paths:") {
		t.Fatalf("actual native untracked refusal: %v %v cause=%v", paths, err, cause)
	}
	if err := os.Rename(retained, gitDir); err != nil {
		t.Fatal(err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD changed: %s", got)
	}
}

//nolint:paralleltest // Each actual Git/Work Log fixture pins process-wide configuration.
func TestE2EDirtyLegacyCapturePreservesPrivatePhaseRefusals(t *testing.T) {
	for _, kind := range []string{"projection", "run", "materialization"} {
		//nolint:paralleltest // The existing Git fixture uses t.Setenv.
		t.Run(kind, func(t *testing.T) {
			fixture := newGitFixture(t)
			created, err := Create(t.Context(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "capture-" + kind, WorkLog: WorkLogOptions{Model: "unknown"}})
			if err != nil || len(created) != 1 {
				t.Fatalf("native claimed checkout prerequisite: %+v %v", created, err)
			}
			worktree := created[0].WorktreeDir
			claimBefore, err := os.ReadFile(created[0].WorkLogPath)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := readWorkLogProjection(worktree)
			if err != nil {
				t.Fatal(err)
			}
			runPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID)
			head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
			payload := []byte("retain unsaved user work\n")
			payloadPath := filepath.Join(worktree, "unsaved.txt")
			if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := collectDirtyCapture(t.Context(), worktree); err != nil {
				t.Fatalf("native capture prerequisite: %v", err)
			}
			occupied := ""
			var control error
			switch kind {
			case "projection":
				occupied = filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName)
				if err := os.WriteFile(occupied, []byte("{invalid"), 0o600); err != nil {
					t.Fatal(err)
				}
				_, control = readWorkLogProjectionForClaim(fixture.home, worktree)
			case "run":
				occupied = runPath
				if err := os.Rename(runPath, runPath+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(runPath, []byte("blocked private run"), 0o600); err != nil {
					t.Fatal(err)
				}
				_, _, control = openWorkLogRun(fixture.home, projection.EffortID, projection.RunID, false)
			default:
				occupied = filepath.Join(runPath, "dirty-discard")
				if err := os.WriteFile(occupied, []byte("blocked private capture"), 0o600); err != nil {
					t.Fatal(err)
				}
				owned, err := os.Open(runPath)
				if err != nil {
					t.Fatal(err)
				}
				_, control = openPrivateChild(owned, "dirty-discard", true)
				_ = owned.Close()
			}
			if control == nil {
				t.Fatal("native phase prerequisite did not refuse")
			}
			before, err := os.ReadFile(occupied)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := captureAndPersistDirtyWorktree(t.Context(), fixture.home, worktree, nil)
			if err == nil || receipt != nil {
				t.Fatalf("private phase accepted: %+v %v", receipt, err)
			}
			if kind == "projection" {
				if err.Error() != control.Error() {
					t.Fatalf("projection cause changed: %v control=%v", err, control)
				}
			} else {
				cause := control
				for errors.Unwrap(cause) != nil {
					cause = errors.Unwrap(cause)
				}
				if !errors.Is(err, cause) {
					t.Fatalf("native owned-open cause changed: %v control=%v", err, cause)
				}
				if kind == "run" && !strings.HasPrefix(err.Error(), "open private Work Log run for dirty capture:") {
					t.Fatalf("run phase prefix lost: %v", err)
				}
			}
			after, readErr := os.ReadFile(occupied)
			if readErr != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("blocking evidence changed: %q %v", after, readErr)
			}
			after, readErr = os.ReadFile(payloadPath)
			if readErr != nil || !reflect.DeepEqual(after, payload) {
				t.Fatalf("unsaved bytes changed: %q %v", after, readErr)
			}
			claimPath := created[0].WorkLogPath
			if kind == "run" {
				claimPath = strings.Replace(claimPath, runPath, runPath+"-retained", 1)
			}
			claimAfter, readErr := os.ReadFile(claimPath)
			if readErr != nil || !reflect.DeepEqual(claimBefore, claimAfter) {
				t.Fatalf("private claim bytes changed: %q %v", claimAfter, readErr)
			}
			if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != head {
				t.Fatalf("HEAD changed: %s", got)
			}
		})
	}
}

//nolint:paralleltest // Existing native claimed fixture pins process-wide configuration.
func TestE2EDirtyLegacyCaptureBindsActualReceipts(t *testing.T) {
	fixture := newGitFixture(t)
	// A non-repository genuinely refuses the first native query, before parsing.
	if paths, err := dirtyCapturePaths(t.Context(), t.TempDir()); err == nil || paths != nil || !strings.HasPrefix(err.Error(), "inspect tracked dirty paths:") {
		t.Fatalf("native tracked refusal: %v %v", paths, err)
	}
	if receipt, err := captureAndPersistDirtyWorktree(t.Context(), fixture.home, fixture.canonical, nil); err == nil || receipt != nil || !strings.Contains(err.Error(), "without a private Work Log claim") {
		t.Fatalf("unclaimed native checkout: %+v %v", receipt, err)
	}
	created, err := Create(t.Context(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "capture-receipts", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil || len(created) != 1 {
		t.Fatalf("native claimed prerequisite: %+v %v", created, err)
	}
	path := created[0].WorktreeDir
	payload := filepath.Join(path, "unsaved.txt")
	if err := os.WriteFile(payload, []byte("first private bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	material, err := collectDirtyCapture(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := captureAndPersistDirtyWorktree(t.Context(), fixture.home, path, &material.Manifest.Receipt)
	if err != nil || receipt == nil || *receipt != material.Manifest.Receipt {
		t.Fatalf("native persisted receipt: %+v %v", receipt, err)
	}
	if err := os.WriteFile(payload, []byte("changed private bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if receipt, err := captureAndPersistDirtyWorktree(t.Context(), fixture.home, path, &material.Manifest.Receipt); err == nil || receipt != nil || !strings.HasPrefix(err.Error(), "dirty worktree bytes changed after evidence capture:") {
		t.Fatalf("changed native receipt: %+v %v", receipt, err)
	}
	if receipt, err := captureAndPersistDirtyWorktree(t.Context(), fixture.home, filepath.Join(t.TempDir(), "missing"), nil); err == nil || receipt != nil {
		t.Fatalf("missing capture root accepted: %+v %v", receipt, err)
	}
}
