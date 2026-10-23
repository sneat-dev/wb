package streamrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
)

//nolint:paralleltest // Process-wide environment changes in TestStreamWorktreesPlansCentralLocalAndConfiguredSharedPaths; these rows share their parent environment and remain sequential.
func TestStreamWorktreesPlansCentralLocalAndConfiguredSharedPaths(t *testing.T) {
	projectsRoot := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	adapter := streamWorktrees{projectsRoot: projectsRoot}

	central, err := adapter.PlannedWorktree("stream-paths", "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	wantCentral := filepath.Join(projectsRoot, ".worktrees", "stream-paths", "acme", "app")
	if central != wantCentral {
		t.Fatalf("central planned path = %q, want %q", central, wantCentral)
	}

	configPath := filepath.Join(configHome, "wb", "worktrees.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("version: 1\nworktrees:\n  store: repository-local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := adapter.PlannedWorktree("stream-paths", "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	wantLocal := filepath.Join(projectsRoot, "acme", "app", ".worktrees", "stream-paths")
	if local != wantLocal {
		t.Fatalf("repository-local planned path = %q, want %q", local, wantLocal)
	}

	sharedRoot := filepath.Join(t.TempDir(), "shared-worktrees")
	if err := os.WriteFile(configPath, []byte("version: 1\nworktrees:\n  root: "+sharedRoot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared, err := adapter.PlannedWorktree("stream-paths", "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	sharedParent, err := filepath.EvalSymlinks(filepath.Dir(sharedRoot))
	if err != nil {
		t.Fatal(err)
	}
	wantShared := filepath.Join(sharedParent, filepath.Base(sharedRoot), "stream-paths", "acme", "app")
	if shared != wantShared {
		t.Fatalf("shared planned path = %q, want %q", shared, wantShared)
	}
}

//nolint:paralleltest // Process-wide environment changes in TestStreamWorktreesRefusesAnInvalidConfiguredRoot; these rows share their parent environment and remain sequential.
func TestStreamWorktreesRefusesAnInvalidConfiguredRoot(t *testing.T) {
	projectsRoot := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "wb", "worktrees.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("version: 1\nworktrees:\n  root: relative/root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&streamWorktrees{projectsRoot: projectsRoot}).PlannedWorktree("stream-paths", "acme/app"); err == nil {
		t.Fatal("invalid user worktree root planned a stream checkout")
	}
}

func TestStreamWorktreesPassesExactSquashReceiptToCleanup(t *testing.T) {
	t.Parallel()
	var got worktrees.CleanupOptions
	cleanup := func(_ context.Context, options worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		got = options
		return worktrees.CleanupOutcome{Results: []worktrees.CleanupResult{{
			ListResult: worktrees.ListResult{Repository: "acme/app"}, Applied: true,
		}}}, nil
	}
	receipt := &streams.SquashAbsorptionReceipt{
		Target: "main", SourceBranch: "stream/incidentius", SourceSHA: "41cd41cd41cd41cd41cd41cd41cd41cd41cd41cd",
		CandidateSHA: "8def8def8def8def8def8def8def8def8def8def", LandingSHA: "8d0e8d0e8d0e8d0e8d0e8d0e8d0e8d0e8d0e8d0e",
	}
	worktree := "/worktrees/incidentius/acme/app"
	if err := (&streamWorktrees{projectsRoot: "/fixture", cleanup: cleanup}).Remove(context.Background(), "incidentius", "acme/app", worktree, receipt); err != nil {
		t.Fatal(err)
	}
	if len(got.MergeReceiptProofs) != 1 {
		t.Fatalf("cleanup proofs = %#v, want one", got.MergeReceiptProofs)
	}
	proof := got.MergeReceiptProofs[0]
	if proof.Repository != "acme/app" || proof.Target != receipt.Target || proof.SourceTask != "incidentius" ||
		proof.SourceWorktree != worktree || proof.SourceBranch != receipt.SourceBranch || proof.SourceSHA != receipt.SourceSHA ||
		proof.CandidateSHA != receipt.CandidateSHA || proof.LandingSHA != receipt.LandingSHA {
		t.Fatalf("cleanup proof = %#v, want exact receipt mapping", proof)
	}
}
func TestAdapterValidationAndCreateErrorsPreserveIdentity(t *testing.T) {
	t.Parallel()
	adapter := streamWorktrees{projectsRoot: t.TempDir()}
	if _, err := adapter.PlannedWorktree("task", "acme/app/extra"); err == nil {
		t.Fatal("malformed slug accepted")
	}
	failure := errors.New("create failed")
	adapter.create = func(ctx context.Context, repos []string, options worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
		if !options.Resume || !options.BranchChosen || options.Operation != "task" || options.Branch != "stream/task" || len(repos) != 1 {
			t.Fatal(options, repos)
		}
		return nil, failure
	}
	if _, err := adapter.Create(context.Background(), "task", "stream/task", []string{"acme/app"}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
