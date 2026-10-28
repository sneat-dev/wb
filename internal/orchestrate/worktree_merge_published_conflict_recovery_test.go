package orchestrate

import (
	"context"
	"encoding/json"
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

// newPublishedPrepareConflictFixture builds the durable legacy shape this
// recovery owns, entirely from real private Git: a validated candidate P that
// was published through an open pull request, an advanced source S, and a clean
// descendant H that merges S into P. The receipt is then rewritten to the
// shape a failed prepare refresh used to persist: prepare/conflict with no
// recorded candidate, but the pull request and published predecessor retained.
func newPublishedPrepareConflictFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, string, string) {
	t.Helper()
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "published-conflict-source", "feature/published-conflict", "source.txt", "source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	published := receipt.Candidate.SHA
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", published+":refs/heads/"+receipt.Candidate.Branch)
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "advance.txt"), "advance\n")
	runEngineGit(t, source.WorktreeDir, "add", "advance.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "feat: advance the published conflict source")
	advanced := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	runEngineGit(t, receipt.Candidate.Worktree, "merge", "--no-edit", advanced)
	head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))

	receipt.Phase, receipt.Status = WorktreeMergePhasePrepare, WorktreeMergeConflict
	receipt.Sources[0].SHA = advanced
	receipt.Candidate.SHA = ""
	receipt.PullRequest = "https://example.test/acme/app/pull/41"
	receipt.PublishedCandidateSHA = published
	receipt.Failure = "prepare refresh failed before recording the refreshed head"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	return fixture, receipt, published, head
}

func assertNativeAncestor(t *testing.T, directory, ancestor, descendant string) {
	t.Helper()
	if got := strings.TrimSpace(runEngineGit(t, directory, "merge-base", ancestor, descendant)); got != ancestor {
		t.Fatalf("native merge-base(%s, %s)=%s, want the recorded ancestor", ancestor, descendant, got)
	}
}

// clonePublishedConflictReceipt isolates per-row source mutations from the
// fixture receipt's shared slice.
func clonePublishedConflictReceipt(r WorktreeMergeReceipt) WorktreeMergeReceipt {
	r.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
	return r
}

//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestPublishedPrepareConflictRecoveryProvesExactPredecessorWithoutRecordingEarly(t *testing.T) {
	fixture, receipt, published, head := newPublishedPrepareConflictFixture(t)
	installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
	before := receipt

	changed, err := recoverPublishedPrepareConflictCandidate(t.Context(), fixture.githubDir, &receipt, 5*time.Second, 0)
	if err != nil || !changed {
		t.Fatalf("actual published prepare recovery changed=%v error=%v", changed, err)
	}
	if receipt.Candidate.SHA != head || receipt.Status != WorktreeMergePreparing || receipt.Failure != "" ||
		receipt.PullRequest != before.PullRequest || receipt.PublishedCandidateSHA != published || receipt.TargetSHA != before.TargetSHA {
		t.Fatalf("recovered published candidate identity=%+v", receipt)
	}
	// The proof is a read: the durable receipt still records the exact
	// publication and waits for validation before any new candidate is saved.
	durable, err := readWorktreeMergeReceipt(before.ReceiptPath)
	if err != nil || durable.Candidate.SHA != "" || durable.Status != WorktreeMergeConflict || durable.PublishedCandidateSHA != published || durable.PullRequest != before.PullRequest {
		t.Fatalf("recovery wrote before validation: %+v error=%v", durable, err)
	}
	for _, root := range []string{published, receipt.TargetSHA, receipt.Sources[0].SHA} {
		assertNativeAncestor(t, receipt.Candidate.Worktree, root, head)
	}
}

//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestPublishedPrepareConflictRecoveryAdmitsOnlyTheFailedRefreshShape(t *testing.T) {
	fixture, receipt, published, head := newPublishedPrepareConflictFixture(t)
	installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)

	if changed, err := recoverPublishedPrepareConflictCandidateWithRunner(t.Context(), defaultRunner, fixture.githubDir, nil, 5*time.Second, 0); changed || err != nil {
		t.Fatalf("nil receipt changed=%v error=%v", changed, err)
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*WorktreeMergeReceipt)
		wantErr string
		wantOK  bool
	}{
		{"land phase", func(r *WorktreeMergeReceipt) { r.Phase = WorktreeMergePhaseLand }, "", false},
		{"prepared status", func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared }, "", false},
		{"no pull request", func(r *WorktreeMergeReceipt) { r.PullRequest = "" }, "", false},
		{"no published predecessor", func(r *WorktreeMergeReceipt) { r.PublishedCandidateSHA = "" }, "", false},
		{"landing recorded", func(r *WorktreeMergeReceipt) { r.LandingSHA = r.TargetSHA }, "", false},
		{"incomplete receipt identity", func(r *WorktreeMergeReceipt) { r.ID = "" }, "not an exact unlanded published prepare conflict", false},
		{"incomplete source identity", func(r *WorktreeMergeReceipt) { r.Sources[0].SHA = "" }, "incomplete receipted source", false},
		{"ordinary published conflict already contains its source", func(r *WorktreeMergeReceipt) { r.Candidate.SHA = head }, "", false},
	} {
		//nolint:paralleltest // Each case builds its own t.Setenv real-Git fixture in the shared private lane.
		t.Run(tc.name, func(t *testing.T) {
			current := clonePublishedConflictReceipt(receipt)
			tc.mutate(&current)
			before := current
			changed, err := recoverPublishedPrepareConflictCandidateWithRunner(t.Context(), defaultRunner, fixture.githubDir, &current, 5*time.Second, 0)
			if tc.wantErr != "" {
				if changed || err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("refusal changed=%v error=%v want %q", changed, err, tc.wantErr)
				}
				return
			}
			if tc.wantOK {
				t.Fatal("unreachable")
			}
			if changed || err != nil || !reflect.DeepEqual(current, before) {
				t.Fatalf("unowned shape changed=%v error=%v receipt=%+v", changed, err, current)
			}
		})
	}
}

//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestPublishedPrepareConflictRecoveryRefusesUnprovenNativeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, engineFixture, *WorktreeMergeReceipt)
		want   string
	}{
		{"missing candidate worktree", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt) {
			r.Candidate.Worktree = filepath.Join(t.TempDir(), "absent")
		}, "guard published prepare candidate"},
		{"candidate branch", func(_ *testing.T, _ engineFixture, r *WorktreeMergeReceipt) {
			r.Candidate.Branch = "wb/other-candidate"
		}, "does not match WB Guard"},
		{"canonical repository", func(_ *testing.T, _ engineFixture, r *WorktreeMergeReceipt) { r.Repository = "acme/other" }, "canonical repository does not match"},
		{"invalid repository", func(_ *testing.T, _ engineFixture, r *WorktreeMergeReceipt) { r.Repository = "../outside" }, "safe path segments"},
		{"claim owner identity", func(_ *testing.T, _ engineFixture, r *WorktreeMergeReceipt) { r.Candidate.Task = "different-owner" }, "active Work Log claim"},
		{"dirty candidate", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "dirty.txt"), "dirty\n")
		}, "worktree is dirty"},
		{"dirty source", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(r.Sources[0].Worktree, "dirty.txt"), "dirty\n")
		}, "source"},
		{"advanced source head", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(r.Sources[0].Worktree, "later.txt"), "later\n")
			runEngineGit(t, r.Sources[0].Worktree, "add", "later.txt")
			runEngineGit(t, r.Sources[0].Worktree, "commit", "-m", "test: drift the receipted source")
		}, "advanced from"},
		{"rewritten source snapshot", func(_ *testing.T, f engineFixture, r *WorktreeMergeReceipt) {
			r.Sources[0].SHA = strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
		}, "source"},
		{"target drift", func(t *testing.T, f engineFixture, _ *WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(f.canonical, "drift.txt"), "drift\n")
			runEngineGit(t, f.canonical, "add", "drift.txt")
			runEngineGit(t, f.canonical, "commit", "-m", "test: drift the recorded target")
			runEngineGit(t, f.canonical, "push", "origin", "main")
		}, "target drifted"},
		{"missing published predecessor ancestry", func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) {
			extra := createMergeSource(t, f, "unrelated-predecessor", "feature/unrelated-predecessor", "other.txt", "other\n")
			r.PublishedCandidateSHA = strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))
		}, "does not contain required immutable root"},
		{"missing recorded candidate ancestry", func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) {
			extra := createMergeSource(t, f, "unrelated-recorded", "feature/unrelated-recorded", "other.txt", "other\n")
			r.Candidate.SHA = strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))
		}, "does not contain required immutable root"},
		{"missing source ancestry", func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) {
			extra := createMergeSource(t, f, "unrelated-source", "feature/unrelated-source", "other.txt", "other\n")
			r.Sources = []WorktreeMergeSource{{Task: "unrelated-source", Worktree: extra.WorktreeDir, Branch: extra.Branch, SHA: strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))}}
		}, "does not contain required immutable root"},
	} {
		//nolint:paralleltest // Each case builds its own t.Setenv real-Git fixture in the shared private lane.
		t.Run(tc.name, func(t *testing.T) {
			fixture, receipt, published, _ := newPublishedPrepareConflictFixture(t)
			installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
			tc.mutate(t, fixture, &receipt)
			before := receipt
			changed, err := recoverPublishedPrepareConflictCandidate(t.Context(), fixture.githubDir, &receipt, 5*time.Second, 0)
			if changed || err == nil || !strings.Contains(err.Error(), tc.want) || receipt.Candidate.SHA != before.Candidate.SHA ||
				receipt.PublishedCandidateSHA != before.PublishedCandidateSHA || receipt.PullRequest != before.PullRequest {
				t.Fatalf("native refusal changed=%v error=%v receipt=%+v want %q", changed, err, receipt, tc.want)
			}
			durable, readErr := readWorktreeMergeReceipt(before.ReceiptPath)
			if readErr != nil || durable.Candidate.SHA != "" || durable.PublishedCandidateSHA != published || durable.Status != WorktreeMergeConflict {
				t.Fatalf("refusal changed durable receipt: %+v error=%v", durable, readErr)
			}
		})
	}
}

//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestPublishedPrepareConflictRecoveryRefusesUnprovenPullRequestIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, engineFixture, *WorktreeMergeReceipt)
		env    map[string]string
		want   string
	}{
		{"closed pull request", func(_ *testing.T, _ engineFixture, _ *WorktreeMergeReceipt) {}, map[string]string{"WB_TEST_PR_STATE": "CLOSED"}, "not open"},
		{"merged pull request", func(_ *testing.T, _ engineFixture, _ *WorktreeMergeReceipt) {}, map[string]string{"WB_TEST_PR_MERGED": "true"}, "not open"},
		{"wrong base", func(_ *testing.T, _ engineFixture, _ *WorktreeMergeReceipt) {}, map[string]string{"WB_TEST_PR_BASE_REF": "release"}, "does not match exact repository, target, candidate branch, and candidate SHA"},
		{"wrong head branch", func(_ *testing.T, _ engineFixture, _ *WorktreeMergeReceipt) {}, map[string]string{"WB_TEST_PR_HEAD_REF": "wb/other-head"}, "does not match exact repository, target, candidate branch, and candidate SHA"},
		{"wrong head SHA", func(t *testing.T, f engineFixture, _ *WorktreeMergeReceipt) {
		}, map[string]string{"WB_TEST_PR_HEAD_SHA": strings.Repeat("a", 40)}, "does not match exact repository, target, candidate branch, and candidate SHA"},
		{"wrong head repository", func(_ *testing.T, _ engineFixture, _ *WorktreeMergeReceipt) {}, map[string]string{"WB_TEST_PR_HEAD_REPO": "acme/other"}, "does not match exact repository, target, candidate branch, and candidate SHA"},
		{"wrong base repository", func(_ *testing.T, _ engineFixture, _ *WorktreeMergeReceipt) {}, map[string]string{"WB_TEST_PR_BASE_REPO": "acme/other"}, "does not match exact repository, target, candidate branch, and candidate SHA"},
		{"moved remote candidate", func(t *testing.T, _ engineFixture, r *WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "moved.txt"), "moved\n")
			runEngineGit(t, r.Candidate.Worktree, "add", "moved.txt")
			runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "test: move the published candidate ref")
			runEngineGit(t, r.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+r.Candidate.Branch)
		}, map[string]string{"WB_TEST_PR_HEAD_SHA": ""}, "candidate remote branch"},
	} {
		//nolint:paralleltest // Each case builds its own t.Setenv real-Git fixture in the shared private lane.
		t.Run(tc.name, func(t *testing.T) {
			fixture, receipt, published, _ := newPublishedPrepareConflictFixture(t)
			installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
			tc.mutate(t, fixture, &receipt)
			if tc.name == "moved remote candidate" {
				// The provider still reports the predecessor it last observed
				// while the private remote ref has moved: the ref proof, not a
				// provider coincidence, must refuse.
				if err := os.Setenv("WB_TEST_PR_HEAD_SHA", published); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Unsetenv("WB_TEST_PR_HEAD_SHA") })
			} else {
				for key, value := range tc.env {
					t.Setenv(key, value)
				}
			}
			before := receipt
			changed, err := recoverPublishedPrepareConflictCandidate(t.Context(), fixture.githubDir, &receipt, 5*time.Second, 0)
			if changed || err == nil || !strings.Contains(err.Error(), tc.want) || receipt.Candidate.SHA != before.Candidate.SHA ||
				receipt.PublishedCandidateSHA != before.PublishedCandidateSHA || receipt.PullRequest != before.PullRequest {
				t.Fatalf("provider refusal changed=%v error=%v receipt=%+v want %q", changed, err, receipt, tc.want)
			}
		})
	}
}

// recordLocalOnlyRepair commits a Q-only repair on the published predecessor in
// the managed candidate worktree and returns Q, restoring the branch so the
// fixture's clean head still omits the repair.
func recordLocalOnlyRepair(t *testing.T, worktree, branch, predecessor string) string {
	t.Helper()
	runEngineGit(t, worktree, "checkout", "-q", "--detach", predecessor)
	writeEngineFile(t, filepath.Join(worktree, "local-repair.txt"), "validated local repair, not yet published\n")
	runEngineGit(t, worktree, "add", "local-repair.txt")
	runEngineGit(t, worktree, "commit", "-q", "-m", "fix: validated local repair that is not yet published")
	repair := strings.TrimSpace(runEngineGit(t, worktree, "rev-parse", "HEAD"))
	runEngineGit(t, worktree, "checkout", "-q", branch)
	return repair
}

//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestPublishedPrepareConflictRecoveryPreservesRecordedLocalRepair(t *testing.T) {
	//nolint:paralleltest // Each case builds its own t.Setenv real-Git fixture in the shared private lane.
	t.Run("resolution dropping the recorded repair refuses", func(t *testing.T) {
		fixture, receipt, published, head := newPublishedPrepareConflictFixture(t)
		installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
		repair := recordLocalOnlyRepair(t, receipt.Candidate.Worktree, receipt.Candidate.Branch, published)
		// The legacy receipt records the local repair only through its
		// validated revision; the clean head still contains P+S+target but
		// drops Q.
		receipt.Validation.Revision = repair
		receipt.Failure = "prepare refresh failed after validating " + repair
		before := receipt

		changed, err := recoverPublishedPrepareConflictCandidate(t.Context(), fixture.githubDir, &receipt, 5*time.Second, 0)
		if changed || err == nil || !strings.Contains(err.Error(), "does not contain required immutable root") || !strings.Contains(err.Error(), repair) {
			t.Fatalf("repair-dropping resolution changed=%v error=%v receipt=%+v", changed, err, receipt)
		}
		if receipt.Candidate.SHA != before.Candidate.SHA || receipt.PublishedCandidateSHA != before.PublishedCandidateSHA || receipt.PullRequest != before.PullRequest {
			t.Fatalf("repair-dropping refusal changed publication identity: %+v", receipt)
		}
		if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); got != head {
			t.Fatalf("refusal moved the candidate head to %s, want preserved %s", got, head)
		}
	})

	//nolint:paralleltest // Each case builds its own t.Setenv real-Git fixture in the shared private lane.
	t.Run("resolution containing the recorded repair recovers", func(t *testing.T) {
		fixture, receipt, published, _ := newPublishedPrepareConflictFixture(t)
		installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
		repair := recordLocalOnlyRepair(t, receipt.Candidate.Worktree, receipt.Candidate.Branch, published)
		// A clean resolution H descends from P and Q and integrates the
		// advanced source S.
		runEngineGit(t, receipt.Candidate.Worktree, "reset", "--hard", repair)
		runEngineGit(t, receipt.Candidate.Worktree, "merge", "--no-edit", receipt.Sources[0].SHA)
		resolved := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
		receipt.Validation.Revision = repair

		changed, err := recoverPublishedPrepareConflictCandidate(t.Context(), fixture.githubDir, &receipt, 5*time.Second, 0)
		if err != nil || !changed || receipt.Candidate.SHA != resolved || receipt.Status != WorktreeMergePreparing ||
			receipt.PublishedCandidateSHA != published || receipt.PullRequest != "https://example.test/acme/app/pull/41" {
			t.Fatalf("repair-preserving resolution changed=%v error=%v receipt=%+v", changed, err, receipt)
		}
		for _, root := range []string{published, repair, receipt.TargetSHA, receipt.Sources[0].SHA} {
			assertNativeAncestor(t, receipt.Candidate.Worktree, root, resolved)
		}
	})
}

//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestPublishedPrepareConflictRecoveryProvesEveryRecordedCandidateRoot(t *testing.T) {
	// Each row records one available root of the same publication that a
	// replacement head omitted. The refusal must name it and leave the
	// candidate and publication identity untouched.
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, engineFixture, *WorktreeMergeReceipt, string)
	}{
		{"validation revision", func(_ *testing.T, _ engineFixture, r *WorktreeMergeReceipt, side string) {
			r.Validation.Revision = side
		}},
		{"validation deferral candidate", func(_ *testing.T, _ engineFixture, r *WorktreeMergeReceipt, side string) {
			r.ValidationDeferral = &WorktreeMergeValidationDeferral{Route: WorktreeMergeRoutePullRequest, CandidateSHA: side, Reason: "test deferral"}
		}},
		{"validation identity candidate", func(_ *testing.T, _ engineFixture, r *WorktreeMergeReceipt, side string) {
			r.ValidationIdentity = &WorktreeMergeValidationIdentity{CandidateSHA: side, TargetSHA: r.TargetSHA}
		}},
		{"target refresh previous candidate", func(_ *testing.T, _ engineFixture, r *WorktreeMergeReceipt, side string) {
			r.TargetRefreshes = []WorktreeMergeTargetRefresh{{PreviousTargetSHA: r.TargetSHA, NewTargetSHA: r.TargetSHA, PreviousCandidateSHA: side, NewCandidateSHA: r.PublishedCandidateSHA}}
		}},
		{"target refresh new candidate", func(_ *testing.T, _ engineFixture, r *WorktreeMergeReceipt, side string) {
			r.TargetRefreshes = []WorktreeMergeTargetRefresh{{PreviousTargetSHA: r.TargetSHA, NewTargetSHA: r.TargetSHA, PreviousCandidateSHA: r.PublishedCandidateSHA, NewCandidateSHA: side}}
		}},
		{"target refresh previous target", func(_ *testing.T, _ engineFixture, r *WorktreeMergeReceipt, side string) {
			r.TargetRefreshes = []WorktreeMergeTargetRefresh{{PreviousTargetSHA: side, NewTargetSHA: r.TargetSHA, PreviousCandidateSHA: r.PublishedCandidateSHA, NewCandidateSHA: r.PublishedCandidateSHA}}
		}},
		{"source refresh evidence", func(_ *testing.T, _ engineFixture, r *WorktreeMergeReceipt, side string) {
			r.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: r.CreatedAt, Sources: []WorktreeMergeSource{{Task: "historical-source", Worktree: r.Sources[0].Worktree, Branch: r.Sources[0].Branch, SHA: side}}}}
		}},
	} {
		//nolint:paralleltest // Each case builds its own t.Setenv real-Git fixture in the shared private lane.
		t.Run(tc.name, func(t *testing.T) {
			fixture, receipt, published, head := newPublishedPrepareConflictFixture(t)
			installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
			side := createMergeSource(t, fixture, "unrelated-"+strings.ReplaceAll(tc.name, " ", "-"), "feature/unrelated-recorded-root", "unrelated.txt", "unrelated\n")
			sideSHA := strings.TrimSpace(runEngineGit(t, side.WorktreeDir, "rev-parse", "HEAD"))
			tc.mutate(t, fixture, &receipt, sideSHA)
			before := receipt

			changed, err := recoverPublishedPrepareConflictCandidate(t.Context(), fixture.githubDir, &receipt, 5*time.Second, 0)
			if changed || err == nil || !strings.Contains(err.Error(), "does not contain required immutable root") || !strings.Contains(err.Error(), sideSHA) {
				t.Fatalf("%s omission changed=%v error=%v receipt=%+v", tc.name, changed, err, receipt)
			}
			if receipt.Candidate.SHA != before.Candidate.SHA || receipt.PublishedCandidateSHA != before.PublishedCandidateSHA || receipt.PullRequest != before.PullRequest {
				t.Fatalf("%s refusal changed publication identity: %+v", tc.name, receipt)
			}
			if got := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); got != head {
				t.Fatalf("%s refusal moved the candidate head to %s, want %s", tc.name, got, head)
			}
		})
	}
}

//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestPublishedPrepareConflictRecoveryRecoversWhenEveryRecordedRootIsContained(t *testing.T) {
	fixture, receipt, published, head := newPublishedPrepareConflictFixture(t)
	installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
	receipt.Validation.Revision = published
	receipt.ValidationDeferral = &WorktreeMergeValidationDeferral{Route: WorktreeMergeRoutePullRequest, CandidateSHA: head, Reason: "test deferral"}
	receipt.ValidationIdentity = &WorktreeMergeValidationIdentity{CandidateSHA: published, TargetSHA: receipt.TargetSHA}
	receipt.TargetRefreshes = []WorktreeMergeTargetRefresh{{PreviousTargetSHA: receipt.TargetSHA, NewTargetSHA: receipt.TargetSHA, PreviousCandidateSHA: published, NewCandidateSHA: head}}
	receipt.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: receipt.CreatedAt, Sources: append([]WorktreeMergeSource(nil), receipt.Sources...)}}
	// Prose that names an unrelated commit is not recorded evidence: it must
	// neither add a root nor influence the proof.
	receipt.Failure = "prepare refresh failed near " + strings.Repeat("0", 40)

	changed, err := recoverPublishedPrepareConflictCandidate(t.Context(), fixture.githubDir, &receipt, 5*time.Second, 0)
	if err != nil || !changed || receipt.Candidate.SHA != head || receipt.Status != WorktreeMergePreparing ||
		receipt.PublishedCandidateSHA != published || receipt.PullRequest != "https://example.test/acme/app/pull/41" {
		t.Fatalf("complete-root recovery changed=%v error=%v receipt=%+v", changed, err, receipt)
	}
}

//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestPublishedPrepareConflictRecoveryUsesTheProvedTargetNormalization(t *testing.T) {
	fixture, receipt, published, _ := newPublishedPrepareConflictFixture(t)
	installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
	// Advance the target and integrate it, so the immutable claim base (the
	// original target) differs from the refreshed receipt target while every
	// recorded snapshot remains an ancestor of the clean local head.
	writeEngineFile(t, filepath.Join(fixture.canonical, "target-advance.txt"), "target advance\n")
	runEngineGit(t, fixture.canonical, "add", "target-advance.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: advance target for normalization")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, receipt.Candidate.Worktree, "merge", "--no-edit", target)
	head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	receipt.TargetSHA = target
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	changed, err := recoverPublishedPrepareConflictCandidate(t.Context(), fixture.githubDir, &receipt, 5*time.Second, 0)
	if err != nil || !changed || receipt.Candidate.SHA != head {
		t.Fatalf("proved normalization changed=%v error=%v receipt=%+v", changed, err, receipt)
	}

	// The same normalization must refuse when the clean head does not contain
	// the freshly fetched receipt target.
	fixture, receipt, published, _ = newPublishedPrepareConflictFixture(t)
	installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
	writeEngineFile(t, filepath.Join(fixture.canonical, "unmerged-target.txt"), "unmerged target\n")
	runEngineGit(t, fixture.canonical, "add", "unmerged-target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "feat: unrelated target advance")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	unmerged := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	receipt.TargetSHA = unmerged
	before := receipt
	changed, err = recoverPublishedPrepareConflictCandidate(t.Context(), fixture.githubDir, &receipt, 5*time.Second, 0)
	if changed || err == nil || !strings.Contains(err.Error(), "claim base differs") || !reflect.DeepEqual(receipt, before) || receipt.Candidate.SHA != "" {
		t.Fatalf("unmerged normalization changed=%v error=%v receipt=%+v", changed, err, receipt)
	}
}

//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestPublishedPrepareConflictRecoveryRefusesExactNativeReadErrors(t *testing.T) {
	fixture, receipt, published, head := newPublishedPrepareConflictFixture(t)
	installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
	sentinel := errors.New("controlled exact published-conflict read refused")
	worktree := receipt.Candidate.Worktree
	missingSource := receipt
	missingSource.Candidate.SHA = head
	for _, tc := range []struct {
		name     string
		receipt  WorktreeMergeReceipt
		dir      string
		argv     string
		contains string
	}{
		{"candidate status", receipt, worktree, "status --porcelain=v1", ""},
		{"candidate HEAD", receipt, worktree, "rev-parse --verify HEAD^{commit}", ""},
		{"target fetch", receipt, worktree, "fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main", ""},
		{"predecessor ancestry", receipt, worktree, "merge-base " + published + " " + head, ""},
		{"missing source read", missingSource, worktree, "merge-base " + receipt.Sources[0].SHA + " " + head, "inspect published prepare candidate source"},
		{"remote candidate ref", receipt, worktree, "ls-remote --heads origin refs/heads/" + receipt.Candidate.Branch, "read candidate remote ref"},
	} {
		//nolint:paralleltest // Each case builds its own t.Setenv real-Git fixture in the shared private lane.
		t.Run(tc.name, func(t *testing.T) {
			current := tc.receipt
			before := current
			run := conflictNegativeRunner{Runner: runner.New(), dir: tc.dir, argv: tc.argv, err: sentinel}
			changed, err := recoverPublishedPrepareConflictCandidateWithRunner(t.Context(), run, fixture.githubDir, &current, 5*time.Second, 0)
			if changed || !errors.Is(err, sentinel) || !reflect.DeepEqual(current, before) {
				t.Fatalf("native read %s changed=%v error=%v receipt=%+v", tc.name, changed, err, current)
			}
			if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("native read %s error=%v want %q", tc.name, err, tc.contains)
			}
		})
	}
}

//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestPublishedPrepareConflictRecoveryRefusesInactiveCandidateClaim(t *testing.T) {
	fixture, receipt, _, _ := newPublishedPrepareConflictFixture(t)
	view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: receipt.Candidate.Worktree})
	if err != nil || view.Claim == nil || view.Claim.ClaimPath == "" {
		t.Fatalf("actual candidate claim=%+v error=%v", view.Claim, err)
	}
	raw, err := os.ReadFile(view.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	var claim map[string]any
	if err := json.Unmarshal(raw, &claim); err != nil {
		t.Fatal(err)
	}
	claim["lifecycle"] = "terminal"
	inactive, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(view.Claim.ClaimPath, inactive, 0o600); err != nil {
		t.Fatal(err)
	}
	before := receipt
	changed, recoverErr := recoverPublishedPrepareConflictCandidate(t.Context(), fixture.githubDir, &receipt, 5*time.Second, 0)
	if changed || recoverErr == nil || !reflect.DeepEqual(receipt, before) ||
		!strings.Contains(recoverErr.Error(), "claim") && !strings.Contains(recoverErr.Error(), "Work Log") {
		t.Fatalf("inactive claim changed=%v error=%v receipt=%+v", changed, recoverErr, receipt)
	}
}

//nolint:paralleltest // newEngineFixture scopes the process-wide projects root with t.Setenv; each case owns a private real-Git lane.
func TestPublishedPrepareConflictRecoveryRefusesUnreadableCandidateWorkLog(t *testing.T) {
	fixture, receipt, published, _ := newPublishedPrepareConflictFixture(t)
	prompts := filepath.Join(receipt.Candidate.Worktree, ".wb", "local", "prompts")
	if err := os.RemoveAll(prompts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prompts, []byte("private non-directory prompt fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeEngineGH(t, fixture, published, receipt.Candidate.Branch)
	before := receipt
	changed, err := recoverPublishedPrepareConflictCandidate(t.Context(), fixture.githubDir, &receipt, 5*time.Second, 0)
	if changed || err == nil || !strings.Contains(err.Error(), "load Work Log") || !reflect.DeepEqual(receipt, before) {
		t.Fatalf("unreadable journal changed=%v error=%v receipt=%+v", changed, err, receipt)
	}
}
