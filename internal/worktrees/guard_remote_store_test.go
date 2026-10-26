//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The configured remote-store clone is WB's own machine-state checkout: the
// guard admits it with uncommitted changes (WB's commit there briefly has the
// claim it is committing staged), while every other canonical clone stays
// held to the canonical-clean rule (sneat-dev/wb#899).
//
//nolint:paralleltest // newGitFixture overrides process-wide HOME and WB/XDG roots.
func TestE2EGuardAdmitsTheConfiguredRemoteStoreCloneWithLocalChanges(t *testing.T) {
	fixture := newGitFixture(t)
	claim := filepath.Join(fixture.canonical, "claims", "task.yaml")
	if err := os.MkdirAll(filepath.Dir(claim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claim, []byte("task: task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "claims")

	result, err := Guard(context.Background(), fixture.canonical, GuardOptions{ProjectsRoot: fixture.projectsRoot, RemoteStoreClone: fixture.canonical})
	if err != nil {
		t.Fatalf("guard on the remote-store clone = %v, want it admitted", err)
	}
	if !result.RemoteStore || result.Kind != "canonical" {
		t.Fatalf("guard result = %+v, want a canonical remote-store result", result)
	}

	other := filepath.Join(t.TempDir(), "elsewhere")
	_, err = Guard(context.Background(), fixture.canonical, GuardOptions{ProjectsRoot: fixture.projectsRoot, RemoteStoreClone: other})
	if err == nil || !strings.Contains(err.Error(), "canonical clones must remain clean") {
		t.Fatalf("guard on an ordinary dirty canonical clone = %v, want the canonical-clean refusal", err)
	}
}
