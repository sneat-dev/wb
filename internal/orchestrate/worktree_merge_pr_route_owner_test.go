package orchestrate

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPRRouteRecordedAdvanceKeepsOwnerSpecificMetadata(t *testing.T) {
	t.Parallel()
	oldTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	receipt := WorktreeMergeReceipt{TargetSHA: "target-old", Candidate: WorktreeMergeCandidate{SHA: "candidate-old"}, PublishedCandidateSHA: "published-old", UpdatedAt: oldTime, Status: WorktreeMergeChecksPending, Failure: "retained failure", LocalSync: "retained note"}
	before := time.Now().UTC()
	recordWorktreeMergeUpdateBranchAdvance(&receipt, "target-new", "candidate-new")
	after := time.Now().UTC()
	if receipt.TargetSHA != "target-new" || receipt.Candidate.SHA != "candidate-new" || receipt.PublishedCandidateSHA != "candidate-new" || len(receipt.TargetRefreshes) != 1 {
		t.Fatalf("advance=%+v", receipt)
	}
	event := receipt.TargetRefreshes[0]
	if event.PreviousTargetSHA != "target-old" || event.NewTargetSHA != "target-new" || event.PreviousCandidateSHA != "candidate-old" || event.NewCandidateSHA != "candidate-new" || event.RecordedAt.Before(before) || event.RecordedAt.After(after) {
		t.Fatalf("history=%+v", event)
	}
	if receipt.UpdatedAt != oldTime || receipt.Status != WorktreeMergeChecksPending || receipt.Failure != "retained failure" || receipt.LocalSync != "retained note" {
		t.Fatalf("phase changed owner metadata: %+v", receipt)
	}
	recordWorktreeMergeUpdateBranchAdvance(&receipt, "target-third", "candidate-third")
	if receipt.TargetRefreshes[1].PreviousTargetSHA != "target-new" || receipt.TargetRefreshes[1].PreviousCandidateSHA != "candidate-new" {
		t.Fatalf("second history=%+v", receipt.TargetRefreshes)
	}
}

func TestPRRouteHistoryWalksBothIdentitiesWithoutInventingHops(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name     string
		events   []WorktreeMergeTargetRefresh
		from, to string
		want     bool
	}{
		{name: "equal", from: "same", to: "same", want: true},
		{name: "empty", from: "a", to: "c"},
		{name: "broken", events: []WorktreeMergeTargetRefresh{{PreviousTargetSHA: "a", NewTargetSHA: "b", PreviousCandidateSHA: "a", NewCandidateSHA: "b"}}, from: "a", to: "c"},
		{name: "multiple", events: []WorktreeMergeTargetRefresh{{PreviousTargetSHA: "b", NewTargetSHA: "c", PreviousCandidateSHA: "b", NewCandidateSHA: "c"}, {PreviousTargetSHA: "a", NewTargetSHA: "b", PreviousCandidateSHA: "a", NewCandidateSHA: "b"}}, from: "a", to: "c", want: true},
		{name: "cycle", events: []WorktreeMergeTargetRefresh{{PreviousTargetSHA: "a", NewTargetSHA: "b", PreviousCandidateSHA: "a", NewCandidateSHA: "b"}, {PreviousTargetSHA: "b", NewTargetSHA: "a", PreviousCandidateSHA: "b", NewCandidateSHA: "a"}}, from: "a", to: "c"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := WorktreeMergeReceipt{TargetRefreshes: row.events}
			if a, b := worktreeMergeTargetAdvanceRecorded(r, row.from, row.to), worktreeMergeCandidateAdvanceRecorded(r, row.from, row.to); a != row.want || b != row.want {
				t.Fatalf("target=%t candidate=%t want=%t", a, b, row.want)
			}
		})
	}
}

func TestPRRouteDeferredFindingsReplaceOnlyTheirOwnCode(t *testing.T) {
	t.Parallel()
	skipped := PullRequestWaitResult{RequiredChecks: []RequiredRemoteCheck{{Name: "CI"}}, Checks: []RemoteCheck{{Name: "CI", Conclusion: "skipped"}}}
	for _, row := range []struct {
		name     string
		deferred bool
		findings []WorktreeMergeFinding
		waited   PullRequestWaitResult
		want     int
	}{
		{name: "local validation", waited: skipped},
		{name: "executed", deferred: true, waited: PullRequestWaitResult{RequiredChecks: []RequiredRemoteCheck{{Name: "CI"}}, Checks: []RemoteCheck{{Name: "CI", Conclusion: "success"}}}},
		{name: "append", deferred: true, findings: []WorktreeMergeFinding{{Code: "unrelated", Message: "preserved"}}, waited: skipped, want: 2},
		{name: "replace", deferred: true, findings: []WorktreeMergeFinding{{Code: WorktreeMergeFindingDeferredValidationCheckSkipped, Message: "old"}}, waited: skipped, want: 1},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := WorktreeMergeReceipt{Findings: append([]WorktreeMergeFinding(nil), row.findings...)}
			if row.deferred {
				r.ValidationDeferral = &WorktreeMergeValidationDeferral{}
			}
			recordDeferredValidationCheckSkippedFinding(&r, row.waited)
			if len(r.Findings) != row.want {
				t.Fatalf("findings=%+v", r.Findings)
			}
			if row.want > 0 {
				last := r.Findings[len(r.Findings)-1]
				if !reflect.DeepEqual(last.Checks, []string{"CI"}) || !strings.Contains(last.Message, "skipped or neutral") {
					t.Fatalf("finding=%+v", last)
				}
			}
			if row.name == "append" && r.Findings[0].Message != "preserved" {
				t.Fatal("unrelated finding changed")
			}
		})
	}
}

func TestPRRouteEarlyAdoptionRefusalsNeverMutateTheReceipt(t *testing.T) {
	t.Parallel()
	if err := adoptWorktreeMergeUpdateBranchAdvance(context.Background(), nil, nil, nil, "old", "new"); err == nil || !strings.Contains(err.Error(), "no receipt") {
		t.Fatalf("nil receipt=%v", err)
	}
	r := WorktreeMergeReceipt{Candidate: WorktreeMergeCandidate{SHA: "held"}, TargetSHA: "target"}
	before := r
	if err := adoptWorktreeMergeUpdateBranchAdvance(context.Background(), nil, nil, &r, "foreign", "new"); err == nil || !strings.Contains(err.Error(), "did not hold") {
		t.Fatalf("foreign head=%v", err)
	}
	if !reflect.DeepEqual(r, before) {
		t.Fatal("foreign head changed receipt")
	}
	for _, row := range []WorktreeMergeReceipt{{}, {PullRequest: "41"}, {PullRequest: "41", Candidate: WorktreeMergeCandidate{SHA: "old"}, LandingSHA: "landed"}} {
		original := row
		adopted, err := adoptServerUpdatedWorktreeMergeHead(context.Background(), nil, nil, &row)
		if err != nil || adopted || !reflect.DeepEqual(row, original) {
			t.Fatalf("early resume=%+v %t %v", row, adopted, err)
		}
	}
	if adopted, err := adoptServerUpdatedWorktreeMergeHead(context.Background(), nil, nil, nil); adopted || err != nil {
		t.Fatalf("nil resume=%t %v", adopted, err)
	}
}
