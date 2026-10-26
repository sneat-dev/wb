package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func acknowledgeUnpublishedConflictFixture(t *testing.T, options WorktreeMergeUnpublishedConflictOptions) (WorktreeMergeUnpublishedConflictAcknowledgement, error) {
	t.Helper()
	ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{Read: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) < 2 || args[0] != "pr" || args[1] != "list" || !strings.Contains(strings.Join(args, " "), "--state all") {
			t.Fatalf("unexpected GitHub publication query: %v", args)
		}
		return []byte("[]"), nil
	}})
	return AcknowledgeUnpublishedConflict(ctx, options)
}

func unpublishedConflictFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, WorktreeMergeUnpublishedConflictOptions) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "preserved-source", "feature/preserved", "feature.txt", "preserve this work\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil || r.Status != WorktreeMergePrepared {
		t.Fatalf("prepare native fixture: %+v %v", r, err)
	}
	r.Phase, r.Status = WorktreeMergePhaseLand, WorktreeMergeConflict
	r.Failure = "historical nonrecoverable land/conflict"
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	return f, r, WorktreeMergeUnpublishedConflictOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Actor: "test-operator", Reason: "preserve historical unpublished conflict"}
}

func TestUnpublishedConflictAcknowledgementPreservesWorkAndReleasesExactLane(t *testing.T) {
	t.Parallel()
	f, r, options := unpublishedConflictFixture(t)
	original, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := acknowledgeUnpublishedConflictFixture(t, options)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ID == "" || len(preview.Claims) != 2 || preview.Candidate != r.Candidate {
		t.Fatal(preview)
	}
	if _, err := os.Stat(preview.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("preview wrote sidecar: %v", err)
	}
	if claim, err := ActiveMergeLaneClaim(f.githubDir, r.Repository, r.Sources[0].Branch); err != nil || claim == nil {
		t.Fatalf("preview released claim: %+v %v", claim, err)
	}
	options.Apply = true
	if _, err := acknowledgeUnpublishedConflictFixture(t, options); err == nil {
		t.Fatal("apply skipped reviewed proof")
	}
	options.ExpectedProofID = preview.ID
	ack, err := acknowledgeUnpublishedConflictFixture(t, options)
	if err != nil {
		t.Fatal(err)
	}
	ackBytes, err := os.ReadFile(ack.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	if claim, err := ActiveMergeLaneClaim(f.githubDir, r.Repository, r.Sources[0].Branch); err != nil || claim != nil {
		t.Fatalf("valid sidecar retained branch claim: %+v %v", claim, err)
	}
	if claim, err := activeWorktreeMergeLaneReceipt(t.Context(), f.githubDir, filepath.Dir(r.ReceiptPath), r.Lane); err != nil || claim != nil {
		t.Fatalf("valid sidecar retained lane: %+v %v", claim, err)
	}
	if _, err := LandWorktreeMerge(t.Context(), WorktreeMergeLandOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath}); err == nil || !strings.Contains(err.Error(), "cannot be replayed") {
		t.Fatalf("historical replay: %v", err)
	}
	again, err := acknowledgeUnpublishedConflictFixture(t, options)
	if err != nil || again.RecordedAt != ack.RecordedAt {
		t.Fatalf("immutable retry: %+v %v", again, err)
	}
	for _, asset := range append([]WorktreeMergeSource{{Task: r.Candidate.Task, Worktree: r.Candidate.Worktree, Branch: r.Candidate.Branch, SHA: r.Candidate.SHA}}, r.Sources...) {
		if head := strings.TrimSpace(runEngineGit(t, asset.Worktree, "rev-parse", "HEAD")); head != asset.SHA {
			t.Fatalf("changed preserved HEAD %s", head)
		}
		if status := runEngineGit(t, asset.Worktree, "status", "--porcelain"); strings.TrimSpace(status) != "" {
			t.Fatal(status)
		}
	}
	current, err := os.ReadFile(r.ReceiptPath)
	if err != nil || !bytes.Equal(original, current) {
		t.Fatalf("rewrote historical receipt: %v", err)
	}
	current, err = os.ReadFile(ack.AcknowledgementPath)
	if err != nil || !bytes.Equal(ackBytes, current) {
		t.Fatalf("rewrote acknowledgement: %v", err)
	}
	// Both scanners must reject tampering rather than interpreting it as release.
	corrupt := ack
	corrupt.ReceiptSHA256 = strings.Repeat("0", 64)
	data, _ := json.Marshal(corrupt)
	if err := os.WriteFile(ack.AcknowledgementPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ActiveMergeLaneClaim(f.githubDir, r.Repository, r.Sources[0].Branch); err == nil {
		t.Fatal("tampered sidecar released branch")
	}
	if _, err := activeWorktreeMergeLaneReceipt(t.Context(), f.githubDir, filepath.Dir(r.ReceiptPath), r.Lane); err == nil {
		t.Fatal("tampered sidecar released lane")
	}
	if _, err := LandWorktreeMerge(t.Context(), WorktreeMergeLandOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath}); err == nil {
		t.Fatal("tampered sidecar allowed historical replay")
	}
	if _, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{r.Sources[0].Worktree}, Target: r.Target, Model: "test-model", AgentRuntime: "test"}); err == nil {
		t.Fatal("tampered sidecar allowed successor preparation")
	}
	if err := os.WriteFile(ack.AcknowledgementPath, ackBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{r.Sources[0].Worktree}, Target: r.Target, Model: "test-model", AgentRuntime: "test"})
	if err != nil || next.ReceiptPath == r.ReceiptPath || next.Candidate.Worktree == r.Candidate.Worktree {
		t.Fatalf("fresh independent prepare: %+v %v", next, err)
	}
	if claim, err := ActiveMergeLaneClaim(f.githubDir, r.Repository, r.Sources[0].Branch); err != nil || claim == nil || claim.ReceiptPath != next.ReceiptPath {
		t.Fatalf("acknowledgement released a different receipt: %+v %v", claim, err)
	}
}

func TestUnpublishedConflictApplyRejectsDriftFromReviewedPreview(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"target", "claim", "receipt", "native lock"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, r, o := unpublishedConflictFixture(t)
			preview, err := acknowledgeUnpublishedConflictFixture(t, o)
			if err != nil {
				t.Fatal(err)
			}
			o.Apply, o.ExpectedProofID = true, preview.ID
			switch kind {
			case "target":
				runEngineGit(t, f.canonical, "commit", "--allow-empty", "-m", "target advanced after preview")
				runEngineGit(t, f.canonical, "push", "origin", "main")
			case "claim":
				path := preview.Claims[0].ClaimPath
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var claim map[string]any
				if err := json.Unmarshal(data, &claim); err != nil {
					t.Fatal(err)
				}
				claim["model"] = "changed-audited-model"
				data, err = json.Marshal(claim)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "receipt":
				r.Failure = "different historical evidence"
				if err := persistWorktreeMergeReceipt(r); err != nil {
					t.Fatal(err)
				}
			case "native lock":
				lock, err := AcquireOperationLock(f.githubDir, r.Lane, true)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.Release() }()
			}
			if _, err := acknowledgeUnpublishedConflictFixture(t, o); err == nil {
				t.Fatal("changed reviewed proof accepted")
			}
			if _, err := os.Stat(preview.AcknowledgementPath); !os.IsNotExist(err) {
				t.Fatal("refusal wrote acknowledgement")
			}
		})
	}
}

func TestUnpublishedConflictRecoveryRefusesChangedOrPublishedWork(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, engineFixture, *WorktreeMergeReceipt, *WorktreeMergeUnpublishedConflictOptions)
	}{
		{"dirty candidate", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt, _ *WorktreeMergeUnpublishedConflictOptions) {
			writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "dirty"), "dirty")
		}},
		{"dirty source", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt, _ *WorktreeMergeUnpublishedConflictOptions) {
			writeEngineFile(t, filepath.Join(r.Sources[0].Worktree, "dirty"), "dirty")
		}},
		{"active owner", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt, _ *WorktreeMergeUnpublishedConflictOptions) {
			if err := worktrees.RecordCustody(r.Candidate.Worktree, r.Candidate.Task, "test active owner", worktrees.AgentIdentity{Runtime: "test", Model: "test", PID: os.Getpid()}); err != nil {
				t.Fatal(err)
			}
		}},
		{"advanced candidate", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt, _ *WorktreeMergeUnpublishedConflictOptions) {
			runEngineGit(t, r.Candidate.Worktree, "commit", "--allow-empty", "-m", "advance")
		}},
		{"advanced source", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt, _ *WorktreeMergeUnpublishedConflictOptions) {
			runEngineGit(t, r.Sources[0].Worktree, "commit", "--allow-empty", "-m", "advance")
		}},
		{"published candidate", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt, _ *WorktreeMergeUnpublishedConflictOptions) {
			runEngineGit(t, r.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+r.Candidate.Branch)
		}},
		{"candidate landed", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt, _ *WorktreeMergeUnpublishedConflictOptions) {
			runEngineGit(t, r.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/main")
		}},
		{"mismatched root", func(t *testing.T, _ engineFixture, _ *WorktreeMergeReceipt, o *WorktreeMergeUnpublishedConflictOptions) {
			o.ProjectsRoot = t.TempDir()
		}},
		{"missing candidate", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt, _ *WorktreeMergeUnpublishedConflictOptions) {
			if err := os.Rename(r.Candidate.Worktree, r.Candidate.Worktree+"-preserved"); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing source", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt, _ *WorktreeMergeUnpublishedConflictOptions) {
			if err := os.Rename(r.Sources[0].Worktree, r.Sources[0].Worktree+"-preserved"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, r, o := unpublishedConflictFixture(t)
			tc.mutate(t, f, &r, &o)
			if _, err := acknowledgeUnpublishedConflictFixture(t, o); err == nil {
				t.Fatal("unsafe recovery accepted")
			}
			if _, err := os.Stat(r.ReceiptPath + worktreeMergeUnpublishedConflictSuffix); !os.IsNotExist(err) {
				t.Fatal("refusal wrote sidecar")
			}
		})
	}
}

func TestUnpublishedConflictReceiptPolicyRefusesRecoverableAndIncompleteShapes(t *testing.T) {
	t.Parallel()
	_, r, _ := unpublishedConflictFixture(t)
	for _, change := range []func(*WorktreeMergeReceipt){
		func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhasePrepare }, func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }, func(r *WorktreeMergeReceipt) { r.Candidate.SHA = "" }, func(r *WorktreeMergeReceipt) { r.PullRequest = "17" }, func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = r.Candidate.SHA }, func(r *WorktreeMergeReceipt) { r.LandingSHA = r.Candidate.SHA }, func(r *WorktreeMergeReceipt) { r.Candidate.Task = "other" }, func(r *WorktreeMergeReceipt) { r.Lane = "other" }, func(r *WorktreeMergeReceipt) { r.Sources[0].Task = "" }, func(r *WorktreeMergeReceipt) { r.Candidate.Branch = r.Target },
	} {
		copy := r
		copy.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
		change(&copy)
		if err := validateUnpublishedConflictReceipt(copy); err == nil {
			t.Fatalf("accepted %+v", copy)
		}
	}
}
