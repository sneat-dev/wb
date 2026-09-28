package deps

// Pack unit p02 coverage: mergeReleaseObservations, githubActionsAdapter's
// working-tree scan error paths, goAdapter.inspectWorkingTree and
// npmAdapter.inspectWorkingTree. Unit tier only: real TempDir filesystem
// fixtures, no real git/gh, no npm/pnpm process starts (regenerateAffectedLockfiles
// is never reached by these calls: inspectWorkingTree never calls apply()).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMergeReleaseObservations(t *testing.T) {
	t.Parallel()
	first := ReleaseObservation{Module: "example.com/foo", Status: "pending", CheckedAt: time.Unix(100, 0)}
	second := ReleaseObservation{
		Module:               "example.com/foo",
		ExpectedRequirements: map[string]string{"example.com/bar": "v1.2.3"},
		Status:               "pending",
		CheckedAt:            time.Unix(200, 0),
	}
	merged := mergeReleaseObservations([]ReleaseObservation{first}, []ReleaseObservation{second})
	if len(merged) != 1 {
		t.Fatalf("expected 1 merged observation, got %d", len(merged))
	}
	if merged[0].ExpectedRequirements["example.com/bar"] != "v1.2.3" {
		t.Fatalf("expected the second group's requirement to be merged in, got %+v", merged[0].ExpectedRequirements)
	}
}

func TestGithubActionsInspectWorkingTreeRootUnreadable(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	githubDir := filepath.Join(worktree, ".github")
	if err := os.MkdirAll(githubDir, 0o755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	if err := os.Chmod(githubDir, 0o000); err != nil {
		t.Fatalf("chmod .github: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(githubDir, 0o755) })

	if _, err := (githubActionsAdapter{}).inspectWorkingTree(context.Background(), worktree, Target{Dependency: "example/action", Version: "v1"}, Options{}); err == nil {
		t.Fatalf("expected a stat error for an unreadable .github directory")
	}
}

func TestGithubActionsInspectWorkingTreeWalkErr(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	workflows := filepath.Join(worktree, ".github", "workflows")
	locked := filepath.Join(workflows, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatalf("mkdir locked: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod locked: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if _, err := (githubActionsAdapter{}).inspectWorkingTree(context.Background(), worktree, Target{Dependency: "example/action", Version: "v1"}, Options{}); err == nil {
		t.Fatalf("expected a walk error from the unreadable subdirectory")
	}
}

func TestGithubActionsInspectWorkingTreeSuccess(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	workflows := filepath.Join(worktree, ".github", "workflows")
	if err := os.MkdirAll(workflows, 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	content := "name: CI\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n"
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if _, err := (githubActionsAdapter{}).inspectWorkingTree(context.Background(), worktree, Target{Dependency: "actions/checkout", Version: "v4"}, Options{}); err != nil {
		t.Fatalf("inspectWorkingTree: %v", err)
	}
}

func TestGoAdapterInspectWorkingTree(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	goMod := "module testmodule\n\ngo 1.21\n\nrequire example.com/foo v1.0.0\n"
	if err := os.WriteFile(filepath.Join(worktree, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	unchanged, err := (goAdapter{}).inspectWorkingTree(context.Background(), worktree, Target{Dependency: "example.com/foo", Version: "v1.0.0"}, Options{})
	if err != nil {
		t.Fatalf("inspectWorkingTree unchanged: %v", err)
	}
	if len(unchanged) != 1 || unchanged[0].Action != "unchanged" {
		t.Fatalf("expected 1 unchanged decision, got %+v", unchanged)
	}

	planned, err := (goAdapter{}).inspectWorkingTree(context.Background(), worktree, Target{Dependency: "example.com/foo", Version: "v1.2.0"}, Options{})
	if err != nil {
		t.Fatalf("inspectWorkingTree planned: %v", err)
	}
	if len(planned) != 1 || planned[0].Action != "planned" {
		t.Fatalf("expected 1 planned decision, got %+v", planned)
	}
}

func TestNpmAdapterInspectWorkingTreePackageBlocked(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	pkg := "{\n  \"dependencies\": {\n    \"other-pkg\": \"1.0.0\",\n    \"example-pkg\": \"2.0.0\"\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(worktree, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if _, err := (npmAdapter{}).inspectWorkingTree(context.Background(), worktree, Target{Dependency: "example-pkg", Version: "1.0.0"}, Options{AllowDowngrade: false}); err == nil {
		t.Fatalf("expected a blocked-downgrade error")
	}
}

func TestNpmAdapterInspectWorkingTreeWorkspaceBlocked(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	workspace := "overrides:\n  other-pkg: 1.0.0\n  example-pkg: 2.0.0\n"
	if err := os.WriteFile(filepath.Join(worktree, "pnpm-workspace.yaml"), []byte(workspace), 0o644); err != nil {
		t.Fatalf("write pnpm-workspace.yaml: %v", err)
	}
	if _, err := (npmAdapter{}).inspectWorkingTree(context.Background(), worktree, Target{Dependency: "example-pkg", Version: "1.0.0"}, Options{AllowDowngrade: false}); err == nil {
		t.Fatalf("expected a blocked-downgrade error from the workspace override")
	}
}

func TestNpmAdapterInspectWorkingTreeEmpty(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	decisions, err := (npmAdapter{}).inspectWorkingTree(context.Background(), worktree, Target{Dependency: "example-pkg", Version: "1.0.0"}, Options{})
	if err != nil {
		t.Fatalf("inspectWorkingTree: %v", err)
	}
	if len(decisions) != 0 {
		t.Fatalf("expected no decisions for an empty worktree, got %+v", decisions)
	}
}
