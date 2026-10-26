package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Both candidates and source branches are genuine WB-created Git worktrees.
// GitHub is the sole external fixture, including the superseded closed PR.
//
//nolint:paralleltest // The published Prepare journey installs its private gh fixture through process environment.
func TestUnpublishedConflictPreservesExactRebatchedCandidateCustody(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(fmt.Sprint("published=", published), func(t *testing.T) {
			f, old, _ := unpublishedConflictFixture(t)
			old.Phase, old.Status = WorktreeMergePhasePrepare, WorktreeMergePrepared
			if published {
				runEngineGit(t, old.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+old.Candidate.Branch)
				old.Phase, old.Status, old.PullRequest, old.PublishedCandidateSHA = WorktreeMergePhaseLand, WorktreeMergeChecksFailed, "41", old.Candidate.SHA
				installWorktreeMergeDirectGH(t)
				t.Setenv("WB_TEST_CANDIDATE_SHA", old.Candidate.SHA)
			}
			if err := persistWorktreeMergeReceipt(old); err != nil {
				t.Fatal(err)
			}
			extraBase := "main"
			if published {
				extraBase = old.Candidate.Branch
			}
			extra := createMergeSourceOnBase(t, f, "rebatch-extra", "feature/rebatch-extra", extraBase, "extra.txt", "extra work\n")
			if published {
				view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: extra.WorktreeDir})
				if err != nil || view.Claim == nil || view.Claim.Base != old.Candidate.Branch || view.Claim.BaseSHA != old.Candidate.SHA {
					t.Fatalf("native source lacks exact predecessor branch/base: %+v %v", view.Claim, err)
				}
			}
			r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{old.Sources[0].Worktree, extra.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", RebatchReceipt: old.ReceiptPath})
			if err != nil {
				t.Fatal(err)
			}
			r.Phase, r.Status = WorktreeMergePhaseLand, WorktreeMergeConflict
			if err := persistWorktreeMergeReceipt(r); err != nil {
				t.Fatal(err)
			}
			state, head := "closed", old.Candidate.SHA
			ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{
				Read: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if len(args) > 1 && args[0] == "pr" && args[1] == "list" {
						return []byte("[]"), nil
					}
					if len(args) > 1 && args[0] == "pr" && args[1] == "view" {
						// A stale preliminary answer stays CLOSED even when the final
						// identity-bearing snapshot reopens below. The old split proof
						// would have accepted CLOSED followed by OPEN.
						return []byte(fmt.Sprintf(`{"state":"CLOSED","closedAt":"2026-10-26T00:00:00Z","headRefName":%q,"headRefOid":%q}`, old.Candidate.Branch, head)), nil
					}
					return nil, fmt.Errorf("unexpected GitHub read: %v", args)
				},
				Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
					if request.Endpoint != "repos/acme/app/pulls/41" {
						return githubobserver.Response{}, fmt.Errorf("unexpected endpoint %s", request.Endpoint)
					}
					return githubobserver.Response{Body: []byte(fmt.Sprintf(`{"number":41,"state":%q,"merged":false,"head":{"ref":%q,"sha":%q,"repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`, state, old.Candidate.Branch, head))}, nil
				},
			})
			o := WorktreeMergeUnpublishedConflictOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Actor: "operator", Reason: "preserve all rebatch custody"}
			preview, err := AcknowledgeUnpublishedConflict(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			if len(preview.Claims) != 4 || len(preview.RebatchProofs) != 1 || preview.RebatchProofs[0].Candidate != old.Candidate || preview.RebatchOf != old.ReceiptPath {
				t.Fatalf("lost rebatch custody: %+v", preview)
			}
			missing := preview.RebatchProofs[0]
			missing.ReceiptPath = filepath.Join(t.TempDir(), "missing-receipt")
			if err := proveUnpublishedConflictPredecessorPublication(ctx, missing); err == nil {
				t.Fatal("accepted absent predecessor receipt")
			}
			wrong := preview.RebatchProofs[0]
			wrong.Candidate.SHA = "wrong"
			if err := proveUnpublishedConflictPredecessorPublication(ctx, wrong); err == nil {
				t.Fatal("accepted substituted predecessor identity")
			}
			if !published {
				proof := preview.RebatchProofs[0]
				remote := filepath.Join(filepath.Dir(f.githubDir), "remote.git")
				if err := os.Rename(remote, remote+"-held"); err != nil {
					t.Fatal(err)
				}
				publicationErr := proveUnpublishedConflictPredecessorPublication(ctx, proof)
				if err := os.Rename(remote+"-held", remote); err != nil {
					t.Fatal(err)
				}
				if publicationErr == nil {
					t.Fatal("accepted unavailable predecessor publication evidence")
				}
				runEngineGit(t, old.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+old.Candidate.Branch)
				if err := proveUnpublishedConflictPredecessorPublication(ctx, proof); err == nil {
					t.Fatal("accepted newly published predecessor")
				}
				runEngineGit(t, remote, "update-ref", "-d", "refs/heads/"+old.Candidate.Branch)
			}
			dirty := filepath.Join(old.Candidate.Worktree, "uncommitted-custody.txt")
			writeEngineFile(t, dirty, "preserve dirty predecessor")
			if _, err := AcknowledgeUnpublishedConflict(ctx, o); err == nil {
				t.Fatal("released dirty rebatched candidate")
			}
			if err := os.Remove(dirty); err != nil {
				t.Fatal(err)
			}
			if published {
				missingRoot := old
				missingRoot.Candidate.Worktree = filepath.Join(t.TempDir(), "missing-candidate")
				if err := proveClosedUnpublishedConflictPredecessor(ctx, missingRoot); err == nil {
					t.Fatal("accepted missing published predecessor checkout")
				}
				unavailable := githubobserver.WithReader(t.Context(), githubobserver.Reader{Get: func(context.Context, githubobserver.GetRequest) (githubobserver.Response, error) {
					return githubobserver.Response{}, fmt.Errorf("GitHub unavailable")
				}})
				if err := proveClosedUnpublishedConflictPredecessor(unavailable, old); err == nil {
					t.Fatal("accepted unavailable final PR snapshot")
				}
				for _, badState := range []string{"open", "merged"} {
					state = badState
					if _, err := AcknowledgeUnpublishedConflict(ctx, o); err == nil {
						t.Fatalf("accepted %s predecessor PR", badState)
					}
				}
				state = "closed"
				head = "wrong-head"
				if _, err := AcknowledgeUnpublishedConflict(ctx, o); err == nil {
					t.Fatal("accepted drifted superseded PR head")
				}
				head = old.Candidate.SHA
				remote := filepath.Join(filepath.Dir(f.githubDir), "remote.git")
				if err := os.Rename(remote, remote+"-held"); err != nil {
					t.Fatal(err)
				}
				publicationErr := proveClosedUnpublishedConflictPredecessor(ctx, old)
				if err := os.Rename(remote+"-held", remote); err != nil {
					t.Fatal(err)
				}
				if publicationErr == nil {
					t.Fatal("accepted unavailable published predecessor ref")
				}
				runEngineGit(t, remote, "update-ref", "refs/heads/"+old.Candidate.Branch, old.TargetSHA)
				if _, err := AcknowledgeUnpublishedConflict(ctx, o); err == nil {
					t.Fatal("accepted drifted published predecessor ref")
				}
				runEngineGit(t, remote, "update-ref", "refs/heads/"+old.Candidate.Branch, old.Candidate.SHA)
			}
			o.Apply, o.ExpectedProofID = true, preview.ID
			ack, err := AcknowledgeUnpublishedConflict(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readUnpublishedConflictAcknowledgement(r); err != nil {
				t.Fatal(err)
			}
			for _, claim := range ack.Claims {
				digest, err := worktreeMergeReceiptSHA256(claim.ClaimPath)
				if err != nil || digest != claim.ClaimSHA256 {
					t.Fatalf("changed preserved claim %s: %v", claim.ClaimPath, err)
				}
			}
			if claim, err := ActiveMergeLaneClaim(f.githubDir, r.Repository, old.Candidate.Branch); err != nil || claim != nil {
				t.Fatalf("exact rebatch custody remained claimed: %+v %v", claim, err)
			}
			next, err := PrepareWorktreeMerge(ctx, WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{r.Sources[0].Worktree, r.Sources[1].Worktree}, Target: r.Target, Model: "test-model", AgentRuntime: "test"})
			if err != nil || next.ReceiptPath == r.ReceiptPath || next.Candidate.Worktree == r.Candidate.Worktree || next.Candidate.Worktree == old.Candidate.Worktree {
				t.Fatalf("fresh rebatch-independent prepare failed: %+v %v", next, err)
			}
			// Recomputed outer IDs cannot hide a missing predecessor custody snapshot.
			ack.RebatchProofs = nil
			ack.ID = unpublishedConflictProofID(ack)
			data, _ := json.Marshal(ack)
			if err := os.WriteFile(ack.AcknowledgementPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readUnpublishedConflictAcknowledgement(r); err == nil {
				t.Fatal("accepted omitted predecessor custody")
			}
		})
	}
}
