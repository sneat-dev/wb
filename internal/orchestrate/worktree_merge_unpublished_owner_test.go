package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Prepare is the genuine default engine: the invalid Go source produces a real
// validation_failed receipt. Each TOP shares only its private native baseline.
func unpublishedOwnerFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	writeEngineGoModule(t, f.canonical, "package app\n")
	runEngineGit(t, f.canonical, "add", "go.mod", "app.go")
	runEngineGit(t, f.canonical, "commit", "-m", "test: native unpublished validation baseline")
	runEngineGit(t, f.canonical, "push", "origin", "main")
	source := createMergeSource(t, f, "unpublished-owner-source", "feature/unpublished-owner", "candidate.go", "package app\n\nfunc Candidate() { missingCandidate }\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err == nil || r.Status != WorktreeMergeValidationFailed || r.Phase != WorktreeMergePhasePrepare || r.PullRequest != "" {
		t.Fatalf("native failed prepare: %+v %v", r, err)
	}
	return f, r
}
func unpublishedOwnerOptions(f engineFixture, r WorktreeMergeReceipt) WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions {
	return WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "actual unpublished candidate and preserved sources"}
}
func unpublishedOwnerCall(ctx context.Context, o WorktreeMergeUnpublishedValidationFailureAcknowledgementOptions, run runner.Runner) (WorktreeMergeUnpublishedValidationFailureAcknowledgement, error) {
	return acknowledgeUnpublishedValidationFailure(ctx, o, readWorktreeMergeReceipt, run, worktreeMergeReceiptSHA256, persistUnpublishedValidationFailureAcknowledgement, worktrees.CanonicalRepositoryPath)
}

func TestUnpublishedOwnerReceiptPolicyContracts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	r := WorktreeMergeReceipt{ReceiptPath: path, Repository: "acme/app", Target: "main", TargetSHA: strings.Repeat("a", 40), Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeValidationFailed, Candidate: WorktreeMergeCandidate{Worktree: "/candidate", Branch: "wb/candidate", SHA: strings.Repeat("c", 40)}, Sources: []WorktreeMergeSource{{Task: "source", Worktree: "/source", Branch: "feature/source", SHA: strings.Repeat("b", 40)}}}
	r.Lane = worktreeMergeLaneID(r.Repository, r.Target)
	r.ID = worktreeMergeOperationID(r.Lane, r.Sources)
	r.Candidate.Task = r.ID
	for _, status := range []WorktreeMergeStatus{WorktreeMergeValidationFailed, WorktreeMergePreparing} {
		copy := r
		copy.Status = status
		if err := validateUnpublishedValidationFailureReceipt(copy, path); err != nil {
			t.Fatalf("supported policy: %v", err)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt)
		want   string
	}{
		{"path", func(r *WorktreeMergeReceipt) { r.ReceiptPath = "different" }, "exact unpublished"},
		{"lane", func(r *WorktreeMergeReceipt) { r.Lane = "different" }, "exact unpublished"},
		{"phase", func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhaseLand }, "exact unpublished"},
		{"status", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }, "exact unpublished"},
		{"landing", func(r *WorktreeMergeReceipt) { r.LandingSHA = r.TargetSHA }, "exact unpublished"},
		{"pr", func(r *WorktreeMergeReceipt) { r.PullRequest = "17" }, "exact unpublished"},
		{"published", func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = r.Candidate.SHA }, "exact unpublished"},
		{"target sha", func(r *WorktreeMergeReceipt) { r.TargetSHA = "" }, "exact unpublished"},
		{"candidate task", func(r *WorktreeMergeReceipt) { r.Candidate.Task = "different" }, "exact unpublished"},
		{"candidate sha", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = "" }, "exact unpublished"},
		{"operation id", func(r *WorktreeMergeReceipt) { r.ID = "different"; r.Candidate.Task = r.ID }, "exact unpublished"},
		{"source task", func(r *WorktreeMergeReceipt) { r.Sources[0].Task = "" }, "incomplete immutable"},
		{"source worktree", func(r *WorktreeMergeReceipt) { r.Sources[0].Worktree = "" }, "incomplete immutable"},
		{"source branch", func(r *WorktreeMergeReceipt) { r.Sources[0].Branch = "" }, "incomplete immutable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			copy := r
			copy.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			tc.mutate(&copy)
			if err := validateUnpublishedValidationFailureReceipt(copy, path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("native DTO refusal: %v", err)
			}
		})
	}
}

func TestUnpublishedOwnerReadAdmissionAndReleasedLockContracts(t *testing.T) {
	t.Parallel()
	f, r := unpublishedOwnerFixture(t)
	verify := retirementOwnerReceiptBytes(t, r)
	o := unpublishedOwnerOptions(f, r)
	sentinel := errors.New("selected unpublished receipt read")
	for _, stage := range []string{"resolver", "first read", "first validation", "actor", "second read", "second validation", "lock"} {
		//nolint:paralleltest // Rows share one private native fixture and execute mutations/locks synchronously.
		t.Run(stage, func(t *testing.T) {
			options := o
			calls := 0
			read := func(path string) (WorktreeMergeReceipt, error) {
				calls++
				if stage == "first read" && calls == 1 || stage == "second read" && calls == 2 {
					return WorktreeMergeReceipt{}, sentinel
				}
				actual, err := readWorktreeMergeReceipt(path)
				if err == nil && (stage == "first validation" && calls == 1 || stage == "second validation" && calls == 2) {
					actual.Phase = WorktreeMergePhaseLand
				}
				return actual, err
			}
			if stage == "resolver" {
				options.Receipt = ""
			}
			if stage == "actor" {
				options.Actor = ""
			}
			var held OperationLock
			if stage == "lock" {
				var err error
				held, err = AcquireOperationLock(f.githubDir, r.Lane, true)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := held.Release(); err != nil {
						t.Error(err)
					}
				}()
			}
			_, err := acknowledgeUnpublishedValidationFailure(t.Context(), options, read, defaultRunner, worktreeMergeReceiptSHA256, persistUnpublishedValidationFailureAcknowledgement, worktrees.CanonicalRepositoryPath)
			if err == nil {
				t.Fatal("selected refusal accepted")
			}
			if strings.Contains(stage, "read") && !errors.Is(err, sentinel) {
				t.Fatalf("read identity lost: %v", err)
			}
			if stage == "resolver" && calls != 0 || stage == "actor" && calls != 1 || strings.HasPrefix(stage, "second") && calls != 2 {
				t.Fatalf("ordering %s reads=%d", stage, calls)
			}
			verify()
			if _, err := os.Stat(unpublishedValidationFailureAcknowledgementPath(r.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("refused sidecar: %v", err)
			}
			if stage != "lock" {
				retirementOwnerReleased(t, f.githubDir, r.Lane)
			}
		})
	}
}

func TestUnpublishedOwnerExactNativeGitAndWriteRefusals(t *testing.T) {
	t.Parallel()
	f, r := unpublishedOwnerFixture(t)
	verify := retirementOwnerReceiptBytes(t, r)
	o := unpublishedOwnerOptions(f, r)
	// Native immutable receipt/source/candidate are shared only by synchronous
	// rows. Each injected error is negative; other effects use their defaults.
	for _, stage := range []string{"clean", "HEAD", "remote publication", "target fetch", "target revision", "candidate ancestry", "hash", "persist"} {
		//nolint:paralleltest // Rows share one private native fixture and execute mutations/locks synchronously.
		t.Run(stage, func(t *testing.T) {
			sentinel := errors.New("selected unpublished stage " + stage)
			consumed := 0
			args := map[string][]string{"clean": {"status", "--porcelain=v1"}, "HEAD": {"rev-parse", "--verify", "HEAD^{commit}"}, "remote publication": {"ls-remote", "--heads", "origin", "refs/heads/" + r.Candidate.Branch}, "target fetch": {"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}, "target revision": {"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}, "candidate ancestry": {"merge-base", r.Candidate.SHA, r.TargetSHA}}
			run := retirementOwnerRunner{Runner: defaultRunner, before: func(_ context.Context, dir, name string, actual []string) error {
				if dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(actual, args[stage]) {
					consumed++
					return sentinel
				}
				return nil
			}}
			hash := worktreeMergeReceiptSHA256
			persist := persistUnpublishedValidationFailureAcknowledgement
			if stage == "hash" {
				hash = func(path string) (string, error) {
					if path != r.ReceiptPath {
						t.Fatal("hash path changed")
					}
					consumed++
					return "", sentinel
				}
			}
			if stage == "persist" {
				persist = func(path string, a WorktreeMergeUnpublishedValidationFailureAcknowledgement) error {
					if path != unpublishedValidationFailureAcknowledgementPath(r.ReceiptPath) || len(a.PreservedSources) != len(r.Sources) {
						t.Fatalf("native proof discarded: %+v", a)
					}
					consumed++
					return sentinel
				}
			}
			_, err := acknowledgeUnpublishedValidationFailure(t.Context(), o, readWorktreeMergeReceipt, run, hash, persist, worktrees.CanonicalRepositoryPath)
			if !errors.Is(err, sentinel) || consumed != 1 {
				t.Fatalf("exact native stage %s: %v consumed=%d", stage, err, consumed)
			}
			verify()
			retirementOwnerReleased(t, f.githubDir, r.Lane)
			if _, err := os.Stat(unpublishedValidationFailureAcknowledgementPath(r.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("refusal wrote sidecar: %v", err)
			}
		})
	}
}

func TestUnpublishedOwnerNativeCandidateAndSourceRefusals(t *testing.T) {
	t.Parallel()
	f, r := unpublishedOwnerFixture(t)
	verify := retirementOwnerReceiptBytes(t, r)
	o := unpublishedOwnerOptions(f, r)
	source := r.Sources[0]
	sourceHead := strings.TrimSpace(runEngineGit(t, source.Worktree, "rev-parse", "HEAD"))
	for _, stage := range []string{"guard missing", "guard branch", "dirty candidate", "changed HEAD", "dirty source", "rewritten source", "published", "landed"} {
		//nolint:paralleltest // Rows share one private native fixture and execute mutations/locks synchronously.
		t.Run(stage, func(t *testing.T) {
			var restore func(context.Context) error
			want := ""
			restored := false
			native := func(ctx context.Context, dir string, args ...string) error {
				_, _, err := runCommand(ctx, defaultRunner, 0, 0, dir, "git", args...)
				return err
			}
			restoreNow := func() error {
				if restored || restore == nil {
					return nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := restore(ctx); err != nil {
					return err
				}
				restored = true
				return nil
			}
			// Register before any physical mutation, including a failing setup command.
			t.Cleanup(func() {
				if err := restoreNow(); err != nil {
					t.Error(err)
				}
			})
			switch stage {
			case "guard missing":
				options := o
				var changed WorktreeMergeReceipt
				read := func(path string) (WorktreeMergeReceipt, error) {
					actual, err := readWorktreeMergeReceipt(path)
					changed = actual
					changed.Candidate.Worktree = filepath.Join(t.TempDir(), "absent")
					return changed, err
				}
				_, err := acknowledgeUnpublishedValidationFailure(t.Context(), options, read, defaultRunner, worktreeMergeReceiptSHA256, persistUnpublishedValidationFailureAcknowledgement, worktrees.CanonicalRepositoryPath)
				if err == nil || !strings.Contains(err.Error(), "guard candidate worktree") {
					t.Fatalf("actual absent Guard: %v", err)
				}
				verify()
				retirementOwnerReleased(t, f.githubDir, r.Lane)
				return
			case "guard branch":
				restore = func(ctx context.Context) error {
					return native(ctx, r.Candidate.Worktree, "checkout", r.Candidate.Branch)
				}
				runEngineGit(t, r.Candidate.Worktree, "checkout", "-b", "wrong-linked-identity")
				want = "exact linked-worktree identity"
			case "dirty candidate":
				path := filepath.Join(r.Candidate.Worktree, "dirty.txt")
				restore = func(context.Context) error { return os.Remove(path) }
				writeEngineFile(t, path, "native candidate dirty\n")
				want = "not clean"
			case "changed HEAD":
				restore = func(ctx context.Context) error {
					return native(ctx, r.Candidate.Worktree, "reset", "--hard", r.Candidate.SHA)
				}
				writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "changed.txt"), "native candidate advance\n")
				runEngineGit(t, r.Candidate.Worktree, "add", "changed.txt")
				runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "test: candidate advance")
				want = "does not match receipted SHA"
			case "dirty source":
				path := filepath.Join(source.Worktree, "dirty.txt")
				restore = func(context.Context) error { return os.Remove(path) }
				writeEngineFile(t, path, "native source dirty\n")
				want = "prove preserved source"
			case "rewritten source":
				restore = func(ctx context.Context) error { return native(ctx, source.Worktree, "reset", "--hard", sourceHead) }
				runEngineGit(t, source.Worktree, "reset", "--hard", r.TargetSHA)
				want = "was rewritten"
			case "published":
				restore = func(ctx context.Context) error {
					return native(ctx, r.Candidate.Worktree, "push", "origin", ":"+r.Candidate.Branch)
				}
				runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.Candidate.Branch)
				want = "is published"
			case "landed":
				restore = func(ctx context.Context) error {
					return native(ctx, r.Candidate.Worktree, "push", "--force", "origin", r.TargetSHA+":refs/heads/main")
				}
				runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.Candidate.SHA+":refs/heads/main")
				want = "already reachable"
			}
			_, err := unpublishedOwnerCall(t.Context(), o, defaultRunner)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("physical native refusal %s: %v want %s", stage, err, want)
			}
			if err := restoreNow(); err != nil {
				t.Fatal(err)
			}
			verify()
			retirementOwnerReleased(t, f.githubDir, r.Lane)
		})
	}
}

func TestUnpublishedOwnerNativeDiscardedProofAndCanonicalOrdering(t *testing.T) {
	t.Parallel()
	f, r := unpublishedOwnerFixture(t)
	// Record the interrupted prepare phase of this exact native candidate; real
	// Abort publishes the discarded backlog, never a synthetic positive DTO.
	r.Status = WorktreeMergePreparing
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	aborted, err := worktrees.Abort(t.Context(), worktrees.AbortOptions{ProjectsRoot: f.githubDir, Task: r.Candidate.Task, Base: r.Target, Apply: true, Disposition: worktrees.AbortDiscarded, DeleteRemote: true})
	if err != nil || len(aborted) != 1 || !aborted[0].Applied || !aborted[0].WorktreeGone || !aborted[0].BranchDeleted {
		t.Fatalf("native discard: %+v %v", aborted, err)
	}
	proof, err := worktrees.FindDiscardedLifecycleBacklogProof(t.Context(), f.githubDir, r.Repository, r.Target, r.Candidate.Task, r.Candidate.Worktree, r.Candidate.Branch, r.Candidate.SHA)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(proof.Path)
	if err != nil {
		t.Fatal(err)
	}
	verify := retirementOwnerReceiptBytes(t, r)
	o := unpublishedOwnerOptions(f, r)
	//nolint:paralleltest // Both rows re-observe the same private discarded backlog before publication.
	t.Run("incomplete backlog", func(t *testing.T) {
		changed := bytes.Replace(before, []byte(`"stage": "complete"`), []byte(`"stage": "worktree_removed"`), 1)
		if bytes.Equal(changed, before) {
			t.Fatal("real completed backlog stage missing")
		}
		t.Cleanup(func() {
			if err := os.WriteFile(proof.Path, before, 0o600); err != nil {
				t.Error(err)
			}
		})
		if err := os.WriteFile(proof.Path, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		canonicalCalls := 0
		canonical := func(root, repo string) (string, error) {
			canonicalCalls++
			return worktrees.CanonicalRepositoryPath(root, repo)
		}
		_, err := acknowledgeUnpublishedValidationFailure(t.Context(), o, readWorktreeMergeReceipt, defaultRunner, worktreeMergeReceiptSHA256, persistUnpublishedValidationFailureAcknowledgement, canonical)
		if err == nil || !strings.Contains(err.Error(), "prove discarded interrupted candidate") || canonicalCalls != 0 {
			t.Fatalf("native backlog must precede canonical: %v calls=%d", err, canonicalCalls)
		}
		if err := os.WriteFile(proof.Path, before, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	//nolint:paralleltest // This row follows restoration of the same private discarded backlog.
	t.Run("late canonical refusal", func(t *testing.T) {
		sentinel := errors.New("selected canonical refusal after actual discard")
		calls := 0
		_, err := acknowledgeUnpublishedValidationFailure(t.Context(), o, readWorktreeMergeReceipt, defaultRunner, worktreeMergeReceiptSHA256, persistUnpublishedValidationFailureAcknowledgement, func(root, repo string) (string, error) {
			calls++
			if root != f.githubDir || repo != r.Repository {
				t.Fatal("canonical inputs collapsed")
			}
			return "", sentinel
		})
		if !errors.Is(err, sentinel) || calls != 1 {
			t.Fatalf("canonical identity/order: %v calls=%d", err, calls)
		}
	})
	ack, err := AcknowledgeUnpublishedValidationFailure(t.Context(), o)
	if err != nil || ack.CandidateCleanupBacklog != proof.Path || len(ack.PreservedSources) != len(r.Sources) {
		t.Fatalf("native discarded positive proof: %+v %v", ack, err)
	}
	verify()
	retirementOwnerReleased(t, f.githubDir, r.Lane)
}

func TestUnpublishedOwnerAuthenticatedReplayAndMismatchContracts(t *testing.T) {
	t.Parallel()
	f, r := unpublishedOwnerFixture(t)
	verify := retirementOwnerReceiptBytes(t, r)
	o := unpublishedOwnerOptions(f, r)
	path := unpublishedValidationFailureAcknowledgementPath(r.ReceiptPath)
	o.Apply = false
	dry, err := AcknowledgeUnpublishedValidationFailure(t.Context(), o)
	if err != nil || dry.ID == "" || len(dry.PreservedSources) != 1 {
		t.Fatalf("native dryrun proof: %+v %v", dry, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dryrun wrote sidecar: %v", err)
	}
	o.Apply = true
	applied, err := AcknowledgeUnpublishedValidationFailure(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := readUnpublishedValidationFailureAcknowledgement(path, r)
	if err != nil || loaded.ID != applied.ID {
		t.Fatalf("actual saved proof invalid: %+v %v", loaded, err)
	}
	again, err := AcknowledgeUnpublishedValidationFailure(t.Context(), o)
	if err != nil || again.ID != applied.ID {
		t.Fatalf("same-proof replay: %+v %v", again, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A different preserved source SHA is still accepted by the sidecar parser,
	// which authenticates its recomputed ID. The owner must then refuse mismatch.
	altered := applied
	altered.PreservedSources = append([]WorktreeMergeSource(nil), applied.PreservedSources...)
	altered.PreservedSources[0].SHA = r.TargetSHA
	altered.ID = unpublishedValidationFailureAcknowledgementID(altered)
	if err := persistUnpublishedValidationFailureAcknowledgement(path, altered); err != nil {
		t.Fatal(err)
	}
	if parsed, err := readUnpublishedValidationFailureAcknowledgement(path, r); err != nil || parsed.ID != altered.ID {
		t.Fatalf("valid alternate sidecar preflight: %+v %v", parsed, err)
	}
	_, err = AcknowledgeUnpublishedValidationFailure(t.Context(), o)
	if err == nil || !strings.Contains(err.Error(), "binds different immutable evidence") {
		t.Fatalf("owner mismatch not reached: %v", err)
	}
	if err := os.WriteFile(path, []byte("not JSON\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = AcknowledgeUnpublishedValidationFailure(t.Context(), o)
	if err == nil {
		t.Fatal("malformed actual sidecar accepted")
	}
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	verify()
	retirementOwnerReleased(t, f.githubDir, r.Lane)
}
