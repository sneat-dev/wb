package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // The native remote GitHub fixture sets process-wide GH/PATH state.
func TestStrandedLandingPublishedPredecessorRequiresAncestryAndImmutableEvidence(t *testing.T) {
	f := newEngineFixture(t)
	source := createMergeSource(t, f, "predecessor-source", "feature/predecessor", "source.txt", "original\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	predecessor := r.Candidate.SHA
	runEngineGit(t, r.Candidate.Worktree, "commit", "--allow-empty", "-m", "advance preserved candidate")
	r.Candidate.SHA = strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", "HEAD"))
	runEngineGit(t, r.Candidate.Worktree, "commit", "--allow-empty", "-m", "advance merged PR head")
	prHead := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "merge", "--no-ff", "--no-edit", prHead)
	landing := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "push", "origin", "main")
	r.Phase, r.Status, r.PullRequest, r.PublishedCandidateSHA = WorktreeMergePhaseLand, WorktreeMergeConflict, "https://example.test/acme/app/pull/818", predecessor
	r.Route.Route = WorktreeMergeRoutePullRequest
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, f.canonical, "worktree", "remove", r.Candidate.Worktree)
	runEngineGit(t, f.canonical, "worktree", "remove", source.WorktreeDir)
	installStrandedLandingGH(t, r.PullRequest, f.repository.CloneURL)
	t.Setenv("WB_TEST_PR_STATE", "MERGED")
	t.Setenv("WB_TEST_CANDIDATE_SHA", r.Candidate.SHA)
	t.Setenv("WB_TEST_PR_HEAD_SHA", prHead)
	t.Setenv("WB_TEST_MERGE_COMMIT_SHA", landing)
	options := WorktreeMergeStrandedLandingAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Actor: "reviewer", Reason: "published predecessor is retained by merged candidate"}
	if _, err := AcknowledgeStrandedPullRequestLanding(t.Context(), options); err == nil || !strings.Contains(err.Error(), "does not match its exact preserved candidate") {
		t.Fatalf("default mismatch refusal=%v", err)
	}
	options.AllowPublishedPredecessor = true
	preview, err := AcknowledgeStrandedPullRequestLanding(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if preview.PublishedPredecessorSHA != predecessor || preview.CandidateSHA != r.Candidate.SHA || preview.PullRequestHeadSHA != prHead || preview.ProvedLandingSHA != landing || preview.ReceiptSHA256 == "" {
		t.Fatalf("incomplete proof: %+v", preview)
	}
	if _, err := os.Stat(preview.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("preview wrote: %v", err)
	}
	// A sibling published SHA or source cannot be vouched for by the merged PR.
	sibling := strings.TrimSpace(runEngineGit(t, f.canonical, "commit-tree", strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", r.TargetSHA+"^{tree}")), "-p", r.TargetSHA, "-m", "unrelated sibling"))
	runEngineGit(t, f.canonical, "push", "origin", sibling+":refs/heads/unrelated")
	for _, field := range []string{"predecessor", "source"} {
		changed := r
		changed.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
		if field == "predecessor" {
			changed.PublishedCandidateSHA = sibling
		} else {
			changed.Sources[0].SHA = sibling
		}
		if err := persistWorktreeMergeReceipt(changed); err != nil {
			t.Fatal(err)
		}
		if _, err := AcknowledgeStrandedPullRequestLanding(t.Context(), options); err == nil || !strings.Contains(err.Error(), "does not prove published predecessor/source") {
			t.Fatalf("sibling %s accepted: %v", field, err)
		}
	}
	if err := os.WriteFile(r.ReceiptPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_PR_STATE", "OPEN")
	if _, err := AcknowledgeStrandedPullRequestLanding(t.Context(), options); err == nil || !strings.Contains(err.Error(), "not MERGED") {
		t.Fatalf("open PR accepted: %v", err)
	}
	t.Setenv("WB_TEST_PR_STATE", "MERGED")
	options.Apply = true
	ack, err := AcknowledgeStrandedPullRequestLanding(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	again, err := AcknowledgeStrandedPullRequestLanding(t.Context(), options)
	if err != nil || again.ID != ack.ID {
		t.Fatalf("retry=%+v error=%v", again, err)
	}
	if claim, err := activeWorktreeMergeLaneReceipt(t.Context(), f.githubDir, filepath.Dir(r.ReceiptPath), r.Lane); err != nil || claim != nil {
		t.Fatalf("lane not released: %+v %v", claim, err)
	}
	after, err := os.ReadFile(r.ReceiptPath)
	if err != nil || string(after) != string(original) {
		t.Fatalf("historical receipt changed: %v", err)
	}
	for _, mutation := range []string{"missing predecessor", "wrong predecessor", "tree-only", "wrong status"} {
		changed := ack
		receipt := r
		switch mutation {
		case "missing predecessor":
			changed.PublishedPredecessorSHA = ""
		case "wrong predecessor":
			changed.PublishedPredecessorSHA = sibling
		case "tree-only":
			changed.CandidateLanding = "tree-identical"
		case "wrong status":
			receipt.Status = WorktreeMergePublished
		}
		changed.ID = strandedLandingAcknowledgementID(changed)
		content, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ack.AcknowledgementPath, content, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readStrandedLandingAcknowledgement(ack.AcknowledgementPath, receipt); err == nil {
			t.Fatalf("tampered %s accepted", mutation)
		}
	}
	if err := os.Remove(ack.AcknowledgementPath); err != nil {
		t.Fatal(err)
	}
	// A byte change during remote proof must never be bound to an older parsed receipt.
	readPR := func(ctx context.Context, dir string, args ...string) (string, error) {
		output, err := githubRead(ctx, dir, args...)
		if err == nil {
			if writeErr := os.WriteFile(r.ReceiptPath, append(original, '\n'), 0600); writeErr != nil {
				return "", writeErr
			}
		}
		return output, err
	}
	if _, err := acknowledgeStrandedPullRequestLanding(t.Context(), options, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, persistStrandedLandingAcknowledgement, readPR); err == nil || !strings.Contains(err.Error(), "changed during") {
		t.Fatalf("receipt race=%v", err)
	}
	if err := os.WriteFile(r.ReceiptPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	// Deterministically exercise pre-proof read/hash refusals without remote writes.
	for _, stage := range []string{"initial hash", "locked hash", "locked hash drift", "locked read", "locked shape"} {
		hashCalls, readCalls := 0, 0
		sentinel := errors.New(stage)
		hash := func(path string) (string, error) {
			hashCalls++
			if (stage == "initial hash" && hashCalls == 1) || (stage == "locked hash" && hashCalls == 2) {
				return "", sentinel
			}
			if stage == "locked hash drift" && hashCalls == 2 {
				return "changed", nil
			}
			return worktreeMergeReceiptSHA256(path)
		}
		read := func(path string) (WorktreeMergeReceipt, error) {
			readCalls++
			if stage == "locked read" && readCalls == 2 {
				return WorktreeMergeReceipt{}, sentinel
			}
			result, err := readWorktreeMergeReceipt(path)
			if stage == "locked shape" && readCalls == 2 {
				result.Status = WorktreeMergePublished
			}
			return result, err
		}
		if _, err := acknowledgeStrandedPullRequestLanding(t.Context(), options, read, hash, persistStrandedLandingAcknowledgement, githubRead); err == nil {
			t.Fatalf("%s accepted", stage)
		}
	}
	if _, err := os.Stat(ack.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("refusal wrote artifact: %v", err)
	}
}

func TestPublishedPredecessorReceiptShapeRemainsNarrow(t *testing.T) {
	t.Parallel()
	base := WorktreeMergeReceipt{ID: "id", ReceiptPath: "/receipt", Repository: "acme/app", Target: "main", TargetSHA: "target", Phase: WorktreeMergePhaseLand, Status: WorktreeMergeConflict, PullRequest: "1", PublishedCandidateSHA: "predecessor", Candidate: WorktreeMergeCandidate{SHA: "candidate"}, Route: WorktreeMergeRouteDecision{Route: WorktreeMergeRoutePullRequest}, Sources: []WorktreeMergeSource{{Task: "s", Worktree: "/s", Branch: "s", SHA: "source"}}}
	base.Lane = worktreeMergeLaneID(base.Repository, base.Target)
	if err := validateStrandedLandingReceiptWithPredecessor(base, base.ReceiptPath, true); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"status", "route", "exact SHA", "empty sources", "incomplete source"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			r := base
			r.Sources = append([]WorktreeMergeSource(nil), base.Sources...)
			switch change {
			case "status":
				r.Status = WorktreeMergePublished
			case "route":
				r.Route.Route = WorktreeMergeRouteDirect
			case "exact SHA":
				r.PublishedCandidateSHA = r.Candidate.SHA
			case "empty sources":
				r.Sources = nil
			case "incomplete source":
				r.Sources[0].SHA = ""
			}
			if err := validateStrandedLandingReceiptWithPredecessor(r, r.ReceiptPath, true); err == nil {
				t.Fatalf("%s accepted", change)
			}
		})
	}
	if err := proveStrandedPublishedPredecessor(t.Context(), base, "tree-identical"); err == nil {
		t.Fatal("tree-only proof accepted")
	}
}
