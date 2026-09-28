package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestEngineRunPreflightAndUnavailableRepositories(t *testing.T) {
	t.Parallel()
	t.Run("invalid options", func(t *testing.T) {
		t.Parallel()
		results, err := Run(context.Background(), nil, textHandler{}, Options{})
		if err == nil || !strings.Contains(err.Error(), "GitHub directory is required") || results != nil {
			t.Fatalf("results=%+v err=%v", results, err)
		}
	})
	t.Run("held lock", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		lock, err := AcquireOperationLock(root, "busy", false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lock.Release() })
		results, err := Run(context.Background(), nil, textHandler{}, Options{GitHubDir: root, Operation: "busy", Resume: true})
		if err == nil || !strings.Contains(err.Error(), "already active") || len(results) != 0 {
			t.Fatalf("results=%+v err=%v", results, err)
		}
	})
	for _, tc := range []struct {
		name        string
		merge, wait bool
	}{
		{name: "merge", merge: true},
		{name: "wait only", wait: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			options := Options{GitHubDir: root, Operation: "archived", Merge: tc.merge, WaitForPRChecks: tc.wait, PR: tc.wait}
			results, err := Run(context.Background(), []Repository{{Slug: "acme/old", Archived: true}}, textHandler{}, options)
			if err != nil || len(results) != 1 || results[0].Status != "skipped" || results[0].PR != "" {
				t.Fatalf("results=%+v err=%v", results, err)
			}
		})
	}
}

func TestEngineManagedInputRejectsUnsafeOrUnownedPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	options := Options{GitHubDir: root, Ref: "main"}
	missing := filepath.Join(root, "missing")
	if guard, err := managedInputWorktree(context.Background(), missing, "acme/app", options); err != nil || guard != nil {
		t.Fatalf("missing path: guard=%+v err=%v", guard, err)
	}
	directory := filepath.Join(root, "plain")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if guard, err := managedInputWorktree(context.Background(), directory, "acme/app", options); err != nil || guard != nil {
		t.Fatalf("plain directory: guard=%+v err=%v", guard, err)
	}
	for _, tc := range []struct {
		name     string
		makePath func(string) error
		want     string
	}{
		{name: "regular file", makePath: func(path string) error { return os.WriteFile(path, []byte("not a directory"), 0o600) }, want: "non-symlink directory"},
		{name: "symlink", makePath: func(path string) error { return os.Symlink(directory, path) }, want: "non-symlink directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(root, strings.ReplaceAll(tc.name, " ", "-"))
			if err := tc.makePath(path); err != nil {
				t.Fatal(err)
			}
			if _, err := managedInputWorktree(context.Background(), path, "acme/app", options); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
	pipe := filepath.Join(directory, ".git")
	if err := os.Mkdir(pipe, 0o755); err != nil {
		t.Fatal(err)
	}
	if guard, err := managedInputWorktree(context.Background(), directory, "acme/app", options); err != nil || guard != nil {
		t.Fatalf("ordinary clone marker: guard=%+v err=%v", guard, err)
	}
	if err := os.Remove(pipe); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(missing, pipe); err != nil {
		t.Fatal(err)
	}
	if _, err := managedInputWorktree(context.Background(), directory, "acme/app", options); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink git entry error=%v", err)
	}
	if err := os.Remove(pipe); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pipe, []byte("gitdir: elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := managedInputWorktree(context.Background(), directory, "acme/app", options); err == nil || !strings.Contains(err.Error(), "verify supplied managed worktree") {
		t.Fatalf("unowned gitdir error=%v", err)
	}
}

func TestEnginePrepareWorktreeResumeBoundaries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	worktree := filepath.Join(root, "checkout")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	const branch = "wb/resume"
	t.Run("branch read failure", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		fake.ExpectArgv([]string{"git", "branch", "--show-current"}, runner.Result{}, errors.New("branch unavailable"))
		_, err := prepareWorktree(context.Background(), root, "acme/app", worktree, worktrees.WorktreePlacement{}, "sha", false, branch, "origin/main", Options{Resume: true, run: fake, Timeout: time.Second})
		if err == nil || !strings.Contains(err.Error(), "branch unavailable") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("exact branch resume", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		fake.ExpectArgv([]string{"git", "branch", "--show-current"}, runner.Result{Stdout: branch + "\n"}, nil)
		created, err := prepareWorktree(context.Background(), root, "acme/app", worktree, worktrees.WorktreePlacement{}, "sha", false, branch, "origin/main", Options{Resume: true, run: fake, Timeout: time.Second})
		if err != nil || created != nil {
			t.Fatalf("created=%+v err=%v", created, err)
		}
	})
	t.Run("disappeared registration", func(t *testing.T) {
		t.Parallel()
		_, err := prepareWorktree(context.Background(), root, "acme/app", filepath.Join(root, "gone"), worktrees.WorktreePlacement{}, "sha", true, branch, "origin/main", Options{})
		if err == nil || !strings.Contains(err.Error(), "registered resume worktree disappeared") {
			t.Fatalf("error=%v", err)
		}
	})
}

func installEngineWorkflowGH(t *testing.T, fixture engineFixture, script string) {
	t.Helper()
	orchCovInstallGH(t, script)
	t.Setenv("WB_HOLD_REMOTE", fixture.repository.CloneURL)
	t.Setenv("WB_HOLD_BREACH", filepath.Join(t.TempDir(), "merge-invoked"))
}

func replaceEngineWorkflowGH(t *testing.T, old, new string) string {
	t.Helper()
	script := strings.Replace(holdGHScript, old, new, 1)
	if script == holdGHScript {
		t.Fatalf("GitHub fixture does not contain %q", old)
	}
	return script
}

//nolint:paralleltest // newEngineFixture and the scripted GitHub CLI use t.Setenv
func TestEngineRunWaitOnlyRecordsExactHeadChecks(t *testing.T) {
	fixture := newEngineFixture(t)
	installEngineWorkflowGH(t, fixture, holdGHScript)
	options := fixture.options()
	options.WaitForPRChecks = true
	options.PR = true
	options.Timeout = 30 * time.Second
	options.CheckPollInterval = time.Millisecond
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err != nil || len(results) != 1 || results[0].Status != "validated" || results[0].PR == "" || results[0].Merged {
		t.Fatalf("results=%+v err=%v", results, err)
	}
}

//nolint:paralleltest // newEngineFixture and the scripted GitHub CLI use t.Setenv
func TestEngineRunWaitOnlyReportsFailedExactHeadChecks(t *testing.T) {
	fixture := newEngineFixture(t)
	script := replaceEngineWorkflowGH(t, `"conclusion":"success"`, `"conclusion":"failure"`)
	installEngineWorkflowGH(t, fixture, script)
	options := fixture.options()
	options.WaitForPRChecks = true
	options.PR = true
	options.Timeout = 30 * time.Second
	options.CheckPollInterval = time.Millisecond
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err == nil || len(results) != 1 || results[0].Status != "failed" || results[0].PR == "" || !strings.Contains(results[0].Reason, "CI") {
		t.Fatalf("results=%+v err=%v", results, err)
	}
}

//nolint:paralleltest // newEngineFixture and the scripted GitHub CLI use t.Setenv
func TestEngineRunMergeReportsServerFailureAfterPassingChecks(t *testing.T) {
	fixture := newEngineFixture(t)
	script := replaceEngineWorkflowGH(t, `echo "HELD REPOSITORY WAS MERGED" > "$WB_HOLD_BREACH"; exit 0`, `echo "injected server merge failure" >&2; exit 1`)
	installEngineWorkflowGH(t, fixture, script)
	options := fixture.options()
	options.Merge = true
	options.Timeout = 30 * time.Second
	options.CheckPollInterval = time.Millisecond
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err == nil || len(results) != 1 || results[0].Status != "failed" || results[0].Merged || results[0].PR == "" || !strings.Contains(results[0].Reason, "injected server merge failure") {
		t.Fatalf("results=%+v err=%v", results, err)
	}
}

//nolint:paralleltest // newEngineFixture and the scripted GitHub CLI use t.Setenv
func TestEngineRunHeldRepositoryReportsFailedChecksWithoutMerging(t *testing.T) {
	fixture := newEngineFixture(t)
	script := replaceEngineWorkflowGH(t, `"conclusion":"success"`, `"conclusion":"failure"`)
	installEngineWorkflowGH(t, fixture, script)
	options := fixture.options()
	options.Merge = true
	options.Hold = []string{"acme/*"}
	options.Timeout = 30 * time.Second
	options.CheckPollInterval = time.Millisecond
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err == nil || len(results) != 1 || results[0].Status != "failed" || !results[0].Held || results[0].Merged || !strings.Contains(results[0].Reason, "CI") {
		t.Fatalf("results=%+v err=%v", results, err)
	}
}

func TestEngineCheckWaitRejectsPollIntervalOutsideBound(t *testing.T) {
	t.Parallel()
	options := Options{Timeout: time.Second, CheckPollInterval: time.Second}
	result := Result[string]{Repository: "acme/app", PR: "https://example.test/pull/1", Commit: "abc"}
	if err := waitAndMerge(context.Background(), options, &result); err == nil || !strings.Contains(err.Error(), "shorter than bounded merge slice") {
		t.Fatalf("merge wait error=%v", err)
	}
	if err := waitForPRChecks(context.Background(), options, &result); err == nil || !strings.Contains(err.Error(), "shorter than bounded PR-check slice") {
		t.Fatalf("PR wait error=%v", err)
	}
}

func TestEngineOperationLockRejectsUnwritableLayout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	blocked := filepath.Join(root, "file")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireOperationLock(blocked, "task", false); err == nil {
		t.Fatal("file root was accepted")
	}
	if err := os.Mkdir(filepath.Join(root, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".wb", "worktrees"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireOperationLock(root, "task", false); err == nil {
		t.Fatal("blocked operation directory was accepted")
	}
}

//nolint:paralleltest // newEngineFixture uses t.Setenv
func TestEngineManagedInputRefusesAnotherRepository(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "other-repo-input", "feature/other-repo-input", "other.txt", "other\n")
	_, err := managedInputWorktree(context.Background(), source.WorktreeDir, "acme/other", fixture.options())
	if err == nil || !strings.Contains(err.Error(), "belongs to") {
		t.Fatalf("cross-repository input error=%v", err)
	}
}

func TestEngineRegisteredWorktreeLookupSurfacesRunnerFailure(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "worktree", "list", "--porcelain"}, runner.Result{}, errors.New("worktree list unavailable"))
	_, err := registeredWorktreeForBranch(context.Background(), t.TempDir(), "wb/task", Options{run: fake})
	if err == nil || !strings.Contains(err.Error(), "worktree list unavailable") {
		t.Fatalf("lookup error=%v", err)
	}
}

func TestEngineRunSortsAndRejectsInvalidRepositorySelection(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	results, err := Run(context.Background(), []Repository{
		{Slug: "zeta/old", Archived: true}, {Slug: "alpha/old", Archived: true},
	}, textHandler{}, Options{GitHubDir: root, Operation: "sorted", DryRun: true})
	if err != nil || len(results) != 2 || results[0].Repository != "alpha/old" || results[1].Repository != "zeta/old" {
		t.Fatalf("sorted results=%+v err=%v", results, err)
	}
	results, err = Run(context.Background(), []Repository{{Slug: "invalid-slug"}}, textHandler{}, Options{GitHubDir: root, Operation: "invalid", DryRun: true})
	if err == nil || len(results) != 1 || results[0].Status != "failed" {
		t.Fatalf("invalid repository results=%+v err=%v", results, err)
	}
}

func TestEnginePrepareWorktreeReportsUnstatablePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "loop")
	if err := os.Symlink(path, path); err != nil {
		t.Fatal(err)
	}
	_, err := prepareWorktree(context.Background(), root, "acme/app", path, worktrees.WorktreePlacement{}, "sha", false, "wb/task", "origin/main", Options{})
	if err == nil || !strings.Contains(err.Error(), "too many levels") {
		t.Fatalf("unstatable worktree error=%v", err)
	}
}

func TestEngineOperationWorktreePathReportsGitFailureAndRegisteredResume(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := ResolvedBase{Ref: "main"}
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "rev-parse", "--verify", "origin/main^{commit}"}, runner.Result{}, errors.New("base vanished"))
	_, _, _, _, err := operationWorktreePath(context.Background(), root, "acme/app", Options{run: fake}, base)
	if err == nil || !strings.Contains(err.Error(), "base vanished") {
		t.Fatalf("base error=%v", err)
	}
	fake = runnertest.New(t)
	registered := filepath.Join(root, "registered")
	fake.ExpectArgv([]string{"git", "worktree", "list", "--porcelain"}, runner.Result{Stdout: "worktree " + registered + "\nbranch refs/heads/wb/task\n"}, nil)
	path, _, _, resume, err := operationWorktreePath(context.Background(), root, "acme/app", Options{Resume: true, Branch: "wb/task", run: fake}, base)
	if err != nil || path != registered || !resume {
		t.Fatalf("path=%q resume=%t err=%v", path, resume, err)
	}
}

type engineRejectPublishHandler struct{ orchCovStageHandler }

func (engineRejectPublishHandler) ValidatePublishable(context.Context, string, Repository) error {
	return errors.New("policy refused publication")
}

//nolint:paralleltest // newEngineFixture uses t.Setenv
func TestEngineRunKeepsUnpublishableMutationLocal(t *testing.T) {
	fixture := newEngineFixture(t)
	options := fixture.options()
	options.Commit = true
	handler := engineRejectPublishHandler{orchCovStageHandler{assessment: Assessment[string]{Applicable: true, NeedsChange: true}}}
	results, err := Run(context.Background(), []Repository{fixture.repository}, handler, options)
	if err == nil || len(results) != 1 || results[0].Status != "failed" || !strings.Contains(results[0].Reason, "policy refused publication") {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	if results[0].WorktreeDir == "" {
		t.Fatal("mutation never reached isolated worktree")
	}
}

type engineReportedHandler struct{ orchCovStageHandler }

func (engineReportedHandler) AppliedFiles(string) []string { return []string{"reported.txt", ""} }

func TestEngineChangedFilesSinceIncludesReportedAndRenamedFiles(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "status", "--porcelain=v1", "-z"}, runner.Result{Stdout: "R  old.txt -> new.txt\x00"}, nil)
	files, err := changedFilesSince(context.Background(), t.TempDir(), nil,
		engineReportedHandler{}, "metadata", Options{run: fake})
	if err != nil || len(files) != 2 || files[0] != "new.txt" || files[1] != "reported.txt" {
		t.Fatalf("changed files=%q err=%v", files, err)
	}
}

//nolint:paralleltest // newEngineFixture uses t.Setenv
func TestEngineRunReportsCanonicalFetchFailure(t *testing.T) {
	fixture := newEngineFixture(t)
	if err := os.RemoveAll(fixture.repository.CloneURL); err != nil {
		t.Fatal(err)
	}
	results, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, fixture.options())
	if err == nil || len(results) != 1 || results[0].Status != "failed" || !strings.Contains(results[0].Reason, "fetch") {
		t.Fatalf("results=%+v err=%v", results, err)
	}
}
