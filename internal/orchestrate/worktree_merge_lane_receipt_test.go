package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestMergeLaneClaimFindsCandidateAfterMalformedSibling(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home, err := wbhome.EnsureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	reports := filepath.Join(home, "reports", "worktree-merge")
	if err := os.MkdirAll(reports, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reports, "000-malformed.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	lane := worktreeMergeLaneID("acme/repo", "main")
	path := filepath.Join(reports, lane+".json")
	receipt := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ReceiptPath: path,
		Repository: "acme/repo", Target: "main", Status: WorktreeMergePrepared,
		Candidate: WorktreeMergeCandidate{Branch: "wb/candidate"},
		Sources:   []WorktreeMergeSource{{Branch: "wb/source"}}}
	writeMergeLaneReceipt(t, path, receipt)
	claim, err := ActiveMergeLaneClaim(root, "acme/repo", "wb/candidate")
	if err != nil || claim == nil || claim.Lane != lane || claim.ReceiptPath != path {
		t.Fatalf("candidate claim = %+v, %v", claim, err)
	}
	claim, err = ActiveMergeLaneClaim(root, "other/repo", "wb/candidate")
	if err != nil || claim != nil {
		t.Fatalf("unrelated repository claim = %+v, %v", claim, err)
	}
	active, err := activeWorktreeMergeLaneReceipt(context.Background(), root, reports, lane)
	if err != nil || active == nil || active.ReceiptPath != path {
		t.Fatalf("active lane = %+v, %v", active, err)
	}
	active, err = activeWorktreeMergeLaneReceipt(context.Background(), root, reports, lane, path)
	if err != nil || active != nil {
		t.Fatalf("excluded lane = %+v, %v", active, err)
	}
	receipt.Status = WorktreeMergeComplete
	writeMergeLaneReceipt(t, path, receipt)
	claim, err = ActiveMergeLaneClaim(root, "acme/repo", "wb/source")
	if err != nil || claim != nil {
		t.Fatalf("complete receipt claim = %+v, %v", claim, err)
	}
}

func TestMergeLaneClaimIncludesRebatchedCandidate(t *testing.T) {
	t.Parallel()
	receipt := WorktreeMergeReceipt{Sources: []WorktreeMergeSource{{Branch: "wb/source"}},
		Candidate:           WorktreeMergeCandidate{Branch: "wb/candidate"},
		RebatchedCandidates: []WorktreeMergeCandidate{{Branch: "wb/rebatched"}}}
	for _, branch := range []string{"wb/source", "wb/candidate", "wb/rebatched"} {
		if !worktreeMergeReceiptClaimsBranch(receipt, branch) {
			t.Fatalf("branch %q lost its lane claim", branch)
		}
	}
	if worktreeMergeReceiptClaimsBranch(receipt, "wb/other") {
		t.Fatal("unrelated branch inherited a lane claim")
	}
}

//nolint:paralleltest // newEngineFixture changes the process environment with t.Setenv
func TestActiveMergeLaneConflictReleaseRequiresUnpublishedPrepare(t *testing.T) {
	fixture := newEngineFixture(t)
	lane := worktreeMergeLaneID("acme/app", "main")
	for _, test := range []struct {
		name      string
		phase     WorktreeMergePhase
		published string
		pr        string
		landing   string
		wantHeld  bool
		setup     string
		wantError string
	}{
		{name: "unpublished prepare"},
		{name: "remote candidate without receipt publication", setup: "publish", wantHeld: true},
		{name: "published candidate", published: "published-sha", wantHeld: true},
		{name: "pull request", pr: "https://example.test/pr/1", wantHeld: true},
		{name: "land phase", phase: WorktreeMergePhaseLand, wantHeld: true},
		{name: "landed sha", landing: "landed-sha", wantHeld: true},
		{name: "invalid repository", setup: "invalid-repository", wantError: "invalid"},
		{name: "unreachable origin", setup: "remove-origin", wantError: "verify unpublished conflict candidate"},
		{name: "invalid adoption sidecar", setup: "invalid-adoption", wantError: "validate published-candidate adoption"},
	} {
		//nolint:paralleltest // each case rewrites and scans the same receipt path
		t.Run(test.name, func(t *testing.T) {
			reports := filepath.Join(fixture.githubDir, ".wb", "reports", "worktree-merge")
			if err := os.MkdirAll(reports, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(reports, lane+".json")
			phase := test.phase
			if phase == "" {
				phase = WorktreeMergePhasePrepare
			}
			repository := "acme/app"
			if test.setup == "invalid-repository" {
				repository = "../invalid"
			}
			writeMergeLaneReceipt(t, path, WorktreeMergeReceipt{
				SchemaVersion: WorktreeMergeSchemaVersion, ReceiptPath: path, Lane: lane,
				Repository: repository, Target: "main", Phase: phase, Status: WorktreeMergeConflict,
				Candidate:             WorktreeMergeCandidate{Branch: "wb/integration/main/fixture"},
				PublishedCandidateSHA: test.published, PullRequest: test.pr, LandingSHA: test.landing,
			})
			switch test.setup {
			case "invalid-adoption":
				adoptionPath := publishedCandidateAdoptionPath(path)
				if err := os.WriteFile(adoptionPath, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(adoptionPath) })
			}
			fake := runnertest.New(t)
			remote := ""
			var remoteErr error
			switch test.setup {
			case "publish":
				remote = "fixture-sha\trefs/heads/wb/integration/main/fixture\n"
			case "remove-origin":
				remoteErr = errors.New("origin unavailable")
			}
			if test.setup != "invalid-adoption" && test.published == "" && test.pr == "" && test.landing == "" && phase == WorktreeMergePhasePrepare && test.setup != "invalid-repository" {
				fake.ExpectArgv([]string{"git", "ls-remote", "--heads", "origin", "refs/heads/wb/integration/main/fixture"}, runner.Result{Stdout: remote}, remoteErr)
			}
			active, err := activeWorktreeMergeLaneReceiptWithRunner(context.Background(), fixture.githubDir, reports, lane, fake)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("active lane error = %v; want %q", err, test.wantError)
				}
				return
			}
			if err != nil || (active != nil) != test.wantHeld {
				t.Fatalf("active lane = %+v, %v; want held=%t", active, err, test.wantHeld)
			}
		})
	}
}

func writeMergeLaneReceipt(t *testing.T, path string, receipt WorktreeMergeReceipt) {
	t.Helper()
	contents, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}
