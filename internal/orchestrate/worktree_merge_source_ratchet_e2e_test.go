//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2EValidationFailedSupersessionSourceRequiresImmutableClaimBase(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "source-base-guard", "feature/source-base-guard", "source.txt", "source\n")
	view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: source.WorktreeDir})
	if err != nil || view.Claim == nil {
		t.Fatalf("source claim: %+v %v", view, err)
	}
	claim := view.Claim
	contents, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(contents, &record); err != nil {
		t.Fatal(err)
	}
	newID := worktrees.WorkLogClaimID(claim.EffortID, worktrees.CreateResult{Repository: claim.Repository, WorktreeDir: claim.Worktree, Branch: claim.Branch, BaseSHA: claim.BaseSHA})
	record["base"], record["claim_id"] = "", newID
	changed, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(claim.ClaimPath, claim.ClaimPath+".retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(claim.ClaimPath), newID+".json"), changed, 0600); err != nil {
		t.Fatal(err)
	}
	projection := filepath.Join(source.WorktreeDir, ".wb-worklog", "recovery.json")
	pointer, err := os.ReadFile(projection)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(pointer, []byte(claim.ClaimID)) {
		t.Fatal("source projection lacks original claim id")
	}
	if err := os.WriteFile(projection, bytes.ReplaceAll(pointer, []byte(claim.ClaimID), []byte(newID)), 0600); err != nil {
		t.Fatal(err)
	}
	receipt := WorktreeMergeReceipt{Repository: claim.Repository, Target: "main"}
	input := WorktreeMergeSource{Task: view.Claim.Task, Worktree: source.WorktreeDir, Branch: source.Branch, SHA: strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))}
	if _, _, err := validateValidationFailedSupersessionSourceWithRunner(context.Background(), defaultRunner, fixture.githubDir, receipt, input); err == nil || !strings.Contains(err.Error(), "no immutable claim base") {
		t.Fatalf("missing claim base accepted: %v", err)
	}
}

func TestE2EValidationFailedSupersessionSourceRequiresRecordedRootAfterClaimBase(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	oldHead := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(fixture.canonical, "new-base.txt"), "new base\n")
	runEngineGit(t, fixture.canonical, "add", "new-base.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance immutable source base")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	source := createMergeSource(t, fixture, "source-newer-base", "feature/source-newer-base", "source.txt", "source\n")
	view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: source.WorktreeDir})
	if err != nil || view.Claim == nil || view.Claim.BaseSHA == oldHead {
		t.Fatalf("newer claim base: %+v %v", view, err)
	}
	receipt := WorktreeMergeReceipt{Repository: view.Claim.Repository, Target: "main"}
	input := WorktreeMergeSource{Task: view.Claim.Task, Worktree: source.WorktreeDir, Branch: source.Branch, SHA: oldHead}
	if _, _, err := validateValidationFailedSupersessionSourceWithRunner(context.Background(), defaultRunner, fixture.githubDir, receipt, input); err == nil || !strings.Contains(err.Error(), "does not descend from immutable claim base") {
		t.Fatalf("source predating claim base accepted: %v", err)
	}
}
