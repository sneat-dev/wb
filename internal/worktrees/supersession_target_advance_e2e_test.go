//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // newGitFixture uses t.Setenv to scope WB and Git configuration for native Git subprocesses.
func TestE2EGenericSupersessionReceiptIgnoresTargetOnlyDependencyChanges(t *testing.T) {
	fixture := newGitFixture(t)
	if err := os.MkdirAll(filepath.Join(fixture.canonical, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, fixture.canonical, "package.json", `{"name":"@acme/app","dependencies":{"nx":"22.6.4"},"scripts":{"test":"go test ./..."}}`+"\n", "seed dependency manifest")
	writeAndCommit(t, fixture.canonical, ".github/workflows/ci.yml", "name: ci\njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n      - run: go test ./...\n", "seed workflow")
	gitTest(t, fixture.canonical, "push", "origin", "main")

	const task = "generic-supersession-target-advance"
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: task, WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	writeAndCommit(t, result.WorktreeDir, "package.json", `{"name":"@acme/app","dependencies":{"nx":"22.6.4"},"scripts":{"pretest":"go generate ./...","test":"go test ./..."}}`+"\n", "add source pretest script")
	writeAndCommit(t, result.WorktreeDir, ".github/workflows/ci.yml", "name: ci\njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n      - run: go generate ./...\n      - run: go test ./...\n", "run source generator in workflow")
	sourceHead := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")

	writeAndCommit(t, fixture.canonical, "package.json", `{"name":"@acme/app","dependencies":{"nx":"22.7.7"},"scripts":{"test":"go test ./..."}}`+"\n", "target-only dependency update")
	writeAndCommit(t, fixture.canonical, ".github/workflows/ci.yml", "name: ci\njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v5\n      - run: go test ./...\n", "target-only workflow action update")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	targetHead := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")

	endpointDiff := strings.Fields(gitTestOutput(t, fixture.canonical, "diff", "--name-only", targetHead, sourceHead))
	endpointFiles := make(map[string]bool, len(endpointDiff))
	for _, file := range endpointDiff {
		endpointFiles[file] = true
	}
	if !endpointFiles["package.json"] || !endpointFiles[".github/workflows/ci.yml"] {
		t.Fatalf("diverged endpoint diff did not expose the target-only files: %v", endpointDiff)
	}
	entry := ListResult{Task: task, Repository: result.Repository, Branch: result.Branch, Base: "main", HeadSHA: sourceHead,
		RemoteTargetSHA: targetHead, CanonicalDir: fixture.canonical, WorktreeDir: result.WorktreeDir}
	sourceCommits := strings.Fields(gitTestOutput(t, fixture.canonical, "rev-list", "--reverse", "--end-of-options", targetHead+".."+sourceHead))
	receipt := completeSupersessionReceipt(result, task, sourceCommits, targetHead, "generic-receipt-after-target-advance")
	if rejection := supersessionService().ValidateSupersessionReceipt(context.Background(), receipt, supersessionEntry(entry)); rejection != "" {
		t.Fatalf("trusted generic receipt for script-only source changes was rejected: %s", rejection)
	}
}
