//go:build e2e

package worktrees

// These native cases pin cleanup inventory's owner selection, admission,
// proof-read, and GitHub observation boundaries. Each refusal checks the
// evidence that must remain unchanged.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

// These cases use independent filesystem roots but are serial because some
// fixture helpers set process-wide HOME/XDG/Git/GitHub environment variables.
//
//nolint:paralleltest // process-wide fixture environment.
func TestE2ECleanupInventoryOwnerSelectionKeepsOnlyExactState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	listed, err := newListInventoryOp(context.Background(), ListOptions{
		ProjectsRoot: root, Task: "selected-task", OwnerState: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	active := ListResult{Task: "selected-task", Repository: "acme/app", WorktreeDir: filepath.Join(root, "active"), OwnerState: "active"}
	orphan := ListResult{Task: "selected-task", Repository: "acme/other", WorktreeDir: filepath.Join(root, "orphan"), OwnerState: "orphaned"}
	listed.outcome.Results = []ListResult{orphan, active}
	listed.mergeClaimedAndSort()
	if len(listed.outcome.Results) != 1 || listed.outcome.Results[0].WorktreeDir != active.WorktreeDir ||
		listed.outcome.Results[0].OwnerState != "active" {
		t.Fatalf("active-only inventory = %#v", listed.outcome.Results)
	}
	listed.options.OwnerState = "orphaned"
	listed.outcome.Results = []ListResult{active, orphan}
	listed.mergeClaimedAndSort()
	if len(listed.outcome.Results) != 1 || listed.outcome.Results[0].WorktreeDir != orphan.WorktreeDir ||
		listed.outcome.Results[0].OwnerState != "orphaned" {
		t.Fatalf("orphan-only inventory = %#v", listed.outcome.Results)
	}
	for _, path := range []string{active.WorktreeDir, orphan.WorktreeDir} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("filter changed or synthesized checkout %s: %v", path, err)
		}
	}
}

//nolint:paralleltest // HOME and XDG_CONFIG_HOME are process-wide.
func TestE2ECleanupConstructorStopsAtExactAdmissionBoundary(t *testing.T) {
	for _, name := range []string{"invalid task", "legacy-home loop", "configured shared root", "logical alias root"} {
		//nolint:paralleltest // each subtest sets process-wide environment.
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)
			options := CleanupOptions{ProjectsRoot: root, Task: "selected-task"}
			want := ""
			switch name {
			case "invalid task":
				options.Task = "../escape"
				want = "must be one safe path segment"
			case "legacy-home loop":
				loop := filepath.Join(t.TempDir(), "loop")
				if err := os.Symlink(loop, loop); err != nil {
					t.Fatal(err)
				}
				t.Setenv("HOME", loop)
				if _, err := normalizeCleanupOptions(options); err != nil {
					t.Fatalf("fixture failed before WB home resolution: %v", err)
				}
			case "configured shared root":
				mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"),
					"version: 1\nworktrees:\n  root: relative-root\n")
				want = "must be an absolute path"
			case "logical alias root":
				blocked := filepath.Join(t.TempDir(), "blocked-root")
				if err := os.WriteFile(blocked, []byte("preserve this root"), 0o600); err != nil {
					t.Fatal(err)
				}
				mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"),
					"version: 1\nworktrees:\n  root: "+blocked+"\n")
				want = "read worktree tasks under " + blocked
			}
			run, err := newCleanupRun(context.Background(), options)
			if run != nil || err == nil {
				t.Fatalf("%s admission = %#v, %v; want exact refusal", name, run, err)
			}
			if name == "legacy-home loop" {
				if !strings.Contains(err.Error(), "EvalSymlinks: too many links") {
					t.Fatalf("legacy-home resolution error = %v, want symlink-cycle refusal", err)
				}
			} else if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s admission error = %v, want %q", name, err, want)
			}
			if _, err := os.Lstat(filepath.Join(root, ".wb")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s admission created private home: %v", name, err)
			}
		})
	}
}

//nolint:paralleltest // constructor/list reads process-wide fixture environment.
func TestE2ECleanupGatherRefusesBlockedBacklogWithoutReport(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	run, err := newCleanupRun(context.Background(), CleanupOptions{ProjectsRoot: root, Task: "selected-task"})
	if err != nil {
		t.Fatal(err)
	}
	home := run.resolution.Write.Home
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(home, "reports")
	wantBytes := []byte("existing private state must remain")
	if err := os.WriteFile(blocked, wantBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	err = run.gatherInventory()
	if err == nil || !strings.Contains(err.Error(), "read lifecycle cleanup backlog") {
		t.Fatalf("backlog boundary = %v, want exact private-directory refusal", err)
	}
	got, err := os.ReadFile(blocked)
	if err != nil || !reflect.DeepEqual(got, wantBytes) {
		t.Fatalf("blocked private state changed: %q, %v", got, err)
	}
	if run.normalized.ReportDir != "" || len(run.backlog) != 0 || len(run.listed.Results) != 0 {
		t.Fatalf("refusal advanced cleanup inventory/report: report=%q backlog=%#v listed=%#v", run.normalized.ReportDir, run.backlog, run.listed.Results)
	}
}

//nolint:paralleltest // configured root and HOME are process-wide fixture values.
func TestE2ECleanupGatherRefusesConfiguredRootReadFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	blocked := filepath.Join(t.TempDir(), "configured-root-file")
	wantBytes := []byte("another owner's root")
	if err := os.WriteFile(blocked, wantBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"),
		"version: 1\nworktrees:\n  root: "+blocked+"\n")
	// AllMerged has no logical-task selector, so construction may admit this
	// configured root; the authoritative inventory must still reject it.
	run, err := newCleanupRun(context.Background(), CleanupOptions{ProjectsRoot: root, AllMerged: true})
	if err != nil {
		t.Fatalf("fixture failed before inventory: %v", err)
	}
	err = run.gatherInventory()
	if err == nil || !strings.Contains(err.Error(), "read worktree tasks under "+blocked) {
		t.Fatalf("configured-root inventory = %v, want exact read failure", err)
	}
	if got, err := os.ReadFile(blocked); err != nil || !reflect.DeepEqual(got, wantBytes) {
		t.Fatalf("configured-root occupant changed: %q, %v", got, err)
	}
	if run.normalized.ReportDir != "" || len(run.listed.Results) != 0 || len(run.backlog) != 0 {
		t.Fatalf("failed inventory advanced cleanup: report=%q listed=%#v backlog=%#v",
			run.normalized.ReportDir, run.listed.Results, run.backlog)
	}
}

//nolint:paralleltest // WB_HOME and configuration are process-wide.
func TestE2ECleanupGatherFiltersBacklogByExactRepositoryAfterSubstringWalk(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	run, err := newCleanupRun(context.Background(), CleanupOptions{
		ProjectsRoot: root, Task: "selected-task", ExactRepository: "acme/app",
	})
	if err != nil {
		t.Fatal(err)
	}
	worktreesRoot := filepath.Join(root, ".worktrees")
	for _, repository := range []string{"app", "app2"} {
		entry := ListResult{
			Task: "selected-task", Repository: "acme/" + repository,
			CanonicalDir: filepath.Join(root, "acme", repository), WorktreesRoot: worktreesRoot,
			WorktreeDir: filepath.Join(worktreesRoot, "selected-task", "acme", repository),
			Branch:      "wb/selected-task", Base: "main", HeadSHA: strings.Repeat("a", 40),
		}
		record := newLifecycleBacklogRecord(root, entry, "removed")
		if err := persistLifecycleBacklog(run.resolution.Write.Home, &record, lifecycleStageRemovingWorktree); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.gatherInventory(); err != nil {
		t.Fatal(err)
	}
	if len(run.backlog) != 1 || run.backlog[0].Repository != "acme/app" ||
		len(run.listed.Results) != 0 || len(run.backlogQuarantine) != 0 {
		t.Fatalf("exact backlog selection = backlog=%#v listed=%#v quarantined=%#v", run.backlog, run.listed.Results, run.backlogQuarantine)
	}
	if _, err := os.Lstat(filepath.Join(root, "acme", "app")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only backlog scan changed canonical path: %v", err)
	}
}

//nolint:paralleltest // Create and the hosted-PR fixture set process-wide environment.
func TestE2ECleanupGatherRefusesUnreadableAcknowledgementBeforeAnyReport(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "ack-read-boundary",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create candidate = %#v, %v", created, err)
	}
	worktree := created[0].WorktreeDir
	if err := os.WriteFile(filepath.Join(worktree, "local.txt"), []byte("not on target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "local.txt")
	gitTest(t, worktree, "commit", "-m", "local candidate")
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	projection, err := readWorkLogProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID,
		"claims", projection.ClaimID+".json")
	claimBefore, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	registrationBefore := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	mainBefore := remoteBranchForTest(t, fixture.canonical, "main")
	installPullRequestResponses(t, "[]", "")
	blocked := filepath.Join(fixture.home, "reports", "worktree-merge")
	if err := os.MkdirAll(filepath.Dir(blocked), 0o700); err != nil {
		t.Fatal(err)
	}
	wantBlocker := []byte("keep this occupant")
	if err := os.WriteFile(blocked, wantBlocker, 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := newCleanupRun(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "ack-read-boundary",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = run.gatherInventory()
	if err == nil || !strings.Contains(err.Error(), "read worktree-merge reports") {
		t.Fatalf("acknowledgement read = %v, want exact reports-directory refusal", err)
	}
	if len(run.listed.Results) != 1 || run.listed.Results[0].WorktreeDir != worktree {
		t.Fatalf("acknowledgement refusal occurred before completed List: %#v", run.listed.Results)
	}
	if got, err := os.ReadFile(blocked); err != nil || !reflect.DeepEqual(got, wantBlocker) {
		t.Fatalf("reports-directory occupant changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(claimPath); err != nil || !reflect.DeepEqual(got, claimBefore) {
		t.Fatalf("immutable claim changed: %q, %v", got, err)
	}
	if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != head {
		t.Fatalf("candidate HEAD changed: %s -> %s", head, got)
	}
	if got := remoteBranchForTest(t, fixture.canonical, "main"); got != mainBefore {
		t.Fatalf("origin/main changed: %s -> %s", mainBefore, got)
	}
	if got := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain"); got != registrationBefore {
		t.Fatalf("Git registration changed: before=%q after=%q", registrationBefore, got)
	}
	if run.normalized.ReportDir != "" {
		t.Fatalf("failed inventory selected a cleanup report: %q", run.normalized.ReportDir)
	}
}

//nolint:paralleltest // fixture and gh helper set process-wide environment.
func TestE2ECleanupGatherRefusesNamespaceReplacedAfterCompletedInspection(t *testing.T) {
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "namespace-read-boundary",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create shared candidate = %#v, %v", created, err)
	}
	worktree := created[0].WorktreeDir
	root := filepath.Join(fixture.home, "worktrees")
	if !strings.HasPrefix(worktree, root+string(filepath.Separator)) {
		t.Fatalf("candidate %s is outside configured shared root %s", worktree, root)
	}
	projection, err := readWorkLogProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID,
		"claims", projection.ClaimID+".json")
	claimBefore, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	registrationBefore := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	mainBefore := remoteBranchForTest(t, fixture.canonical, "main")
	installPullRequestResponses(t, "[]", "")
	backup := root + "-temporarily-moved"
	occupant := []byte("must remain until explicit restore")
	swapped := false
	var swapErr error
	progress := func(event ListProgress) {
		if !event.Done || event.Path != worktree || swapped || swapErr != nil {
			return
		}
		if err := os.Rename(root, backup); err != nil {
			swapErr = err
			return
		}
		swapped = true
		if err := os.WriteFile(root, occupant, 0o600); err != nil {
			swapErr = err
		}
	}
	run, err := newCleanupRun(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "namespace-read-boundary", Progress: progress,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Register restoration before any assertion: even an earlier error must
	// never leave a temporary Git checkout at a changed path.
	t.Cleanup(func() {
		if swapped {
			if err := os.Remove(root); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Errorf("remove temporary namespace occupant: %v", err)
			}
			if err := os.Rename(backup, root); err != nil {
				t.Errorf("restore original task namespace: %v", err)
			}
		}
	})
	err = run.gatherInventory()
	if swapErr != nil || !swapped {
		t.Fatalf("completed candidate inspection did not exchange its root: swapped=%t err=%v inventory=%v", swapped, swapErr, err)
	}
	if err == nil || !strings.Contains(err.Error(), "read worktree tasks under "+root) ||
		len(run.listed.Results) != 1 || run.listed.Results[0].WorktreeDir != worktree {
		t.Fatalf("namespace read must fail after successful List: results=%#v err=%v", run.listed.Results, err)
	}
	if got, err := os.ReadFile(root); err != nil || !reflect.DeepEqual(got, occupant) {
		t.Fatalf("namespace occupant changed: %q, %v", got, err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, root); err != nil {
		t.Fatal(err)
	}
	swapped = false
	if got, err := os.ReadFile(claimPath); err != nil || !reflect.DeepEqual(got, claimBefore) {
		t.Fatalf("immutable claim changed: %q, %v", got, err)
	}
	if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != head {
		t.Fatalf("candidate HEAD changed: %s -> %s", head, got)
	}
	if got := remoteBranchForTest(t, fixture.canonical, "main"); got != mainBefore {
		t.Fatalf("origin/main changed: %s -> %s", mainBefore, got)
	}
	if got := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain"); got != registrationBefore {
		t.Fatalf("Git registration changed: before=%q after=%q", registrationBefore, got)
	}
	if run.normalized.ReportDir != "" {
		t.Fatalf("failed namespace scan selected report: %q", run.normalized.ReportDir)
	}
}

// Three independent Git observation failures use one minimal existing native
// fixture shape. The runner is scoped to each inspection invocation; no global
// Git hooks are added. The fetched main ref and registry must remain intact.
//
//nolint:paralleltest // real Git and hosted PR fixtures set process-wide environment.
func TestE2ELifecycleGitHubInspectionKeepsDistinctFailedObservationsDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation string
		failAt    int
		branch    string
	}{
		{"remote branch object", "cat-file", 1, "feature"},
		{"remote branch ancestry", "merge-base", 1, "feature"},
		{"residual history", "rev-list", 1, ""},
	} {
		//nolint:paralleltest // newGitFixture and gh fixture set global env.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			installPullRequestResponses(t, "[]", "")
			mainHead := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
			branchHead := ""
			head := mainHead
			if tc.branch != "" {
				gitTest(t, fixture.canonical, "checkout", "-b", tc.branch)
				if err := os.WriteFile(filepath.Join(fixture.canonical, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				gitTest(t, fixture.canonical, "add", "feature.txt")
				gitTest(t, fixture.canonical, "commit", "-m", "feature")
				gitTest(t, fixture.canonical, "push", "origin", tc.branch)
				branchHead = gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
				gitTest(t, fixture.canonical, "checkout", "main")
			} else {
				if err := os.WriteFile(filepath.Join(fixture.canonical, "residual.txt"), []byte("not pushed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				gitTest(t, fixture.canonical, "add", "residual.txt")
				gitTest(t, fixture.canonical, "commit", "-m", "residual")
				head = gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
			}
			beforeRegistry := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
			fault := &lifecycleGitObservationFault{Runner: runner.New(), operation: tc.operation, failAt: tc.failAt}
			inspection := lifecycleInspection{
				ctx: withGitRunner(context.Background(), fault), home: fixture.home,
				canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app",
				base: "main", branch: tc.branch, head: head, withGitHub: true,
				policy: inspectPolicy{residueEvidence: tc.branch == "", residueDepth: 3},
			}
			err := inspection.checkGitHubIntegration()
			if fault.seen < tc.failAt || err == nil || !strings.Contains(err.Error(), "selected lifecycle Git observation failed") {
				t.Fatalf("%s phase = %+v, %v; matched calls=%d", tc.name, inspection.result, err, fault.seen)
			}
			if inspection.result.AbsorbedAtOrigin || inspection.result.Landing != nil || inspection.result.MergedPullRequest != nil {
				t.Fatalf("failed observation granted receipt authority: %+v", inspection.result)
			}
			if inspection.result.RemoteTargetSHA != mainHead ||
				(tc.branch != "" && (inspection.result.RemoteHeadSHA != branchHead || !inspection.result.IntegratedAtOrigin)) ||
				(tc.branch == "" && inspection.result.IntegratedAtOrigin) {
				t.Fatalf("%s failed outside the intended post-fetch proof phase: %+v", tc.name, inspection.result)
			}
			if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
				t.Fatalf("failed inspection changed canonical HEAD: %s -> %s", head, got)
			}
			if got := remoteBranchForTest(t, fixture.canonical, "main"); got != mainHead {
				t.Fatalf("failed inspection changed origin/main: %s -> %s", mainHead, got)
			}
			if tc.branch != "" {
				if got := remoteBranchForTest(t, fixture.canonical, tc.branch); got != branchHead {
					t.Fatalf("failed inspection changed origin/%s: %s -> %s", tc.branch, branchHead, got)
				}
			}
			if got := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain"); got != beforeRegistry {
				t.Fatalf("failed inspection changed Git registration: before=%q after=%q", beforeRegistry, got)
			}
		})
	}
}

//nolint:paralleltest // real Git and hosted PR fixtures set process-wide environment.
func TestE2ELifecycleGitHubRecoveredTargetFetchFailurePreservesRecordedTarget(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "branch", "stale-target", "main")
	gitTest(t, fixture.canonical, "push", "origin", "stale-target")
	staleHead := remoteBranchForTest(t, fixture.canonical, "stale-target")
	if err := os.WriteFile(filepath.Join(fixture.canonical, "new-head.txt"), []byte("local-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "new-head.txt")
	gitTest(t, fixture.canonical, "commit", "-m", "new local head")
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	mainHead := remoteBranchForTest(t, fixture.canonical, "main")
	installMergedPullRequestFixture(t, head, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC))
	beforeRegistry := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain")
	fault := &lifecycleGitObservationFault{Runner: runner.New(), operation: "fetch", failAt: 2}
	inspection := lifecycleInspection{
		ctx: withGitRunner(context.Background(), fault), home: fixture.home,
		canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app",
		base: "stale-target", head: head, withGitHub: true,
	}
	err := inspection.checkGitHubIntegration()
	if fault.seen != 2 || err == nil || !strings.Contains(err.Error(), "selected lifecycle Git observation failed") {
		t.Fatalf("replacement-target fetch = %+v, %v; fetch count=%d", inspection.result, err, fault.seen)
	}
	// A failed replacement fetch returns no SHA and overwrites the earlier
	// observation in this in-progress result. It must not commit a widened
	// target or integration decision; the actual remote refs remain intact.
	if inspection.result.RemoteTargetSHA != "" || inspection.base != "stale-target" ||
		inspection.result.RecordedBase != "" || inspection.result.Base != "" ||
		inspection.result.IntegratedAtOrigin || inspection.result.AbsorbedAtOrigin {
		t.Fatalf("failed replacement fetch rewrote target authority: %+v", inspection.result)
	}
	if got := remoteBranchForTest(t, fixture.canonical, "stale-target"); got != staleHead {
		t.Fatalf("recorded target changed: %s -> %s", staleHead, got)
	}
	if got := remoteBranchForTest(t, fixture.canonical, "main"); got != mainHead {
		t.Fatalf("replacement target changed: %s -> %s", mainHead, got)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
		t.Fatalf("canonical HEAD changed: %s -> %s", head, got)
	}
	if got := gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain"); got != beforeRegistry {
		t.Fatalf("failed fetch changed registration: before=%q after=%q", beforeRegistry, got)
	}
}
