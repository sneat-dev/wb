//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // Native Git clone fixtures set process-wide Git configuration.
func TestE2ELifecycleCanonicalLocalDiscoveryAndInspectionRefusals(t *testing.T) {
	projects := t.TempDir()
	canonical := newLegacyClone(t, projects, "acme", "app")
	localRoot := filepath.Join(canonical, ".worktrees")
	for _, path := range []string{
		localRoot,
		filepath.Join(projects, "acme", "not-git", ".worktrees"),
		filepath.Join(projects, "acme", "bad name", ".worktrees"),
		filepath.Join(localRoot, "bad name"),
		filepath.Join(localRoot, "not-git-task"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(projects, "acme", "ordinary-file"), []byte("not a repository"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileRepository := filepath.Join(projects, "acme", "file-root")
	if err := os.Mkdir(fileRepository, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fileRepository, ".worktrees"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	linkedRepository := filepath.Join(projects, "acme", "linked-root")
	if err := os.Mkdir(linkedRepository, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(localRoot, filepath.Join(linkedRepository, ".worktrees")); err != nil {
		t.Fatal(err)
	}
	layouts, diagnostics := discoverCanonicalLocalWorktreeLayouts(context.Background(), projects, "acme/")
	if len(layouts) != 1 || layouts[0].WorktreesRoot != localRoot || !layouts[0].Local {
		t.Fatalf("canonical local layouts = %+v", layouts)
	}
	foundNonGit := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Path == filepath.Join(projects, "acme", "not-git") && strings.Contains(diagnostic.Message, "Git identity") {
			foundNonGit = true
		}
		if strings.Contains(diagnostic.Path, "bad name") || strings.Contains(diagnostic.Path, "linked-root") || strings.Contains(diagnostic.Path, "file-root") {
			t.Fatalf("unsafe or non-directory root admitted to diagnostics: %+v", diagnostics)
		}
	}
	if !foundNonGit {
		t.Fatalf("non-Git canonical root lacked scoped diagnostic: %+v", diagnostics)
	}
	fileProjects := filepath.Join(t.TempDir(), "projects-file")
	if err := os.WriteFile(fileProjects, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if layouts, diagnostics := discoverCanonicalLocalWorktreeLayouts(context.Background(), fileProjects, ""); len(layouts) != 0 || len(diagnostics) != 1 || diagnostics[0].Path != fileProjects {
		t.Fatalf("unreadable projects root = layouts=%+v diagnostics=%+v", layouts, diagnostics)
	}
	results, localDiagnostics, _, err := listCanonicalLocalLayout(context.Background(), projects, t.TempDir(),
		wbhome.Layout{WorktreesRoot: localRoot, Local: true}, nil, "main", "", "", false, 1, nil, inspectPolicy{})
	if err != nil || len(results) != 0 || len(localDiagnostics) != 2 {
		t.Fatalf("local refusal inventory = results=%+v diagnostics=%+v err=%v", results, localDiagnostics, err)
	}
	for _, want := range []string{"invalid task directory name", "not a Git worktree root"} {
		found := false
		for _, diagnostic := range localDiagnostics {
			found = found || strings.Contains(diagnostic.Message, want)
		}
		if !found {
			t.Fatalf("missing %q refusal: %+v", want, localDiagnostics)
		}
	}
	fileRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(fileRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := listCanonicalLocalLayout(context.Background(), projects, t.TempDir(),
		wbhome.Layout{WorktreesRoot: fileRoot, Local: true}, nil, "main", "", "", false, 1, nil, inspectPolicy{}); err == nil || !strings.Contains(err.Error(), "read canonical local worktrees") {
		t.Fatalf("file-shaped local root was accepted: %v", err)
	}
}

//nolint:paralleltest // WB home override is process-wide.
func TestE2ELifecycleTaskScopedLocalDiscoveryKeepsExactClaimAndFallback(t *testing.T) {
	projects := t.TempDir()
	t.Setenv(wbhome.EnvOverride, projects)
	home := filepath.Join(projects, ".wb")
	const task = "selected-task"
	localRoot := filepath.Join(projects, "acme", "app", ".worktrees")
	fallbackRoot := filepath.Join(projects, "fallback", "second", ".worktrees")
	for _, path := range []string{filepath.Join(localRoot, task), filepath.Join(fallbackRoot, task)} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	claims := filepath.Join(home, "worklogs", task, "runs", "run-one", "claims")
	valid := workLogClaim{Version: 2, EffortID: task, Task: task, RunID: "run-one", ClaimID: "claim-one",
		Repository: "acme/app", Worktree: filepath.Join(localRoot, task), Lifecycle: "active"}
	wtLifeCovWriteJSON(t, filepath.Join(claims, "claim-one.json"), valid)
	wtLifeCovWriteJSON(t, filepath.Join(claims, "inactive.json"), workLogClaim{Lifecycle: "complete"})
	wtLifeCovWriteJSON(t, filepath.Join(claims, "bad-repository.json"), workLogClaim{
		Task: task, EffortID: task, Repository: "unqualified", Worktree: valid.Worktree, Lifecycle: "active",
	})
	wtLifeCovWriteJSON(t, filepath.Join(claims, "wrong-parent.json"), workLogClaim{
		Task: task, EffortID: task, Repository: "acme/app", Worktree: filepath.Join(projects, "elsewhere"), Lifecycle: "active",
	})
	if err := os.WriteFile(filepath.Join(claims, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claims, "ignored.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(claims, "nested.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "worklogs", task, "runs", "bad name"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-claim.json", filepath.Join(claims, "dangling.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projects, "acme", "bad name", ".worktrees", task), 0o700); err != nil {
		t.Fatal(err)
	}
	layouts, diagnostics := discoverTaskScopedLocalWorktreeLayouts(projects, map[string]bool{task: true})
	if len(diagnostics) != 0 || len(layouts) != 2 || layouts[0].WorktreesRoot != localRoot || layouts[1].WorktreesRoot != fallbackRoot {
		t.Fatalf("task-scoped claim and manifest fallback = layouts=%+v diagnostics=%+v", layouts, diagnostics)
	}
	brokenRuns := filepath.Join(home, "worklogs", "broken-task", "runs")
	if err := os.MkdirAll(filepath.Dir(brokenRuns), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brokenRuns, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if layouts, diagnostics := discoverTaskScopedLocalWorktreeLayouts(projects, map[string]bool{"broken-task": true}); len(layouts) != 0 || len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "read task Work Log runs") {
		t.Fatalf("file-shaped runs boundary = layouts=%+v diagnostics=%+v", layouts, diagnostics)
	}
	badClaims := filepath.Join(home, "worklogs", task, "runs", "z-broken-run", "claims")
	if err := os.MkdirAll(filepath.Dir(badClaims), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badClaims, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if layouts, diagnostics := discoverTaskScopedLocalWorktreeLayouts(projects, map[string]bool{task: true}); len(layouts) != 0 || len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "read task Work Log claims") {
		t.Fatalf("file-shaped claims boundary = layouts=%+v diagnostics=%+v", layouts, diagnostics)
	}
	if err := os.Remove(badClaims); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"github.com", "gitlab.com"} {
		if err := os.MkdirAll(filepath.Join(projects, host, "acme", "app", ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := CanonicalRepositoryPath(projects, "acme/app"); err == nil || !strings.Contains(err.Error(), "more than one host") {
		t.Fatalf("ambiguous canonical clone was accepted: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(localRoot, task)); err != nil {
		t.Fatal(err)
	}
	if layouts, diagnostics := discoverTaskScopedLocalWorktreeLayouts(projects, map[string]bool{task: true}); len(diagnostics) != 0 || len(layouts) != 1 || layouts[0].WorktreesRoot != fallbackRoot {
		t.Fatalf("ambiguous claim chose a host: layouts=%+v diagnostics=%+v", layouts, diagnostics)
	}
}

//nolint:paralleltest // Native Create and Git fixtures set process-wide WB and Git state.
func TestE2ELifecycleRegistryClaimAndMissingCheckoutBoundaries(t *testing.T) {
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	ctx := context.Background()
	created, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "registry-proof", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create registered shared checkout: %+v %v", created, err)
	}
	path := created[0].WorktreeDir
	inspect := func(known map[string]bool, tasks map[string]bool, filter string) ([]ListResult, []ListDiagnostic) {
		return listClaimedRegistryWorktrees(ctx, fixture.projectsRoot, fixture.home, known, tasks,
			"main", filter, "", false, 1, nil, inspectPolicy{})
	}
	results, diagnostics := inspect(map[string]bool{}, nil, "")
	if len(results) != 1 || results[0].WorktreeDir != path || len(diagnostics) != 0 {
		t.Fatalf("registered claimed checkout = results=%+v diagnostics=%+v", results, diagnostics)
	}
	if results, diagnostics := inspect(map[string]bool{path: true}, nil, ""); len(results) != 0 || len(diagnostics) != 0 {
		t.Fatalf("known checkout was inspected twice: results=%+v diagnostics=%+v", results, diagnostics)
	}
	if results, diagnostics := inspect(map[string]bool{}, nil, "unrelated/repository"); len(results) != 0 || len(diagnostics) != 0 {
		t.Fatalf("unselected canonical registry was inspected: results=%+v diagnostics=%+v", results, diagnostics)
	}
	results, diagnostics = inspect(map[string]bool{}, map[string]bool{"registry-proof": true}, "")
	if len(results) != 1 || results[0].WorktreeDir != path || len(diagnostics) != 0 {
		t.Fatalf("task-scoped claimed checkout = results=%+v diagnostics=%+v", results, diagnostics)
	}
	claim, _, _, err := activeWorkLogClaim(fixture.home, path)
	if err != nil {
		t.Fatal(err)
	}
	runsRoot := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs")
	badRuns := filepath.Join(runsRoot, "0 invalid")
	if err := os.Mkdir(badRuns, 0o700); err != nil {
		t.Fatal(err)
	}
	invalidClaims := filepath.Join(runsRoot, "a-invalid-run", "claims")
	if err := os.MkdirAll(invalidClaims, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalidClaims, "ignored.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalidClaims, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	badClaims := filepath.Join(runsRoot, "z-file-run", "claims")
	if err := os.MkdirAll(filepath.Dir(badClaims), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badClaims, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	results, diagnostics = inspect(map[string]bool{}, map[string]bool{"registry-proof": true}, "")
	if len(results) != 1 || results[0].WorktreeDir != path || len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "read task Work Log claims") {
		t.Fatalf("scoped malformed siblings = results=%+v diagnostics=%+v", results, diagnostics)
	}
	badTaskRuns := filepath.Join(fixture.home, "worklogs", "bad-task", "runs")
	if err := os.MkdirAll(filepath.Dir(badTaskRuns), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badTaskRuns, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if results, diagnostics := inspect(map[string]bool{}, map[string]bool{"bad-task": true}, ""); len(results) != 0 || len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "read task Work Log runs") {
		t.Fatalf("scoped file-shaped runs = results=%+v diagnostics=%+v", results, diagnostics)
	}
	if err := os.RemoveAll(badRuns); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Dir(invalidClaims), filepath.Dir(badClaims), filepath.Dir(badTaskRuns)} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	results, diagnostics = inspect(map[string]bool{}, nil, "")
	if len(results) != 0 {
		t.Fatalf("missing registered checkout was admitted: %+v", results)
	}
	found := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Path == path && diagnostic.Task == "registry-proof" && strings.Contains(diagnostic.Message, "still registers") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing registered checkout lost exact claim warning: %+v", diagnostics)
	}
	if err := os.Mkdir(badRuns, 0o700); err != nil {
		t.Fatal(err)
	}
	_, diagnostics = inspect(map[string]bool{}, nil, "")
	found = false
	for _, diagnostic := range diagnostics {
		if diagnostic.Path == path && strings.Contains(diagnostic.Message, "inspect missing registered worktree ownership") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing registered checkout concealed malformed claim namespace: %+v", diagnostics)
	}
}
