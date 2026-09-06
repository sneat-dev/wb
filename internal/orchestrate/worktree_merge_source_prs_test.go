package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakeSourcePullRequestRemote struct {
	byHead     map[string][]PullRequestView
	comments   map[int]bool
	closed     map[int]int
	posted     map[int]int
	commentErr error
}

func (fake *fakeSourcePullRequestRemote) associated(_ context.Context, _ string, head string) ([]PullRequestView, error) {
	return fake.byHead[head], nil
}

func (fake *fakeSourcePullRequestRemote) hasComment(_ context.Context, _ string, number int, _ string) (bool, error) {
	return fake.comments[number], nil
}

func (fake *fakeSourcePullRequestRemote) comment(_ context.Context, _ string, number int, body string) error {
	if fake.commentErr != nil {
		return fake.commentErr
	}
	if !strings.Contains(body, absorbedSourcePRCommentMarker) {
		return nil
	}
	fake.comments[number] = true
	fake.posted[number]++
	return nil
}

func (fake *fakeSourcePullRequestRemote) close(_ context.Context, _ string, number int) error {
	fake.closed[number]++
	return nil
}

func sourcePullRequestView(number int, state, head, base string) PullRequestView {
	view := PullRequestView{Number: number, State: state, HTMLURL: fmt.Sprintf("https://github.com/acme/app/pull/%d", number)}
	view.Head.SHA = head
	view.Base.Ref = base
	view.Base.Repo = &struct {
		FullName string `json:"full_name"`
	}{FullName: "acme/app"}
	return view
}

func TestReconcileAbsorbedSourcePullRequestClosesExactHeadOnce(t *testing.T) {
	const source = "1111111111111111111111111111111111111111"
	remote := &fakeSourcePullRequestRemote{
		byHead:   map[string][]PullRequestView{source: {sourcePullRequestView(7, "open", source, "main")}},
		comments: map[int]bool{}, closed: map[int]int{}, posted: map[int]int{},
	}
	receipt := WorktreeMergeReceipt{
		Repository: "acme/app", Target: "main", PullRequest: "https://github.com/acme/app/pull/9",
		LandingSHA: "2222222222222222222222222222222222222222",
	}
	persisted := 0
	persist := func(WorktreeMergeReceipt) error { persisted++; return nil }
	if err := reconcileAbsorbedSourcePullRequestsWith(context.Background(), &receipt, []string{source}, remote, persist); err != nil {
		t.Fatal(err)
	}
	if remote.closed[7] != 1 || remote.posted[7] != 1 || persisted != 2 {
		t.Fatalf("first reconciliation: closed=%d posted=%d persisted=%d", remote.closed[7], remote.posted[7], persisted)
	}
	if len(receipt.SourcePullRequests) != 1 || !receipt.SourcePullRequests[0].Closed || !receipt.SourcePullRequests[0].Commented || receipt.SourcePullRequests[0].Outcome != "closed_absorbed" {
		t.Fatalf("receipt = %+v", receipt.SourcePullRequests)
	}
	if err := reconcileAbsorbedSourcePullRequestsWith(context.Background(), &receipt, []string{source}, remote, persist); err != nil {
		t.Fatal(err)
	}
	if remote.closed[7] != 1 || remote.posted[7] != 1 {
		t.Fatalf("retry repeated mutation: closed=%d posted=%d", remote.closed[7], remote.posted[7])
	}
}

func TestReconcileAbsorbedSourcePullRequestCommentFailureLeavesItOpen(t *testing.T) {
	const source = "1111111111111111111111111111111111111111"
	remote := &fakeSourcePullRequestRemote{
		byHead:   map[string][]PullRequestView{source: {sourcePullRequestView(7, "open", source, "main")}},
		comments: map[int]bool{}, closed: map[int]int{}, posted: map[int]int{}, commentErr: errors.New("comment unavailable"),
	}
	receipt := WorktreeMergeReceipt{
		Repository: "acme/app", Target: "main", PullRequest: "https://github.com/acme/app/pull/9",
		LandingSHA: "2222222222222222222222222222222222222222",
	}
	err := reconcileAbsorbedSourcePullRequestsWith(context.Background(), &receipt, []string{source}, remote, func(WorktreeMergeReceipt) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "comment unavailable") {
		t.Fatalf("error = %v, want comment failure", err)
	}
	if remote.closed[7] != 0 {
		t.Fatalf("comment failure closed the source pull request %d time(s)", remote.closed[7])
	}
}

func TestReconcileAbsorbedSourcePullRequestRefusesDriftedIdentity(t *testing.T) {
	const source = "1111111111111111111111111111111111111111"
	tests := []struct {
		name, head, base, outcome string
	}{
		{name: "head advanced", head: "3333333333333333333333333333333333333333", base: "main", outcome: "head_advanced"},
		{name: "base changed", head: source, base: "release", outcome: "base_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := &fakeSourcePullRequestRemote{
				byHead:   map[string][]PullRequestView{source: {sourcePullRequestView(7, "open", test.head, test.base)}},
				comments: map[int]bool{}, closed: map[int]int{}, posted: map[int]int{},
			}
			receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "main", PullRequest: "9", LandingSHA: "2222222222222222222222222222222222222222"}
			if err := reconcileAbsorbedSourcePullRequestsWith(context.Background(), &receipt, []string{source}, remote, func(WorktreeMergeReceipt) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if remote.closed[7] != 0 || remote.posted[7] != 0 {
				t.Fatalf("drifted PR mutated: closed=%d posted=%d", remote.closed[7], remote.posted[7])
			}
			if len(receipt.SourcePullRequests) != 1 || receipt.SourcePullRequests[0].Outcome != test.outcome || receipt.SourcePullRequests[0].Closed {
				t.Fatalf("receipt = %+v", receipt.SourcePullRequests)
			}
		})
	}
}
