package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/runner"
)

func TestPostTargetFailureAcknowledgesOnlyAlreadyLandedSourceDescendants(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "descendant-source", "feature/descendant-source", "source.txt", "original\n")
	r, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, source.WorktreeDir, "commit", "--allow-empty", "-m", "advance source before landing")
	advanced := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	// Model a PR that merged an advanced source while the receipt retained its
	// original candidate/source identity, including a distinct merge landing.
	runEngineGit(t, f.canonical, "merge", "--no-ff", "--no-edit", advanced)
	landing := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, f.canonical, "push", "origin", "main")
	r.Phase, r.Status, r.LandingSHA = WorktreeMergePhaseLand, WorktreeMergePostTargetCIFailed, landing
	r.Checks = githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitFailed, Head: landing}
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	options := WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Actor: "reviewer", Reason: "clean descendant already landed"}
	if _, err := AcknowledgeLandedMergeFailure(t.Context(), options); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("default accepted advanced source: %v", err)
	}
	options.AllowLandedSourceDescendants = true
	preview, err := AcknowledgeLandedMergeFailure(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.ObservedSources) != 1 || preview.ObservedSources[0].SHA != advanced || preview.Sources[0].SHA != r.Sources[0].SHA || preview.ReceiptSHA256 == "" || preview.ReceiptLandingSHA != landing {
		t.Fatalf("incomplete evidence: %+v", preview)
	}
	if _, err := os.Stat(preview.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote sidecar: %v", err)
	}
	// Dirty state and an unlanded descendant must still fail without artifacts.
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "dirty.txt"), "dirty\n")
	if _, err := AcknowledgeLandedMergeFailure(t.Context(), options); err == nil || !strings.Contains(err.Error(), "not clean") {
		t.Fatalf("dirty source: %v", err)
	}
	if err := os.Remove(filepath.Join(source.WorktreeDir, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, source.WorktreeDir, "commit", "--allow-empty", "-m", "unlanded advance")
	if _, err := AcknowledgeLandedMergeFailure(t.Context(), options); err == nil || !strings.Contains(err.Error(), "does not contain observed source") {
		t.Fatalf("unlanded descendant: %v", err)
	}
	runEngineGit(t, source.WorktreeDir, "reset", "--hard", advanced)
	options.Apply = true
	ack, err := AcknowledgeLandedMergeFailure(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := readLandedFailureAcknowledgement(ack.AcknowledgementPath, r)
	if err != nil || stored.ID != ack.ID {
		t.Fatalf("stored evidence=%+v error=%v", stored, err)
	}
	again, err := AcknowledgeLandedMergeFailure(t.Context(), options)
	if err != nil || again.ID != ack.ID {
		t.Fatalf("retry=%+v error=%v", again, err)
	}
	if current := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); current != advanced {
		t.Fatal("source head was modified")
	}
	after, err := os.ReadFile(r.ReceiptPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("receipt changed: %v", err)
	}
	if claim, err := ActiveMergeLaneClaim(f.githubDir, f.repository.Slug, r.Sources[0].Branch); err != nil || claim != nil {
		t.Fatalf("lane still claimed: %+v %v", claim, err)
	}
	// A digest-bound acknowledgement cannot survive a historical receipt edit.
	if err := os.WriteFile(r.ReceiptPath, append(after, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLandedFailureAcknowledgement(ack.AcknowledgementPath, r); err == nil {
		t.Fatal("changed receipt accepted")
	}
	if err := os.WriteFile(r.ReceiptPath, after, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(r.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	if _, err := readLandedFailureAcknowledgement(ack.AcknowledgementPath, r); err == nil {
		t.Fatal("missing receipt accepted")
	}
	if _, err := AcknowledgeLandedMergeFailure(t.Context(), options); err == nil {
		t.Fatal("missing receipt hashed")
	}
	if err := os.WriteFile(r.ReceiptPath, after, 0600); err != nil {
		t.Fatal(err)
	}
	withoutObserved := ack
	withoutObserved.ObservedSources = nil
	contentsWithoutObserved, err := json.Marshal(withoutObserved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ack.AcknowledgementPath, contentsWithoutObserved, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLandedFailureAcknowledgement(ack.AcknowledgementPath, r); err == nil {
		t.Fatal("digest without observed sources accepted")
	}
	// Even with a recomputed ID, a changed source identity is rejected.
	tampered := ack
	tampered.ObservedSources = append([]WorktreeMergeSource(nil), ack.ObservedSources...)
	tampered.ObservedSources[0].Branch = "other"
	tampered.ID = landedFailureAcknowledgementID(tampered)
	contents, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ack.AcknowledgementPath, contents, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLandedFailureAcknowledgement(ack.AcknowledgementPath, r); err == nil {
		t.Fatal("changed source identity accepted")
	}
	if err := os.Remove(ack.AcknowledgementPath); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, source.WorktreeDir, "reset", "--hard", r.TargetSHA)
	if _, err := AcknowledgeLandedMergeFailure(t.Context(), options); err == nil || !strings.Contains(err.Error(), "rewritten after landing") {
		t.Fatalf("rewritten source: %v", err)
	}
}

func TestSourceDescendantRecoveryRefusesOtherReceiptStatuses(t *testing.T) {
	t.Parallel()
	f, r := landedFailureOwnerFixture(t)
	_, err := AcknowledgeLandedMergeFailure(t.Context(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, AllowLandedSourceDescendants: true})
	if err == nil || !strings.Contains(err.Error(), "requires a landed_post_target_ci_failed receipt") {
		t.Fatalf("status refusal=%v", err)
	}
}

func TestSourceDescendantRecoveryRechecksHeadsAndReceiptBeforePublication(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"source moves", "receipt changes", "receipt disappears", "containment fails", "candidate removed"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			f, r := landedFailureOwnerFixture(t)
			r.Phase, r.Status, r.LandingSHA = WorktreeMergePhaseLand, WorktreeMergePostTargetCIFailed, r.Candidate.SHA
			r.Checks = githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitFailed, Head: r.LandingSHA}
			if err := persistWorktreeMergeReceipt(r); err != nil {
				t.Fatal(err)
			}
			options := WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, AllowLandedSourceDescendants: true, Apply: true, Actor: "reviewer", Reason: "test native evidence drift"}
			if change == "candidate removed" {
				runEngineGit(t, f.canonical, "worktree", "remove", r.Candidate.Worktree)
				if _, err := AcknowledgeLandedMergeFailure(t.Context(), options); err == nil || !strings.Contains(err.Error(), "requires the exact live candidate") {
					t.Fatalf("missing candidate=%v", err)
				}
				return
			}
			calls := 0
			run := &sourceDescendantProofRunner{Runner: defaultRunner, after: func(dir string, args []string) error {
				if dir == r.Candidate.Worktree && len(args) > 0 && args[0] == "merge-base" && args[1] == r.Sources[0].SHA && args[2] == r.Candidate.SHA {
					calls++
					// Candidate source, target candidate, target landing, observed source.
					if calls == 4 {
						switch change {
						case "source moves":
							runEngineGit(t, r.Sources[0].Worktree, "commit", "--allow-empty", "-m", "concurrent source advance")
						case "receipt changes":
							contents, err := os.ReadFile(r.ReceiptPath)
							if err != nil {
								return err
							}
							return os.WriteFile(r.ReceiptPath, append(contents, '\n'), 0600)
						case "receipt disappears":
							return os.Remove(r.ReceiptPath)
						case "containment fails":
							return errors.New("native containment unavailable")
						}
					}
				}
				return nil
			}}
			_, err := acknowledgeLandedMergeFailure(t.Context(), options, run, readWorktreeMergeReceipt, func(string, WorktreeMergeLandedFailureAcknowledgement) error {
				t.Fatal("drift was published")
				return nil
			})
			if err == nil || calls != 4 {
				t.Fatalf("drift=%s calls=%d error=%v", change, calls, err)
			}
			if _, err := os.Stat(landedFailureAcknowledgementPath(r.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("refusal wrote artifact: %v", err)
			}
		})
	}
}

type sourceDescendantProofRunner struct {
	runner.Runner
	after func(string, []string) error
}

func (r *sourceDescendantProofRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
	if err == nil && name == "git" {
		if err := r.after(dir, args); err != nil {
			return runner.Result{}, err
		}
	}
	return result, err
}
