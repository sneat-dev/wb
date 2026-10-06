package orchestrate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

// These DTOs are hosted reconciliation contracts, not native Git or hosted custody proofs.
func TestSourcePROwnerOutcomeCheckpointsPreserveFailuresAndPartialState(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"head advanced", "base changed", "nil base", "foreign base", "closed after comment", "already closed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			view := sourcePullRequestView(7, "open", "source", "main")
			outcome := "base_mismatch"
			switch mode {
			case "head advanced":
				view.Head.SHA = "advanced"
				outcome = "head_advanced"
			case "base changed":
				view.Base.Ref = "release"
			case "nil base":
				view.Base.Repo = nil
			case "foreign base":
				view.Base.Repo.FullName = "foreign/app"
			case "closed after comment":
				outcome = "closed_absorbed"
			case "already closed":
				view.State = "CLOSED"
				outcome = "already_closed"
			}
			receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "main", LandingSHA: "landing"}
			remote := &fakeSourcePullRequestRemote{byHead: map[string][]githubchecks.PullRequestView{"source": {view}}, comments: map[int]bool{}, closed: map[int]int{}, posted: map[int]int{}}
			sentinel := errors.New("selected outcome checkpoint")
			writes := 0
			before := time.Now().UTC()
			err := reconcileAbsorbedSourcePullRequestsWithProgress(t.Context(), &receipt, []string{"source"}, remote, func(got WorktreeMergeReceipt) error {
				writes++
				item := got.SourcePullRequests[0]
				if item.Outcome == "" {
					if !item.Commented || item.Closed || remote.closed[7] != 0 {
						t.Fatalf("comment checkpoint after close: %+v", item)
					}
					return nil
				}
				if item.Outcome != outcome || item.Reason == "" || item.UpdatedAt.Before(before) || item.Number != 7 || item.SourceSHA != "source" || item.ObservedSHA != view.Head.SHA || item.ObservedBase != view.Base.Ref {
					t.Fatalf("wrong outcome checkpoint: %+v", item)
				}
				return sentinel
			}, nil)
			if !errors.Is(err, sentinel) || len(receipt.SourcePullRequests) != 1 {
				t.Fatalf("checkpoint refusal=%v receipt=%+v", err, receipt.SourcePullRequests)
			}
			wantWrites := 1
			if outcome == "closed_absorbed" || outcome == "already_closed" {
				wantWrites = 2
			}
			if writes != wantWrites {
				t.Fatalf("writes=%d want=%d", writes, wantWrites)
			}
			wantClose := 0
			if outcome == "closed_absorbed" {
				wantClose = 1
			}
			if remote.closed[7] != wantClose {
				t.Fatalf("partial hosted close=%d", remote.closed[7])
			}
		})
	}
}

func TestSourcePROwnerCommentDurabilityAndIdempotentMetadata(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"comment persistence failure", "already commented open", "closed but not commented", "closed and commented"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			state := "open"
			item := WorktreeMergeSourcePullRequestReconciliation{Number: 7, SourceSHA: "source"}
			switch mode {
			case "already commented open":
				item.Commented = true
			case "closed but not commented":
				item.Closed = true
			case "closed and commented":
				item.Closed = true
				item.Commented = true
				state = "closed"
			}
			receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "main", LandingSHA: "landing", SourcePullRequests: []WorktreeMergeSourcePullRequestReconciliation{item}}
			remote := &fakeSourcePullRequestRemote{byHead: map[string][]githubchecks.PullRequestView{"source": {sourcePullRequestView(7, state, "source", "main")}}, comments: map[int]bool{}, closed: map[int]int{}, posted: map[int]int{}}
			sentinel := errors.New("comment receipt failure")
			writes := 0
			err := reconcileAbsorbedSourcePullRequestsWithProgress(t.Context(), &receipt, []string{"source"}, remote, func(got WorktreeMergeReceipt) error {
				writes++
				if mode == "comment persistence failure" {
					if !got.SourcePullRequests[0].Commented || got.SourcePullRequests[0].Closed || remote.closed[7] != 0 {
						t.Fatalf("comment not durable before close: %+v", got.SourcePullRequests[0])
					}
					return sentinel
				}
				return nil
			}, nil)
			if mode == "comment persistence failure" {
				if !errors.Is(err, sentinel) || remote.closed[7] != 0 || writes != 1 {
					t.Fatalf("early failure=%v closes=%d writes=%d", err, remote.closed[7], writes)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(receipt.SourcePullRequests) != 1 {
				t.Fatal("record duplicated")
			}
			if mode == "closed and commented" && (writes != 0 || remote.closed[7] != 0 || remote.posted[7] != 0) {
				t.Fatalf("idempotent record mutated: %d %+v", writes, remote)
			}
			if mode == "already commented open" && (writes != 1 || remote.posted[7] != 0 || remote.closed[7] != 1) {
				t.Fatalf("already-commented mutation=%+v writes=%d", remote, writes)
			}
			if mode == "closed but not commented" && (writes != 1 || remote.posted[7] != 1 || remote.closed[7] != 0) {
				t.Fatalf("closed comment recovery=%+v writes=%d", remote, writes)
			}
		})
	}
}

func TestSourcePROwnerRecordHelperKeepsTimestampAndExactPersistence(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	receipt := WorktreeMergeReceipt{SourcePullRequests: []WorktreeMergeSourcePullRequestReconciliation{{Number: 7, SourceSHA: "source", UpdatedAt: when, Closed: true, Commented: true}}}
	item := findSourcePullRequestReconciliation(&receipt, 7, "source")
	if item != &receipt.SourcePullRequests[0] {
		t.Fatal("matching record not reused")
	}
	sentinel := errors.New("exact persist")
	called := false
	err := recordSourcePullRequestReconciliation(&receipt, item, "outcome", "reason", func(got WorktreeMergeReceipt) error {
		called = true
		if !reflect.DeepEqual(got, receipt) {
			t.Fatalf("persisted different receipt: %+v", got)
		}
		return sentinel
	})
	if !called || !errors.Is(err, sentinel) || item.UpdatedAt != when || !item.Closed || !item.Commented || item.Outcome != "outcome" || item.Reason != "reason" {
		t.Fatalf("helper=%+v %v", item, err)
	}
	next := findSourcePullRequestReconciliation(&receipt, 7, "other")
	if len(receipt.SourcePullRequests) != 2 || next != &receipt.SourcePullRequests[1] {
		t.Fatal("distinct head reused wrong record")
	}
}

func TestSourcePROwnerHostedTransportEmptyCommentsAndMutationDetails(t *testing.T) {
	t.Parallel()
	remote := githubSourcePullRequestRemote{pages: func(context.Context, githubobserver.GetRequest, int) ([]githubobserver.Response, error) {
		return []githubobserver.Response{{Body: []byte(`[{"body":"unrelated"}]`)}}, nil
	}}
	found, err := remote.hasComment(t.Context(), "acme/app", 7, "marker")
	if err != nil || found {
		t.Fatalf("unrelated comment=%t %v", found, err)
	}
	sentinel := errors.New("native command refusal")
	for _, detail := range []string{"", "stderr\nstdout"} {
		result := githubobserver.CommandResponse{Err: sentinel}
		if detail != "" {
			result.Stderr = []byte(" stderr ")
			result.Stdout = []byte(" stdout ")
		}
		err := githubMutationError(result, "action")
		if !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), "action: native command refusal") {
			t.Fatalf("mutation=%v", err)
		}
		if detail != "" && !strings.Contains(err.Error(), "stderr \n stdout") {
			t.Fatalf("lost native detail: %q", err.Error())
		}
	}
	if err := githubMutationError(githubobserver.CommandResponse{}, "action"); err != nil {
		t.Fatal(err)
	}
}

type sourcePROwnerRecordingRemote struct {
	*fakeSourcePullRequestRemote
	body string
}

func (r *sourcePROwnerRecordingRemote) comment(ctx context.Context, repository string, number int, body string) error {
	r.body = body
	return r.fakeSourcePullRequestRemote.comment(ctx, repository, number, body)
}

func TestSourcePROwnerExactCommentBodyAndRemoteErrorIdentity(t *testing.T) {
	t.Parallel()
	for _, batch := range []string{"", "https://example.test/acme/app/pull/9"} {
		t.Run(batch, func(t *testing.T) {
			t.Parallel()
			receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "main", LandingSHA: "landing", PullRequest: batch}
			base := &fakeSourcePullRequestRemote{byHead: map[string][]githubchecks.PullRequestView{"source": {sourcePullRequestView(7, "open", "source", "main")}}, comments: map[int]bool{}, closed: map[int]int{}, posted: map[int]int{}}
			remote := &sourcePROwnerRecordingRemote{fakeSourcePullRequestRemote: base}
			if err := reconcileAbsorbedSourcePullRequestsWithProgress(t.Context(), &receipt, []string{"source"}, remote, func(WorktreeMergeReceipt) error { return nil }, nil); err != nil {
				t.Fatal(err)
			}
			absorber := batch
			if absorber == "" {
				absorber = "the WB batch"
			}
			want := absorbedSourcePRCommentMarker + "\nWB verified that exact head `source` was absorbed by " + absorber + " and that landing `landing` is on `main`. No additional merge is needed."
			if remote.body != want {
				t.Fatalf("comment bytes=%q want=%q", remote.body, want)
			}
		})
	}
	for _, stage := range []string{"association", "comment read", "comment write", "close"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			sentinel := errors.New("selected hosted contract")
			remote := &fakeSourcePullRequestRemote{byHead: map[string][]githubchecks.PullRequestView{"source": {sourcePullRequestView(7, "open", "source", "main")}}, comments: map[int]bool{}, closed: map[int]int{}, posted: map[int]int{}}
			prefix := ""
			switch stage {
			case "association":
				remote.associatedErr = sentinel
				prefix = "discover pull requests for absorbed source source:"
			case "comment read":
				remote.hasCommentErr = sentinel
				prefix = "inspect absorbed source pull request comments https://github.com/acme/app/pull/7:"
			case "comment write":
				remote.commentErr = sentinel
				prefix = "comment on absorbed source pull request https://github.com/acme/app/pull/7:"
			case "close":
				remote.closeErr = sentinel
				prefix = "close absorbed source pull request https://github.com/acme/app/pull/7:"
			}
			receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "main", LandingSHA: "landing"}
			err := reconcileAbsorbedSourcePullRequestsWithProgress(t.Context(), &receipt, []string{"source"}, remote, func(WorktreeMergeReceipt) error { return nil }, nil)
			if !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), prefix) || remote.closed[7] != 0 {
				t.Fatalf("%s refusal=%v closes=%d", stage, err, remote.closed[7])
			}
		})
	}
}
