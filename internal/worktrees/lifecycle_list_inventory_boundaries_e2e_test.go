//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // HOME and XDG_CONFIG_HOME select process-wide read layouts.
func TestE2EListInventoryAdmissionRefusesInvalidSelectionBeforeReadingState(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		prepare    func(*testing.T, *ListOptions)
	}{
		{name: "owner state", want: "unsupported owner state", prepare: func(_ *testing.T, options *ListOptions) {
			options.OwnerState = "retired"
		}},
		{name: "conflicting task selectors", want: "task and tasks cannot be combined", prepare: func(_ *testing.T, options *ListOptions) {
			options.Task, options.Tasks = "one", []string{"two"}
		}},
		{name: "legacy home cycle", want: "too many links", prepare: func(t *testing.T, _ *ListOptions) {
			if _, _, _, _, err := normalizeListOptions(ListOptions{ProjectsRoot: t.TempDir()}); err != nil {
				t.Fatalf("validate ordinary base before cyclic HOME: %v", err)
			}
			loop := filepath.Join(t.TempDir(), "home-loop")
			if err := os.Symlink(loop, loop); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", loop)
		}},
		{name: "configured shared root", want: "must be an absolute path", prepare: func(t *testing.T, _ *ListOptions) {
			mustWriteBranchConfig(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wb", "worktrees.yaml"),
				"version: 1\nworktrees:\n  root: relative-root\n")
		}},
	} {
		//nolint:paralleltest // each case changes the process-wide home or configuration root.
		t.Run(tc.name, func(t *testing.T) {
			projects := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			options := ListOptions{ProjectsRoot: projects, Workers: 1}
			tc.prepare(t, &options)
			if operation, err := newListInventoryOp(context.Background(), options); operation != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("constructor = %+v, %v; want %q", operation, err, tc.want)
			}
			if results, err := List(context.Background(), options); results != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("public inventory = %+v, %v; want %q", results, err, tc.want)
			}
			if _, err := os.Lstat(filepath.Join(projects, ".wb")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refused inventory created private state: %v", err)
			}
		})
	}
}

func TestE2EListCanonicalLocalLayoutKeepsValidSiblingAndReportsLockFailure(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	canonical := newLegacyClone(t, projects, "acme", "app")
	root := filepath.Join(canonical, ".worktrees")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"blocked", "valid"} {
		gitTest(t, canonical, "worktree", "add", "-b", "wb-test/"+task, filepath.Join(root, task), "main")
	}
	head := gitTestOutput(t, canonical, "rev-parse", "HEAD")
	home := t.TempDir()
	layout := wbhome.Layout{Home: home, WorktreesRoot: root, Local: true}
	lockTask := filepath.Join(home, "worktrees", "blocked")
	if err := os.MkdirAll(filepath.Dir(lockTask), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(lockTask, lockTask); err != nil {
		t.Fatal(err)
	}
	results, diagnostics, _, err := listCanonicalLocalLayout(context.Background(), projects, home, layout,
		nil, "main", "", "", false, 1, nil, inspectPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Task != "blocked" ||
		!strings.Contains(diagnostics[0].Message, "inspect authoritative task lock") ||
		!strings.Contains(diagnostics[0].Message, "too many levels of symbolic links") {
		t.Fatalf("task-lock diagnostics = %+v", diagnostics)
	}
	if len(results) != 1 || results[0].Task != "valid" || results[0].WorktreeDir != filepath.Join(root, "valid") {
		t.Fatalf("valid sibling lost after lock refusal: %+v", results)
	}
	if got := gitTestOutput(t, canonical, "rev-parse", "HEAD"); got != head {
		t.Fatalf("inventory changed canonical HEAD: %s -> %s", head, got)
	}
	if results, diagnostics, artifacts, err := listCanonicalLocalLayout(context.Background(), projects, home,
		wbhome.Layout{WorktreesRoot: filepath.Join(root, "absent"), Local: true}, nil,
		"main", "", "", false, 1, nil, inspectPolicy{}); err != nil || len(results) != 0 || len(diagnostics) != 0 || len(artifacts) != 0 {
		t.Fatalf("absent local layout = %+v %+v %+v %v", results, diagnostics, artifacts, err)
	}
}

type lifecycleRebaseTreeFault struct {
	runner.Runner
	revision string
	seen     bool
}

func (fault *lifecycleRebaseTreeFault) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == "git" && len(args) == 4 && args[0] == "-C" && args[2] == "rev-parse" && args[3] == fault.revision+"^{tree}" {
		fault.seen = true
		return runner.Result{}, errors.New("selected rebase source tree observation failed")
	}
	return fault.Runner.RunOpts(ctx, dir, options, name, args...)
}

//nolint:paralleltest // HOME determines the read-only legacy WB layout.
func TestE2ETaskScopedLocalDiscoverySkipsUnreadableOwnerAndKeepsValidSibling(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	const task = "selected-task"
	blocked := filepath.Join(projects, "blocked-owner")
	validRoot := filepath.Join(projects, "valid-owner", "app", ".worktrees")
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(validRoot, task), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	layouts, diagnostics := discoverTaskScopedLocalWorktreeLayouts(projects, map[string]bool{task: true})
	if len(diagnostics) != 0 || len(layouts) != 1 || layouts[0].WorktreesRoot != validRoot {
		t.Fatalf("unreadable owner and valid sibling = %+v, %+v", layouts, diagnostics)
	}
	loop := filepath.Join(t.TempDir(), "projects-loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if layouts, diagnostics := discoverTaskScopedLocalWorktreeLayouts(loop, map[string]bool{task: true}); len(layouts) != 0 || len(diagnostics) != 1 ||
		!strings.Contains(diagnostics[0].Message, "resolve WB home for task-scoped worktrees") ||
		!strings.Contains(diagnostics[0].Message, "too many links") {
		t.Fatalf("home-cycle refusal = %+v, %+v", layouts, diagnostics)
	}
}

func TestE2EClaimedLocalLayoutRefusesIdentityBeforePublishingPlacement(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	claim := workLogClaim{Repository: "acme/app", Task: "selected-task"}
	path := filepath.Join(projects, "acme", "app", ".worktrees", claim.Task)
	if layout, err := claimedLocalWorktreeLayout(projects, path, claim); err != nil || layout.WorktreesRoot != filepath.Dir(path) || !layout.Local {
		t.Fatalf("valid local claim layout = %+v, %v", layout, err)
	}
	for _, corrupted := range []workLogClaim{{Repository: "unqualified", Task: claim.Task}, {Repository: claim.Repository, Task: "../escape"}} {
		if layout, err := claimedLocalWorktreeLayout(projects, path, corrupted); layout.WorktreesRoot != "" || err == nil ||
			!strings.Contains(err.Error(), "invalid repository or task identity") {
			t.Fatalf("invalid claim layout = %+v, %v", layout, err)
		}
	}
	loop := filepath.Join(t.TempDir(), "root-loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if layout, err := claimedLocalWorktreeLayout(loop, path, claim); layout.WorktreesRoot != "" || err == nil ||
		!strings.Contains(err.Error(), "invalid repository or task identity") {
		t.Fatalf("unresolvable projects root admitted local claim = %+v, %v", layout, err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claim classification published checkout: %v", err)
	}
}

func TestE2EReservedStageInventoryRefusesReplacedEntryWithoutReadingOutside(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stage := filepath.Join(root, ".wb-stage-123")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("enumerate stage: %+v, %v", entries, err)
	}
	parked := filepath.Join(root, "parked-stage")
	if err := os.Rename(stage, parked); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "private-evidence")
	if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, stage); err != nil {
		t.Fatal(err)
	}
	artifact, recognized := inspectLifecycleArtifact(context.Background(), root, "", stage, entries[0])
	if !recognized || artifact.Eligible || artifact.Path != stage ||
		!strings.Contains(artifact.Reason, "cannot open reserved WB stage without following links") {
		t.Fatalf("replaced stage authority = %+v, recognized=%t", artifact, recognized)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "unchanged" {
		t.Fatalf("outside evidence changed: %q, %v", raw, err)
	}
	if info, err := os.Lstat(parked); err != nil || !info.IsDir() {
		t.Fatalf("original stage was retired by inspection: %+v, %v", info, err)
	}
}

func TestE2EListInspectionNormalizesZeroWorkersBeforeReportingCandidate(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	missing := filepath.Join(projects, "missing-checkout")
	results, diagnostics := runInspections(context.Background(), []pendingInspect{{task: "missing-task", path: missing}},
		projects, filepath.Join(projects, ".wb"), wbhome.Layout{WorktreesRoot: projects},
		"main", "", "", false, 0, nil, inspectPolicy{})
	if len(results) != 0 || len(diagnostics) != 1 || diagnostics[0].Task != "missing-task" || diagnostics[0].Path != missing {
		t.Fatalf("zero-worker inspection = %+v, %+v", results, diagnostics)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created missing checkout: %v", err)
	}
}

//nolint:paralleltest // Git and gh fixtures change process-wide configuration.
func TestE2ELifecycleGitHubRebaseObservationFailureKeepsTargetAuthority(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "inventory-rebase-fault", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create checkout: %+v, %v", created, err)
	}
	worktree := created[0].WorktreeDir
	layout := wbhome.Layout{Home: fixture.home, WorktreesRoot: filepath.Join(fixture.canonical, ".worktrees"), Local: true}
	valid := lifecycleInspection{ctx: context.Background(), projectsRoot: fixture.projectsRoot, home: fixture.home,
		worktree: worktree, task: "inventory-rebase-fault", layout: layout, base: "main"}
	if err := valid.locate(); err != nil || valid.slug != "acme/app" || valid.canonical != fixture.canonical {
		t.Fatalf("native inspection identity = %+v, %v", valid, err)
	}
	wrongTask := lifecycleInspection{ctx: context.Background(), projectsRoot: fixture.projectsRoot, home: fixture.home,
		worktree: worktree, task: "other-task", layout: layout, base: "main"}
	if err := wrongTask.locate(); err == nil || !strings.Contains(err.Error(), "belongs to task") {
		t.Fatalf("wrong task inspection = %+v, %v", wrongTask, err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "feature.txt")
	gitTest(t, worktree, "commit", "-m", "feature")
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	tree := gitTestOutput(t, fixture.canonical, "rev-parse", head+"^{tree}")
	merge := gitTestOutput(t, fixture.canonical, "commit-tree", tree, "-p", "main", "-m", "rebase source")
	gitTest(t, fixture.canonical, "update-ref", "refs/heads/main", merge)
	gitTest(t, fixture.canonical, "push", "origin", "main")
	installMergedPullRequestFixtureWithMerge(t, head, merge, time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC))
	fault := &lifecycleRebaseTreeFault{Runner: runner.New(), revision: head}
	inspection := lifecycleInspection{
		ctx: withGitRunner(context.Background(), fault), home: fixture.home,
		canonical: fixture.canonical, worktree: worktree, slug: "acme/app", base: "main",
		branch: created[0].Branch, head: head, withGitHub: true,
	}
	err = inspection.checkGitHubIntegration()
	if !fault.seen || err == nil || !strings.Contains(err.Error(), "selected rebase source tree observation failed") ||
		inspection.result.IntegratedAtOrigin || inspection.result.RebaseMergedAtOrigin ||
		inspection.result.RemoteTargetSHA != merge {
		t.Fatalf("rebase observation = %+v, err=%v, selected tree query=%t", inspection.result, err, fault.seen)
	}
	proved := inspection
	proved.ctx = context.Background()
	proved.result = ListResult{}
	if err := proved.checkGitHubIntegration(); err != nil || !proved.result.RebaseMergedAtOrigin || !proved.result.IntegratedAtOrigin {
		t.Fatalf("same immutable receipt without injected read failure = %+v, %v", proved.result, err)
	}
	if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != head {
		t.Fatalf("failed observation changed checkout HEAD: %s -> %s", head, got)
	}
	if got := gitTestOutput(t, fixture.remote, "rev-parse", "refs/heads/main"); got != merge {
		t.Fatalf("failed observation changed remote target: %s -> %s", merge, got)
	}
}
