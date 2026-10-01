//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type lifecycleGitObservationFault struct {
	runner.Runner
	operation string
	failAt    int
	seen      int
}

func (fault *lifecycleGitObservationFault) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if name == "git" && len(args) > 2 && args[0] == "-C" && args[2] == fault.operation {
		fault.seen++
		if fault.seen == fault.failAt {
			return runner.Result{}, errors.New("selected lifecycle Git observation failed")
		}
	}
	return fault.Runner.RunOpts(ctx, dir, options, name, args...)
}

//nolint:paralleltest // Native Git fixtures configure process-wide Git state.
func TestE2ELifecycleLocalDiscoveryRejectsLinkedCloneAndUnreadableBoundaries(t *testing.T) {
	projects := t.TempDir()
	canonical := newLegacyClone(t, projects, "acme", "app")
	linked := filepath.Join(projects, "acme", "linked")
	gitTest(t, canonical, "worktree", "add", "-b", "wb-test/linked", linked, "main")
	for _, repository := range []string{canonical, linked} {
		if err := os.Mkdir(filepath.Join(repository, ".worktrees"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, repository := range []string{"broken-b", "broken-a"} {
		path := filepath.Join(projects, "acme", repository)
		if err := os.MkdirAll(filepath.Join(path, ".worktrees"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	layouts, diagnostics := discoverCanonicalLocalWorktreeLayouts(context.Background(), projects, "")
	if len(layouts) != 1 || layouts[0].WorktreesRoot != filepath.Join(canonical, ".worktrees") || !layouts[0].Local {
		t.Fatalf("canonical local layouts = %+v; linked clone must be refused", layouts)
	}
	wantPaths := []string{filepath.Join(projects, "acme", "broken-a"), filepath.Join(projects, "acme", "broken-b")}
	gotPaths := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		if !strings.Contains(diagnostic.Message, "verify canonical local Git identity") {
			t.Fatalf("unexpected discovery diagnostic: %+v", diagnostic)
		}
		gotPaths = append(gotPaths, diagnostic.Path)
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("sorted diagnostics = %v, want %v", gotPaths, wantPaths)
	}
}

func TestE2ELogicalCleanupRefusesUnreadableTaskRootAndSkipsHiddenRepositories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fileRoot := filepath.Join(root, "not-directory")
	if err := os.WriteFile(fileRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveLogicalCleanupTasks([]wbhome.Layout{{WorktreesRoot: fileRoot}}, []string{"logical"}); err == nil || !strings.Contains(err.Error(), "read worktree tasks under") || got != nil {
		t.Fatalf("file-shaped task root = %v, %v", got, err)
	}
	physical := "session-resume-hidden-repository"
	taskRoot := filepath.Join(root, physical)
	for _, path := range []string{filepath.Join(taskRoot, ".hidden-owner", "repo"), filepath.Join(taskRoot, "acme", ".hidden-repository")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := resolveLogicalCleanupTasks([]wbhome.Layout{{WorktreesRoot: root}}, []string{"logical"})
	if err != nil || !reflect.DeepEqual(got, []string{"logical"}) {
		t.Fatalf("hidden repository supplied cleanup alias: %v, %v", got, err)
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		if err := os.Chmod(taskRoot, 0); err != nil {
			t.Fatal(err)
		}
		got, err = resolveLogicalCleanupTasks([]wbhome.Layout{{WorktreesRoot: root}}, []string{"logical"})
		if restoreErr := os.Chmod(taskRoot, 0o700); restoreErr != nil {
			t.Fatal(restoreErr)
		}
		if got != nil || err == nil || !strings.Contains(err.Error(), "read session-resume task "+physical) {
			t.Fatalf("unreadable session task = %v, %v", got, err)
		}
		owner := filepath.Join(taskRoot, "acme")
		if err := os.Chmod(owner, 0); err != nil {
			t.Fatal(err)
		}
		got, err = resolveLogicalCleanupTasks([]wbhome.Layout{{WorktreesRoot: root}}, []string{"logical"})
		if restoreErr := os.Chmod(owner, 0o700); restoreErr != nil {
			t.Fatal(restoreErr)
		}
		if got != nil || err == nil || !strings.Contains(err.Error(), "read session-resume task "+physical+" owner") {
			t.Fatalf("unreadable session owner = %v, %v", got, err)
		}
	}
}

func TestE2ELifecycleDiscoveryDiagnosesUnreadableOwnerAndRepositoryRoot(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory search permissions")
	}
	projects := t.TempDir()
	owner := filepath.Join(projects, "blocked")
	canonical := filepath.Join(projects, "acme", "app")
	if err := os.MkdirAll(filepath.Join(canonical, ".worktrees"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(owner, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{owner, canonical} {
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
	}
	layouts, diagnostics := discoverCanonicalLocalWorktreeLayouts(context.Background(), projects, "")
	for _, path := range []string{owner, canonical} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if len(layouts) != 0 || len(diagnostics) != 2 ||
		diagnostics[0].Path != filepath.Join(canonical, ".worktrees") ||
		!strings.Contains(diagnostics[0].Message, "inspect canonical local worktrees root") ||
		diagnostics[1].Path != owner || !strings.Contains(diagnostics[1].Message, "read canonical owner directory") {
		t.Fatalf("unreadable canonical hierarchy = layouts=%+v diagnostics=%+v", layouts, diagnostics)
	}
}

func TestE2EClaimedRegistryReportsUnreadableCanonicalInventory(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	fileRoot := filepath.Join(projects, "not-directory")
	if err := os.WriteFile(fileRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	results, diagnostics := listClaimedRegistryWorktrees(context.Background(), fileRoot, filepath.Join(projects, ".wb"),
		nil, nil, "main", "", "", false, 1, nil, inspectPolicy{})
	if len(results) != 0 || len(diagnostics) != 1 || diagnostics[0].Path == "" ||
		!strings.Contains(diagnostics[0].Message, "cannot inspect canonical Git registry") {
		t.Fatalf("unreadable canonical inventory = results=%+v diagnostics=%+v", results, diagnostics)
	}
}

func TestE2ETaskScopedClaimScanRefusesMisboundAndSymlinkedRecords(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "task", "acme", "app")
	claims := filepath.Join(home, "worklogs", "task", "runs", "run", "claims")
	root := workLogClaim{Version: 2, EffortID: "task", RunID: "run", Task: "task", Repository: "acme/app",
		Worktree: worktree, Branch: "task", Base: "main", BaseSHA: strings.Repeat("a", 40),
		Lifecycle: "active", Model: "unknown", ModelProvenance: modelProvenanceUnknown}
	var err error
	root.ClaimID, err = expectedWorkLogClaimID(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	wtLifeCovWriteJSON(t, outside, root)
	if err := os.MkdirAll(claims, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(claims, root.ClaimID+".json")
	if err := os.Symlink(outside, entry); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	read := func() ([]ListResult, []ListDiagnostic) {
		return listTaskScopedClaimedRegistryWorktrees(context.Background(), t.TempDir(), home, nil,
			map[string]bool{"task": true}, "main", "", "", false, 1, nil, inspectPolicy{})
	}
	if results, diagnostics := read(); len(results) != 0 || len(diagnostics) != 0 {
		t.Fatalf("symlinked claim acquired managed placement: %+v %+v", results, diagnostics)
	}
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	misbound := root
	misbound.Branch = "other-task"
	misbound.ClaimID, err = expectedWorkLogClaimID(misbound)
	if err != nil {
		t.Fatal(err)
	}
	wtLifeCovWriteJSON(t, entry, misbound)
	if claim, err := activeWorkLogClaimAtPath(home, worktree, nil); claim != nil || err == nil ||
		!strings.Contains(err.Error(), "entry identity mismatch") {
		t.Fatalf("missing-checkout reader accepted mismatched claim filename: %+v %v", claim, err)
	}
	if results, diagnostics := read(); len(results) != 0 || len(diagnostics) != 0 {
		t.Fatalf("misbound claim acquired managed placement: %+v %+v", results, diagnostics)
	}
}

func TestE2EMissingCheckoutClaimReaderClosesEveryHeldDirectoryOnReadFailure(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "task", "acme", "app")
	claim := workLogClaim{Version: 2, EffortID: "task", RunID: "run", Task: "task", Repository: "acme/app",
		Worktree: worktree, Branch: "task", Base: "main", BaseSHA: strings.Repeat("a", 40),
		Lifecycle: "active", Model: "unknown", ModelProvenance: modelProvenanceUnknown}
	var err error
	claim.ClaimID, err = expectedWorkLogClaimID(claim)
	if err != nil {
		t.Fatal(err)
	}
	wtLifeCovWriteJSON(t, filepath.Join(home, "worklogs", "task", "runs", "run", "claims", claim.ClaimID+".json"), claim)
	for failAt := 1; failAt <= 3; failAt++ {
		var visited []*os.File
		readNames := func(directory *os.File) ([]string, error) {
			visited = append(visited, directory)
			if len(visited) == failAt {
				return nil, errors.New("selected held-directory read failed")
			}
			return directory.Readdirnames(-1)
		}
		found, err := activeWorkLogClaimAtPathWithReadNames(home, worktree, nil, readNames)
		if found != nil || err == nil || !strings.Contains(err.Error(), "selected held-directory read failed") || len(visited) != failAt {
			t.Fatalf("read failure at held directory %d = found=%+v err=%v visits=%d", failAt, found, err, len(visited))
		}
		for _, directory := range visited {
			if _, statErr := directory.Stat(); !errors.Is(statErr, os.ErrClosed) {
				t.Fatalf("held directory remained open after read failure at %d: %v", failAt, statErr)
			}
		}
	}
}

func TestE2ETaskScopedRegistrySeparatesMissingTerminalFilterAndMetadata(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	projects := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "task", "acme", "app")
	claim := workLogClaim{Version: 2, EffortID: "task", RunID: "run", Task: "task", Repository: "acme/app",
		Worktree: worktree, Branch: "task", Base: "main", BaseSHA: strings.Repeat("a", 40),
		Lifecycle: "active", Model: "unknown", ModelProvenance: modelProvenanceUnknown}
	var err error
	claim.ClaimID, err = expectedWorkLogClaimID(claim)
	if err != nil {
		t.Fatal(err)
	}
	claims := filepath.Join(home, "worklogs", "task", "runs", "run", "claims")
	wtLifeCovWriteJSON(t, filepath.Join(claims, claim.ClaimID+".json"), claim)
	read := func(filter string) ([]ListResult, []ListDiagnostic) {
		return listTaskScopedClaimedRegistryWorktrees(context.Background(), projects, home, nil,
			map[string]bool{"task": true}, "main", filter, "", false, 1, nil, inspectPolicy{})
	}
	if results, diagnostics := read(""); len(results) != 0 || len(diagnostics) != 1 || diagnostics[0].Path != worktree ||
		!strings.Contains(diagnostics[0].Message, "working tree is missing") {
		t.Fatalf("missing managed checkout = results=%+v diagnostics=%+v", results, diagnostics)
	}
	if results, diagnostics := read("different-repository"); len(results) != 0 || len(diagnostics) != 0 {
		t.Fatalf("filter admitted unrelated missing checkout: %+v %+v", results, diagnostics)
	}
	terminal := filepath.Join(home, "worklogs", "task", "runs", "run", "terminals", claim.ClaimID+".json")
	terminalClaim := claim
	terminalClaim.Lifecycle = "terminal"
	wtLifeCovWriteJSON(t, terminal, workLogTerminalRecord{Claim: terminalClaim,
		FinalCommit: claim.BaseSHA, Disposition: "landed", SealedAt: time.Unix(123, 0).UTC()})
	if results, diagnostics := read(""); len(results) != 0 || len(diagnostics) != 0 {
		t.Fatalf("terminalized checkout retained missing-active diagnostic: %+v %+v", results, diagnostics)
	}
	if err := os.Remove(terminal); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if results, diagnostics := read(""); len(results) != 0 || len(diagnostics) != 0 {
		t.Fatalf("non-Git checkout acquired managed placement: %+v %+v", results, diagnostics)
	}
	if err := os.Remove(filepath.Join(claims, claim.ClaimID+".json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(claims); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claims, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if results, diagnostics := read(""); len(results) != 0 || len(diagnostics) != 1 ||
		!strings.Contains(diagnostics[0].Message, "read task Work Log claims") {
		t.Fatalf("unreadable claims directory was not diagnosed: %+v %+v", results, diagnostics)
	}
}

func TestE2ETaskScopedRegistrySkipsMissingClaimsAndRefusesLinkedStore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := filepath.Join(root, "real-store")
	home := filepath.Join(root, "home")
	worktree := filepath.Join(root, "shared", "task", "acme", "app")
	claim := workLogClaim{Version: 2, EffortID: "task", RunID: "run-two", Task: "task", Repository: "acme/app",
		Worktree: worktree, Branch: "task", Base: "main", BaseSHA: strings.Repeat("a", 40),
		Lifecycle: "active", Model: "unknown", ModelProvenance: modelProvenanceUnknown}
	var err error
	claim.ClaimID, err = expectedWorkLogClaimID(claim)
	if err != nil {
		t.Fatal(err)
	}
	missingClaimsRun := filepath.Join(store, "worklogs", "task", "runs", "run-one")
	if err := os.MkdirAll(missingClaimsRun, 0o700); err != nil {
		t.Fatal(err)
	}
	wtLifeCovWriteJSON(t, filepath.Join(store, "worklogs", "task", "runs", "run-two", "claims", claim.ClaimID+".json"), claim)
	read := func(home string) ([]ListResult, []ListDiagnostic) {
		return listTaskScopedClaimedRegistryWorktrees(context.Background(), root, home, nil,
			map[string]bool{"task": true}, "main", "", "", false, 1, nil, inspectPolicy{})
	}
	if results, diagnostics := read(store); len(results) != 0 || len(diagnostics) != 1 || diagnostics[0].Path != worktree ||
		!strings.Contains(diagnostics[0].Message, "working tree is missing") {
		t.Fatalf("missing claims hid later valid run: %+v %+v", results, diagnostics)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(store, "worklogs"), filepath.Join(home, "worklogs")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if results, diagnostics := read(home); len(results) != 0 || len(diagnostics) != 0 {
		t.Fatalf("linked private store supplied managed authority: %+v %+v", results, diagnostics)
	}
}

//nolint:paralleltest // Create and native Git fixtures configure process-wide WB state.
func TestE2EClaimedRegistrySeparatesLockAndMissingClaimEvidence(t *testing.T) {
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"),
		"version: 1\nworktrees:\n  root: "+filepath.Join(fixture.home, "physical-worktrees")+"\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "registry-remaining", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create shared checkout: %+v, %v", created, err)
	}
	path := created[0].WorktreeDir
	claim, _, _, err := activeWorkLogClaim(fixture.home, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(path); err != nil {
		t.Fatalf("the registered checkout must retain independent manifest evidence: %v", err)
	}
	inspect := func() ([]ListResult, []ListDiagnostic) {
		return listClaimedRegistryWorktrees(context.Background(), fixture.projectsRoot, fixture.home,
			map[string]bool{}, nil, "main", "", "", false, 1, nil, inspectPolicy{})
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		lockTaskRoot := filepath.Join(fixture.home, "worktrees", claim.Task)
		if err := os.MkdirAll(lockTaskRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(lockTaskRoot, 0); err != nil {
			t.Fatal(err)
		}
		_, diagnostics := inspect()
		_, scopedDiagnostics := listTaskScopedClaimedRegistryWorktrees(context.Background(), fixture.projectsRoot, fixture.home,
			map[string]bool{}, map[string]bool{claim.Task: true}, "main", "", "", false, 1, nil, inspectPolicy{})
		if err := os.Chmod(lockTaskRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if len(diagnostics) != 1 || diagnostics[0].Path != path ||
			!strings.Contains(diagnostics[0].Message, "inspect authoritative task lock") {
			t.Fatalf("unreadable task-lock parent was not diagnosed: %+v", diagnostics)
		}
		if len(scopedDiagnostics) != 1 || scopedDiagnostics[0].Path != path ||
			!strings.Contains(scopedDiagnostics[0].Message, "inspect authoritative task lock") {
			t.Fatalf("task-scoped reader ignored unreadable task lock: %+v", scopedDiagnostics)
		}
	}
	if results, diagnostics := listClaimedRegistryWorktrees(context.Background(), fixture.projectsRoot, fixture.home,
		map[string]bool{}, nil, "main", fixture.canonical, "", false, 1, nil, inspectPolicy{}); len(results) != 0 || len(diagnostics) != 0 {
		t.Fatalf("canonical-path filter inspected unrelated physical checkout: results=%+v diagnostics=%+v", results, diagnostics)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims", claim.ClaimID+".json")
	if err := os.Remove(claimPath); err != nil {
		t.Fatal(err)
	}
	results, diagnostics := inspect()
	if len(results) != 0 || len(diagnostics) != 1 || diagnostics[0].Path != path ||
		!strings.Contains(diagnostics[0].Message, "corroborate managed registry worktree claim") {
		t.Fatalf("manifest did not make missing claim material: results=%+v diagnostics=%+v", results, diagnostics)
	}
}

//nolint:paralleltest // Native Create and Git fixture configuration use process-wide state.
func TestE2EClaimedRegistryRechecksQueuedClaimBeforeInspection(t *testing.T) {
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"),
		"version: 1\nworktrees:\n  root: "+filepath.Join(fixture.home, "old-shared-root")+"\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "queued-claim-recheck", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create queued managed member: %+v, %v", created, err)
	}
	path := created[0].WorktreeDir
	for _, tc := range []struct {
		name, want string
		changed    bool
	}{
		{name: "second claim read fails", want: "re-read managed registry claim"},
		{name: "second claim changes custody", want: "adopted worktree claim requires external registration", changed: true},
	} {
		//nolint:paralleltest // The subtests share a native Git fixture whose parent sets process-wide environment.
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			readClaim := func(home, worktree string) (workLogClaim, workLogProjection, string, error) {
				calls++
				claim, projection, source, readErr := activeWorkLogClaim(home, worktree)
				if readErr != nil || calls != 2 {
					return claim, projection, source, readErr
				}
				if tc.changed {
					claim.AcquiredVia = "adopted"
					return claim, projection, source, nil
				}
				return workLogClaim{}, workLogProjection{}, "", errors.New("selected second claim read failed")
			}
			results, diagnostics := listClaimedRegistryWorktreesWithClaimReader(context.Background(), fixture.projectsRoot,
				fixture.home, map[string]bool{}, nil, "main", "", "", false, 1, nil, inspectPolicy{}, readClaim)
			if calls != 2 || len(results) != 0 || len(diagnostics) != 1 || diagnostics[0].Path != path ||
				!strings.Contains(diagnostics[0].Message, tc.want) {
				t.Fatalf("%s = calls=%d results=%+v diagnostics=%+v", tc.name, calls, results, diagnostics)
			}
		})
	}
}

//nolint:paralleltest // Native Create and Git fixture configuration use process-wide state.
func TestE2EClaimedRegistryLeavesRepositoryLocalMemberToItsOwningWalk(t *testing.T) {
	fixture := newGitFixture(t)
	configureFixtureRepositoryLocalWorktrees(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "local-registry-owner", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 || created[0].WorktreeDir != filepath.Join(fixture.canonical, ".worktrees", "local-registry-owner") {
		t.Fatalf("create repository-local member: %+v, %v", created, err)
	}
	results, diagnostics := listClaimedRegistryWorktrees(context.Background(), fixture.projectsRoot, fixture.home,
		map[string]bool{}, nil, "main", "", "", false, 1, nil, inspectPolicy{})
	if len(results) != 0 || len(diagnostics) != 0 {
		t.Fatalf("registry walk duplicated local member: %+v %+v", results, diagnostics)
	}
}

//nolint:paralleltest // Native Git and adoption configure process-wide fixture state.
func TestE2EClaimedRegistryDoesNotReclassifyAdoptedExternalCheckout(t *testing.T) {
	fixture := newGitFixture(t)
	path := filepath.Join(filepath.Dir(fixture.projectsRoot), "external", "registry-external-checkout")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/registry-external", path, "main")
	adopted, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Path: path, Apply: true})
	if err != nil || len(adopted) != 1 || adopted[0].Action != AdoptAdopted {
		t.Fatalf("adopt external registered checkout: %+v, %v", adopted, err)
	}
	claim, _, _, err := activeWorkLogClaim(fixture.home, path)
	if err != nil || claim.Worktree != path {
		t.Fatalf("adopted claim = %+v, %v", claim, err)
	}
	results, diagnostics := listClaimedRegistryWorktrees(context.Background(), fixture.projectsRoot, fixture.home,
		map[string]bool{}, nil, "main", "", "", false, 1, nil, inspectPolicy{})
	if len(results) != 0 || len(diagnostics) != 1 || diagnostics[0].Task != claim.Task || diagnostics[0].Path != path ||
		!strings.Contains(diagnostics[0].Message, "adopted worktree claim requires external registration") {
		t.Fatalf("external adoption became a shared checkout: results=%+v diagnostics=%+v", results, diagnostics)
	}
}

//nolint:paralleltest // Native Git and adoption configure process-wide fixture state.
func TestE2EClaimedRegistryDoesNotReclassifyShapedExternalAfterPointerLoss(t *testing.T) {
	fixture := newGitFixture(t)
	path := fixture.externalWorktree(t, "feature/registry-shaped-external")
	adopted, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Path: path, Apply: true})
	if err != nil || len(adopted) != 1 || adopted[0].Action != AdoptAdopted {
		t.Fatalf("adopt shaped external checkout: %+v, %v", adopted, err)
	}
	listed, err := List(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: adopted[0].Task})
	if err != nil || len(listed) != 1 || !listed[0].External || listed[0].WorktreeDir != path {
		t.Fatalf("intact pointer did not retain external classification: %+v, %v", listed, err)
	}
	head := gitTestOutput(t, path, "rev-parse", "HEAD")
	if err := transferWorkLogClaim(fixture.home, path, head, "handoff", "next-run", ClaimExecutionIdentity{Model: "unknown"}); err != nil {
		t.Fatalf("transfer adopted claim before pointer loss: %v", err)
	}
	successor, _, _, err := activeWorkLogClaim(fixture.home, path)
	if err != nil || successor.ParentClaimID == "" || successor.AcquiredVia != "handoff" {
		t.Fatalf("transferred adopted claim = %+v, %v", successor, err)
	}
	registration := filepath.Join(fixture.home, "worktrees", adopted[0].Task, "acme", "app", adoptedWorktreePointerName)
	if err := os.Remove(registration); err != nil {
		t.Fatal(err)
	}
	outcome, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: adopted[0].Task})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range outcome.Results {
		if result.WorktreeDir == path && !result.External {
			t.Fatalf("lost adoption pointer reclassified external checkout as managed: %+v; diagnostics=%+v", result, outcome.Diagnostics)
		}
	}
	unscoped, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range unscoped.Results {
		if result.WorktreeDir == path && !result.External {
			t.Fatalf("unscoped inventory reclassified external checkout: %+v", result)
		}
	}
	foundRefusal := false
	for _, diagnostic := range unscoped.Diagnostics {
		if diagnostic.Path == path && strings.Contains(diagnostic.Message, "adopted worktree claim requires external registration") {
			foundRefusal = true
		}
	}
	if !foundRefusal {
		t.Fatalf("lost adoption pointer lacked claim-based refusal: %+v", unscoped.Diagnostics)
	}
	orphans, err := Orphans(context.Background(), OrphanOptions{ProjectsRoot: fixture.projectsRoot, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	foundExternal := false
	for _, family := range orphans.Families {
		for _, worktree := range family.Worktrees {
			if worktree.Path == path {
				foundExternal = worktree.Layout == LayoutExternal
			}
		}
	}
	if !foundExternal {
		t.Fatalf("orphan inventory lost adopted lineage after pointer loss: %+v", orphans.Families)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	missing, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatal(err)
	}
	foundMissingRefusal := false
	for _, diagnostic := range missing.Diagnostics {
		if diagnostic.Path == path && strings.Contains(diagnostic.Message, "adopted worktree claim requires external registration") {
			foundMissingRefusal = true
		}
		if diagnostic.Path == path && strings.Contains(diagnostic.Message, "WB-managed worktree") {
			t.Fatalf("missing adopted checkout was described as managed: %+v", diagnostic)
		}
	}
	if !foundMissingRefusal {
		t.Fatalf("missing adopted checkout lacked lineage refusal: %+v", missing.Diagnostics)
	}
}

func TestE2EClaimedSharedLayoutRejectsInvalidClaimIdentity(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "task", "acme", "app")
	for _, claim := range []workLogClaim{
		{Task: "task", Repository: "unqualified"},
		{Task: "bad task", Repository: "acme/app"},
	} {
		if layout, err := claimedSharedWorktreeLayout(path, claim); err == nil ||
			!strings.Contains(err.Error(), "invalid repository or task identity") || layout != (wbhome.Layout{}) {
			t.Fatalf("invalid claimed layout %+v = %+v, %v", claim, layout, err)
		}
	}
}

func TestE2EClaimedSharedLineageRefusesMissingCyclicAndInvalidPredecessors(t *testing.T) {
	t.Parallel()
	const effort, run = "task", "run"
	for _, tc := range []struct{ name, want string }{
		{name: "missing parent"},
		{name: "mismatched parent identity", want: "predecessor identity mismatch"},
		{name: "cyclic parent", want: "predecessor cycle"},
		{name: "different placement", want: "predecessor placement mismatch"},
		{name: "invalid parent digest", want: "digest mismatch"},
		{name: "cross-custody parent", want: "unsupported root Work Log predecessor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			claims := filepath.Join(home, "worklogs", effort, "runs", run, "claims")
			if err := os.MkdirAll(claims, 0o700); err != nil {
				t.Fatal(err)
			}
			parent := workLogClaim{Version: 2, EffortID: effort, RunID: run, Task: effort, Repository: "acme/app",
				Worktree: filepath.Join(home, "task", "acme", "app"), Branch: "task", Base: "main",
				BaseSHA: strings.Repeat("b", 40), Lifecycle: "active",
				Model: "unknown", ModelProvenance: modelProvenanceUnknown}
			parent.ClaimID, _ = expectedWorkLogClaimID(parent)
			current := newSuccessorWorkLogClaim(parent,
				declaredSuccessorWorkLogClaimID(parent.ClaimID, "agent", "handoff", ClaimExecutionIdentity{Model: "unknown"}),
				time.Now().UTC(), "agent", "handoff", ClaimExecutionIdentity{Model: "unknown"})
			if tc.name != "missing parent" {
				if tc.name == "mismatched parent identity" {
					parent.ClaimID = strings.Repeat("c", 64)
				}
				if tc.name == "cyclic parent" {
					parent.ParentClaimID = current.ClaimID
				}
				if tc.name == "different placement" {
					parent.Worktree = filepath.Join(home, "unrelated", "acme", "app")
					parent.ClaimID, _ = expectedWorkLogClaimID(parent)
					current.ParentClaimID = parent.ClaimID
					current.ClaimID = declaredSuccessorWorkLogClaimID(parent.ClaimID, "agent", "handoff", ClaimExecutionIdentity{Model: "unknown"})
				}
				if tc.name == "invalid parent digest" {
					parent.Repository = "other/app"
				}
				if tc.name == "cross-custody parent" {
					parent.AcquiredVia = "external_handoff"
				}
				wtLifeCovWriteJSON(t, filepath.Join(claims, current.ParentClaimID+".json"), parent)
			}
			adopted, err := claimLineageWasAdopted(home, current)
			if adopted || err == nil || (tc.want == "" && !errors.Is(err, os.ErrNotExist)) ||
				(tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("%s lineage = adopted=%t err=%v, want refusal %q", tc.name, adopted, err, tc.want)
			}
		})
	}
	invalid := workLogClaim{ClaimID: strings.Repeat("a", 64), EffortID: effort, RunID: run, AcquiredVia: "adopted"}
	if adopted, err := claimLineageWasAdopted(t.TempDir(), invalid); adopted || err == nil || !strings.Contains(err.Error(), "identity metadata is invalid") {
		t.Fatalf("invalid current claim granted adopted provenance: %t, %v", adopted, err)
	}
	if _, err := claimedSharedWorktreeLayoutFromClaim(t.TempDir(), invalid.Worktree, invalid); err == nil ||
		!strings.Contains(err.Error(), "verify managed registry claim lineage") {
		t.Fatalf("invalid current claim acquired managed layout: %v", err)
	}
	root := workLogClaim{Version: 2, EffortID: effort, RunID: run, Task: effort, Repository: "acme/app",
		Worktree: filepath.Join(t.TempDir(), effort, "acme", "app"), Branch: effort, Base: "main",
		BaseSHA: strings.Repeat("b", 40), Lifecycle: "active", Model: "unknown", ModelProvenance: modelProvenanceUnknown}
	root.ClaimID, _ = expectedWorkLogClaimID(root)
	legacy := root
	legacy.AcquiredVia = legacyMissingClaimRecoveryType
	if layout, err := claimedSharedWorktreeLayoutFromClaim(t.TempDir(), legacy.Worktree, legacy); err != nil ||
		layout.WorktreesRoot != filepath.Dir(filepath.Dir(filepath.Dir(legacy.Worktree))) {
		t.Fatalf("reconstructed legacy root lost managed placement: %+v, %v", layout, err)
	}
	for _, acquiredVia := range []string{"external_handoff", "parked_session_resume"} {
		malformedRoot := root
		malformedRoot.AcquiredVia = acquiredVia
		if adopted, err := claimLineageWasAdopted(t.TempDir(), malformedRoot); adopted || err == nil ||
			!strings.Contains(err.Error(), "unsupported root") {
			t.Fatalf("parentless %s claim granted managed lineage: %t, %v", acquiredVia, adopted, err)
		}
	}
	successor := newSuccessorWorkLogClaim(root,
		declaredSuccessorWorkLogClaimID(root.ClaimID, "agent", "handoff", ClaimExecutionIdentity{Model: "unknown"}),
		time.Now().UTC(), "agent", "handoff", ClaimExecutionIdentity{Model: "unknown"})
	ancestorHome := t.TempDir()
	ancestor := successor
	descendant := newSuccessorWorkLogClaim(ancestor,
		declaredSuccessorWorkLogClaimID(ancestor.ClaimID, "next-agent", "handoff", ClaimExecutionIdentity{Model: "unknown"}),
		time.Now().UTC(), "next-agent", "handoff", ClaimExecutionIdentity{Model: "unknown"})
	ancestor.AcquiredVia = "external_handoff"
	wtLifeCovWriteJSON(t, filepath.Join(ancestorHome, "worklogs", effort, "runs", run, "claims", ancestor.ClaimID+".json"), ancestor)
	if adopted, err := claimLineageWasAdopted(ancestorHome, descendant); adopted || err == nil ||
		!strings.Contains(err.Error(), "cross-custody Work Log predecessor") {
		t.Fatalf("cross-custody ancestor granted managed lineage: %t, %v", adopted, err)
	}
	if adopted, err := claimLineageWasAdopted(t.TempDir(), successor); adopted || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing private run granted managed lineage: %t, %v", adopted, err)
	}
	partialHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(partialHome, "worklogs", effort, "runs", run), 0o700); err != nil {
		t.Fatal(err)
	}
	if adopted, err := claimLineageWasAdopted(partialHome, successor); adopted || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing claims directory granted managed lineage: %t, %v", adopted, err)
	}
	successor.AcquiredVia = "external_handoff"
	if adopted, err := claimLineageWasAdopted(partialHome, successor); adopted || err == nil ||
		!strings.Contains(err.Error(), "cross-custody") {
		t.Fatalf("cross-custody successor granted managed lineage: %t, %v", adopted, err)
	}
}

//nolint:paralleltest // Native Git and Create configure process-wide fixture state.
func TestE2EClaimedRegistryPreservesCreatedSuccessorAfterStoreChange(t *testing.T) {
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	oldRoot := filepath.Join(fixture.home, "old-shared-root")
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+oldRoot+"\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "created-successor", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create managed checkout: %+v, %v", created, err)
	}
	path := created[0].WorktreeDir
	head := gitTestOutput(t, path, "rev-parse", "HEAD")
	if err := transferWorkLogClaim(fixture.home, path, head, "handoff", "next-run", ClaimExecutionIdentity{Model: "unknown"}); err != nil {
		t.Fatalf("transfer created claim: %v", err)
	}
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"),
		"version: 1\nworktrees:\n  root: "+filepath.Join(fixture.home, "new-shared-root")+"\n")
	listed, err := List(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "created-successor"})
	if err != nil || len(listed) != 1 || listed[0].WorktreeDir != path || listed[0].External || listed[0].WorktreesRoot != oldRoot {
		t.Fatalf("changed-root created successor recovery = %+v, %v", listed, err)
	}
	if layout := orphanLayoutOf(context.Background(), fixture.home, canonicalClone{path: fixture.canonical}, path,
		filepath.Join(fixture.home, "new-shared-root"), ""); layout != LayoutShared {
		t.Fatalf("created successor lost shared-root orphan provenance: %q", layout)
	}
}

//nolint:paralleltest // Native Git and hosted-PR fixtures set process-wide environment.
func TestE2ELifecycleGitHubProofStopsAtFailedGitObservations(t *testing.T) {
	for _, tc := range []struct {
		name, operation, branch string
		failAt                  int
	}{
		{name: "initial target containment", operation: "merge-base", failAt: 1},
		{name: "final target containment", operation: "merge-base", failAt: 2},
		{name: "remote source branch", operation: "ls-remote", branch: "feature", failAt: 1},
	} {
		//nolint:paralleltest // each native fixture changes process-wide Git and gh environment.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			installPullRequestResponses(t, "[]", "")
			if tc.operation == "merge-base" {
				if err := os.WriteFile(filepath.Join(fixture.canonical, "unpublished.txt"), []byte("not on origin/main\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				gitTest(t, fixture.canonical, "add", "unpublished.txt")
				gitTest(t, fixture.canonical, "commit", "-m", "unpublished source")
			}
			head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
			fault := &lifecycleGitObservationFault{Runner: runner.New(), operation: tc.operation, failAt: tc.failAt}
			inspection := lifecycleInspection{
				ctx: withGitRunner(context.Background(), fault), home: fixture.home,
				canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app",
				base: "main", branch: tc.branch, head: head, withGitHub: true,
			}
			err := inspection.checkGitHubIntegration()
			if fault.seen < tc.failAt || err == nil || !strings.Contains(err.Error(), "selected lifecycle Git observation failed") {
				t.Fatalf("%s proof = %+v, %v; matched Git calls = %d", tc.name, inspection.result, err, fault.seen)
			}
			if inspection.result.RemoteTargetSHA != gitTestOutput(t, fixture.remote, "rev-parse", "refs/heads/main") {
				t.Fatalf("%s lost exact fetched target: %+v", tc.name, inspection.result)
			}
			if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
				t.Fatalf("%s changed canonical HEAD: %s -> %s", tc.name, head, got)
			}
		})
	}
}

//nolint:paralleltest // Native Git and gh fixtures set process-wide environment.
func TestE2EDeletedTargetRecoveryKeepsFetchAndReceiptFailuresDiagnostic(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{name: "exact default fetch", want: "selected lifecycle Git observation failed"},
		{name: "candidate report directory", want: "read worktree-merge reports"},
		{name: "exact merged receipt", want: "gh must not run"},
	} {
		//nolint:paralleltest // each case creates its own native remote and gh environment.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
			fault := &lifecycleGitObservationFault{Runner: runner.New(), operation: "fetch", failAt: 2}
			ctx := context.Background()
			switch tc.name {
			case "exact default fetch":
				ctx = withGitRunner(ctx, fault)
			case "candidate report directory":
				path := filepath.Join(fixture.home, "reports", "worktree-merge")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "exact merged receipt":
				installFailingGitHubFixture(t)
			}
			inspection := lifecycleInspection{
				ctx: ctx, home: fixture.home, canonical: fixture.canonical, worktree: fixture.canonical,
				slug: "acme/app", base: "deleted-target", head: head, withGitHub: true,
			}
			err := inspection.checkGitHubIntegration()
			if tc.name == "exact default fetch" {
				if fault.seen < 2 || err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("second target fetch = %+v, %v; fetch count=%d", inspection.result, err, fault.seen)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s failure = %+v, %v", tc.name, inspection.result, err)
			}
			if inspection.result.IntegratedAtOrigin || inspection.result.AbsorbedAtOrigin || inspection.result.MergedPullRequest != nil {
				t.Fatalf("failed deleted-target proof granted integration: %+v", inspection.result)
			}
			if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
				t.Fatalf("deleted-target proof changed canonical HEAD: %s -> %s", head, got)
			}
		})
	}
}
