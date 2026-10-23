package orchestrate

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestPreparingReceiptConstructionKeepsEstablishedFacts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	sources := []WorktreeMergeSource{{Task: "source", Worktree: "/source", Branch: "feature/source", SHA: "source-sha"}}
	admission := &WorktreeMergeHostLoadAdmission{}
	input := preparingReceiptInput{
		options:   WorktreeMergePrepareOptions{CheckTimeout: 2 * time.Second, ShardAttemptTimeout: 3 * time.Second, ProgressRequested: true, HostLoadAdmission: admission},
		candidate: worktrees.CreateResult{WorktreeDir: "/candidate", Branch: "wb/integration/main/op", BaseSHA: "target-sha"}, sources: sources,
		now: now, operation: "op", lane: "lane", repository: "acme/app", target: "main", receiptPath: "/receipt.json",
	}
	receipt := newPreparingWorktreeMergeReceipt(input)
	if receipt.SchemaVersion != WorktreeMergeSchemaVersion || receipt.ID != "op" || receipt.Lane != "lane" || receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergePreparing || receipt.Repository != "acme/app" || receipt.Target != "main" || receipt.TargetSHA != "target-sha" || !reflect.DeepEqual(receipt.Sources, sources) || receipt.Candidate != (WorktreeMergeCandidate{Task: "op", Worktree: "/candidate", Branch: "wb/integration/main/op"}) {
		t.Fatalf("construction changed established facts: %+v", receipt)
	}
	if receipt.CreatedAt != now || receipt.UpdatedAt != now || receipt.ReceiptPath != "/receipt.json" || receipt.HostLoadAdmission != admission || receipt.LaneOwner != nil || receipt.RebatchOf != "" || len(receipt.SourceRefreshes) != 0 {
		t.Fatalf("fresh receipt metadata=%+v", receipt)
	}
	if !reflect.DeepEqual(receipt.ResumeArgs, worktreeMergePrepareResumeArgs(input.receiptPath, true)) || !reflect.DeepEqual(receipt.ValidationTimeouts, worktreeMergeValidationTimeouts(2*time.Second, 3*time.Second)) {
		t.Fatalf("fresh resume/limits=%+v", receipt)
	}
}

func TestPreparingReceiptConstructionPreservesDistinctRefreshAndRepairHistory(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	now := created.Add(time.Hour)
	prior := WorktreeMergeReceipt{
		CreatedAt: created, Status: WorktreeMergePostTargetCIFailed, TargetSHA: "old-target", Candidate: WorktreeMergeCandidate{SHA: "old-candidate"},
		Sources:         []WorktreeMergeSource{{Task: "old-source", SHA: "old-source-sha"}},
		SourceRefreshes: []WorktreeMergeSourceRefresh{{RecordedAt: created, Sources: []WorktreeMergeSource{{Task: "first-source"}}}},
		Route:           WorktreeMergeRouteDecision{Route: WorktreeMergeRoutePullRequest}, Cleanup: true, OnFailure: "stop", ResumeArgs: []string{"original", "resume"},
		PullRequest: "https://example.test/pull/1", PublishedCandidateSHA: "published", PreviousTargetSHA: "previous-target", LandingSHA: "landed", Failure: "post-target failure",
		ForwardRepairs:     []WorktreeMergeForwardRepairReceipt{{CandidateSHA: "older-candidate"}},
		ValidationTimeouts: worktreeMergeValidationTimeouts(11*time.Second, 12*time.Second),
	}
	before := prior
	for _, mode := range []string{"published", "publication fallback", "unpublished", "forward repair"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			previous := prior
			if mode == "publication fallback" {
				previous.PublishedCandidateSHA = ""
			}
			if mode == "unpublished" {
				previous.PullRequest = ""
				previous.PublishedCandidateSHA = ""
			}
			input := preparingReceiptInput{candidate: worktrees.CreateResult{WorktreeDir: "/next", Branch: "wb/next", BaseSHA: "next-base"}, prior: &previous, forwardRepair: mode == "forward repair", now: now, operation: "next", lane: "lane", repository: "acme/app", target: "main", receiptPath: "/next.json"}
			receipt := newPreparingWorktreeMergeReceipt(input)
			if receipt.CreatedAt != created || receipt.UpdatedAt != now || receipt.Route != previous.Route || receipt.Cleanup != previous.Cleanup || receipt.OnFailure != previous.OnFailure || !reflect.DeepEqual(receipt.ResumeArgs, previous.ResumeArgs) || !reflect.DeepEqual(receipt.ValidationTimeouts, previous.ValidationTimeouts) {
				t.Fatalf("inherited policy/history=%+v", receipt)
			}
			wantRefreshes := append(append([]WorktreeMergeSourceRefresh(nil), previous.SourceRefreshes...), WorktreeMergeSourceRefresh{RecordedAt: now, Sources: append([]WorktreeMergeSource(nil), previous.Sources...)})
			if !reflect.DeepEqual(receipt.SourceRefreshes, wantRefreshes) || !reflect.DeepEqual(prior, before) {
				t.Fatalf("immutable refresh history=%+v prior=%+v", receipt.SourceRefreshes, prior)
			}
			if mode == "forward repair" {
				if receipt.PullRequest != "" || receipt.PublishedCandidateSHA != "" || receipt.PreviousTargetSHA != "" || len(receipt.ForwardRepairs) != 2 || receipt.ForwardRepairs[1].CandidateSHA != previous.Candidate.SHA || receipt.ForwardRepairs[1].LandingSHA != previous.LandingSHA || receipt.ForwardRepairs[1].Failure != previous.Failure || receipt.ForwardRepairs[1].PullRequest != previous.PullRequest || receipt.ForwardRepairs[1].Status != previous.Status || receipt.ForwardRepairs[1].TargetSHA != previous.TargetSHA {
					t.Fatalf("repair retained publication or lost landing history: %+v", receipt)
				}
			} else {
				expectedPublished := previous.PublishedCandidateSHA
				if previous.PullRequest != "" && expectedPublished == "" {
					expectedPublished = previous.Candidate.SHA
				}
				if receipt.PullRequest != previous.PullRequest || receipt.PublishedCandidateSHA != expectedPublished || receipt.PreviousTargetSHA != previous.PreviousTargetSHA || len(receipt.ForwardRepairs) != 0 {
					t.Fatalf("ordinary refresh publication/history=%+v", receipt)
				}
			}
			receipt.ResumeArgs[0] = "changed private result"
			receipt.SourceRefreshes[len(receipt.SourceRefreshes)-1].Sources[0].Task = "changed private result"
			if previous.ResumeArgs[0] != "original" || previous.Sources[0].Task != "old-source" {
				t.Fatal("constructed receipt aliased mutable prior resume/source snapshots")
			}
		})
	}
}

func TestPreparingReceiptConstructionCopiesLaneAndRebatchAndHonorsExplicitLimits(t *testing.T) {
	t.Parallel()
	prior := WorktreeMergeReceipt{ValidationTimeouts: worktreeMergeValidationTimeouts(11*time.Second, 12*time.Second)}
	for _, limits := range []struct {
		name                               string
		check, shard, wantCheck, wantShard time.Duration
	}{
		{"inherited", 0, 0, 11 * time.Second, 12 * time.Second},
		{"check override", 2 * time.Second, 0, 2 * time.Second, 12 * time.Second},
		{"shard override", 0, 3 * time.Second, 11 * time.Second, 3 * time.Second},
		{"both override", 2 * time.Second, 3 * time.Second, 2 * time.Second, 3 * time.Second},
	} {
		t.Run(limits.name, func(t *testing.T) {
			t.Parallel()
			input := preparingReceiptInput{options: WorktreeMergePrepareOptions{CheckTimeout: limits.check, ShardAttemptTimeout: limits.shard}, prior: &prior, laneRecord: landinglane.Record{Owner: landinglane.Owner{WBSessionID: "real-owner"}}, rebatch: &WorktreeMergePreparedRebatch{ReceiptPath: "/original.json", OriginalCandidate: WorktreeMergeCandidate{Task: "original", SHA: "original-sha"}}}
			receipt := newPreparingWorktreeMergeReceipt(input)
			if receipt.LaneOwner == nil || receipt.LaneOwner.Owner.WBSessionID != "real-owner" || receipt.RebatchOf != "/original.json" || !reflect.DeepEqual(receipt.RebatchedCandidates, []WorktreeMergeCandidate{input.rebatch.OriginalCandidate}) || !reflect.DeepEqual(receipt.ValidationTimeouts, worktreeMergeValidationTimeouts(limits.wantCheck, limits.wantShard)) {
				t.Fatalf("lane/rebatch/limits=%+v", receipt)
			}
			input.laneRecord.Owner.WBSessionID = "changed input"
			input.rebatch.OriginalCandidate.SHA = "changed input"
			if receipt.LaneOwner.Owner.WBSessionID != "real-owner" || receipt.RebatchedCandidates[0].SHA != "original-sha" {
				t.Fatal("receipt retained mutable lane/rebatch input identity")
			}
		})
	}
}

func TestPrepareContinuationChoiceUsesNativeAdditiveEvidence(t *testing.T) {
	t.Parallel()
	f := newConflictRecoveryFixture(t)
	old := f.receipt.Sources[0]
	fresh := advanceContinuationSource(t, f)
	prior := f.receipt
	choice, err := choosePrepareContinuation(t.Context(), runner.New(), prior, fresh)
	if err != nil || choice != prepareRefresh {
		t.Fatalf("actual additive refresh choice=%v, %v", choice, err)
	}
	// The policy DTO selects repair; all clean/HEAD/source ancestry observations remain native.
	prior.Status = WorktreeMergePostTargetCIFailed
	prior.LandingSHA = prior.TargetSHA
	prior.Candidate.SHA = f.head
	choice, err = choosePrepareContinuation(t.Context(), runner.New(), prior, fresh)
	if err != nil || choice != prepareForwardRepair {
		t.Fatalf("native additive repair choice=%v, %v", choice, err)
	}
	choice, err = choosePrepareContinuation(t.Context(), runner.New(), prior, []WorktreeMergeSource{old})
	if err != nil || choice != prepareRefused {
		t.Fatalf("unchanged source refusal=%v, %v", choice, err)
	}
	sentinel := errors.New("negative exact source or repair read")
	for _, tc := range []struct {
		name, dir, argv string
		receipt         WorktreeMergeReceipt
	}{
		{"refresh source ancestry", fresh[0].Worktree, "merge-base " + old.SHA + " " + fresh[0].SHA, f.receipt},
		{"repair head", prior.Candidate.Worktree, "rev-parse --verify HEAD^{commit}", prior},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := conflictNegativeRunner{Runner: runner.New(), dir: tc.dir, argv: tc.argv, err: sentinel}
			choice, err := choosePrepareContinuation(t.Context(), run, tc.receipt, fresh)
			if choice != prepareRefused || !errors.Is(err, sentinel) {
				t.Fatalf("exact negative continuation=%v, %v", choice, err)
			}
		})
	}
	if strings.TrimSpace(runEngineGit(t, f.receipt.Sources[0].Worktree, "rev-parse", "HEAD")) != fresh[0].SHA {
		t.Fatal("continuation decision changed the actual source")
	}
}
