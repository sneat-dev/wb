package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

type fakeSourcePullRequestRemote struct {
	byHead        map[string][]PullRequestView
	comments      map[int]bool
	closed        map[int]int
	posted        map[int]int
	commentErr    error
	associatedErr error
	hasCommentErr error
	closeErr      error
}

func (fake *fakeSourcePullRequestRemote) associated(_ context.Context, _ string, head string) ([]PullRequestView, error) {
	if fake.associatedErr != nil {
		return nil, fake.associatedErr
	}
	return fake.byHead[head], nil
}

func (fake *fakeSourcePullRequestRemote) hasComment(_ context.Context, _ string, number int, _ string) (bool, error) {
	if fake.hasCommentErr != nil {
		return false, fake.hasCommentErr
	}
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
	if fake.closeErr != nil {
		return fake.closeErr
	}
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	const source = "1111111111111111111111111111111111111111"
	tests := []struct {
		name, head, base, outcome string
	}{
		{name: "head advanced", head: "3333333333333333333333333333333333333333", base: "main", outcome: "head_advanced"},
		{name: "base changed", head: source, base: "release", outcome: "base_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
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

func TestGithubSourcePullRequestRemoteReadsExactAssociationAndCommentPages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := githubSourcePullRequestRemote{
		get: func(_ context.Context, worktree, repository, target, head, endpoint string) ([]byte, error) {
			if worktree != "" || repository != "acme/app" || target != "" || head != "head with space" || endpoint != "repos/acme/app/commits/head%20with%20space/pulls?per_page=100" {
				t.Fatalf("wrong association query: %q %q %q %q %q", worktree, repository, target, head, endpoint)
			}
			return []byte(`[{"number":7}]`), nil
		},
		pages: func(_ context.Context, request githubobserver.GetRequest, maxPages int) ([]githubobserver.Response, error) {
			if request.Repository != "acme/app" || request.Endpoint != "repos/acme/app/issues/7/comments?per_page=100" || maxPages != 0 {
				t.Fatalf("wrong comment query: %+v pages=%d", request, maxPages)
			}
			return []githubobserver.Response{{Body: []byte(`[{"body":"unrelated"}]`)}, {Body: []byte(`[{"body":"the marker is here"}]`)}}, nil
		},
	}
	views, err := remote.associated(ctx, "acme/app", "head with space")
	if err != nil || len(views) != 1 || views[0].Number != 7 {
		t.Fatalf("associated views=%+v error=%v", views, err)
	}
	found, err := remote.hasComment(ctx, "acme/app", 7, "marker")
	if err != nil || !found {
		t.Fatalf("marker found=%t error=%v", found, err)
	}
}

func TestGithubSourcePullRequestRemoteRejectsUnreadableObservations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		get  func(context.Context, string, string, string, string, string) ([]byte, error)
	}{
		{"request failure", func(context.Context, string, string, string, string, string) ([]byte, error) {
			return nil, errors.New("GitHub unavailable")
		}},
		{"invalid association JSON", func(context.Context, string, string, string, string, string) ([]byte, error) { return []byte("{"), nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := (githubSourcePullRequestRemote{get: tc.get}).associated(ctx, "acme/app", "head"); err == nil {
				t.Fatal("unreadable association accepted")
			}
		})
	}
	for _, tc := range []struct {
		name  string
		pages func(context.Context, githubobserver.GetRequest, int) ([]githubobserver.Response, error)
	}{
		{"request failure", func(context.Context, githubobserver.GetRequest, int) ([]githubobserver.Response, error) {
			return nil, errors.New("GitHub unavailable")
		}},
		{"invalid comment JSON", func(context.Context, githubobserver.GetRequest, int) ([]githubobserver.Response, error) {
			return []githubobserver.Response{{Body: []byte("{")}}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := (githubSourcePullRequestRemote{pages: tc.pages}).hasComment(ctx, "acme/app", 7, "marker"); err == nil {
				t.Fatal("unreadable comment history accepted")
			}
		})
	}
}

func TestGithubSourcePullRequestRemoteSendsCommentAndCloseRequests(t *testing.T) {
	t.Parallel()
	var calls [][]string
	remote := githubSourcePullRequestRemote{execute: func(_ context.Context, worktree string, args ...string) githubobserver.CommandResponse {
		if worktree != "" {
			t.Fatalf("mutation used worktree %q", worktree)
		}
		calls = append(calls, append([]string(nil), args...))
		return githubobserver.CommandResponse{}
	}}
	if err := remote.comment(context.Background(), "acme/app", 7, "message"); err != nil {
		t.Fatal(err)
	}
	if err := remote.close(context.Background(), "acme/app", 7); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || strings.Join(calls[0], " ") != "api --method POST repos/acme/app/issues/7/comments -f body=message" || strings.Join(calls[1], " ") != "api --method PATCH repos/acme/app/pulls/7 -f state=closed" {
		t.Fatalf("mutation requests=%v", calls)
	}
	failing := githubSourcePullRequestRemote{execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
		return githubobserver.CommandResponse{Err: errors.New("permission denied"), Stderr: []byte("forbidden")}
	}}
	if err := failing.comment(context.Background(), "acme/app", 7, "message"); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("comment error=%v", err)
	}
	if err := failing.close(context.Background(), "acme/app", 7); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("close error=%v", err)
	}
}

func TestReconcileAbsorbedSourcePullRequestsSkipsBatchPRAndDuplicateAssociations(t *testing.T) {
	t.Parallel()
	const source = "1111111111111111111111111111111111111111"
	remote := &fakeSourcePullRequestRemote{
		byHead: map[string][]PullRequestView{source: {
			sourcePullRequestView(9, "open", source, "main"),
			sourcePullRequestView(7, "closed", source, "main"),
			sourcePullRequestView(7, "closed", source, "main"),
		}}, comments: map[int]bool{7: true}, closed: map[int]int{}, posted: map[int]int{},
	}
	receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "main", PullRequest: "https://github.com/acme/app/pull/9", LandingSHA: "landed"}
	persisted := 0
	err := reconcileAbsorbedSourcePullRequestsWith(context.Background(), &receipt, []string{source, source}, remote, func(WorktreeMergeReceipt) error { persisted++; return nil })
	if err != nil || len(receipt.SourcePullRequests) != 1 || receipt.SourcePullRequests[0].Number != 7 || receipt.SourcePullRequests[0].Outcome != "already_closed" || remote.posted[7] != 0 || remote.closed[7] != 0 || persisted != 1 {
		t.Fatalf("receipt=%+v posted=%v closed=%v persisted=%d error=%v", receipt.SourcePullRequests, remote.posted, remote.closed, persisted, err)
	}
}

func TestReconcileAbsorbedSourcePullRequestsStopsOnRemoteOrReceiptFailure(t *testing.T) {
	t.Parallel()
	const source = "1111111111111111111111111111111111111111"
	cases := []struct {
		name       string
		configure  func(*fakeSourcePullRequestRemote)
		persistErr error
		want       string
		wantClose  int
	}{
		{"association read", func(r *fakeSourcePullRequestRemote) { r.associatedErr = errors.New("association unavailable") }, nil, "association unavailable", 0},
		{"comment read", func(r *fakeSourcePullRequestRemote) { r.hasCommentErr = errors.New("comment history unavailable") }, nil, "comment history unavailable", 0},
		{"comment write", func(r *fakeSourcePullRequestRemote) { r.commentErr = errors.New("comment refused") }, nil, "comment refused", 0},
		{"comment receipt", func(*fakeSourcePullRequestRemote) {}, errors.New("receipt unavailable"), "receipt unavailable", 0},
		{"close write", func(r *fakeSourcePullRequestRemote) { r.closeErr = errors.New("close refused") }, nil, "close refused", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			remote := &fakeSourcePullRequestRemote{byHead: map[string][]PullRequestView{source: {sourcePullRequestView(7, "open", source, "main")}}, comments: map[int]bool{}, closed: map[int]int{}, posted: map[int]int{}}
			tc.configure(remote)
			receipt := WorktreeMergeReceipt{Repository: "acme/app", Target: "main", LandingSHA: "landed"}
			err := reconcileAbsorbedSourcePullRequestsWith(context.Background(), &receipt, []string{source}, remote, func(WorktreeMergeReceipt) error { return tc.persistErr })
			if err == nil || !strings.Contains(err.Error(), tc.want) || remote.closed[7] != tc.wantClose {
				t.Fatalf("error=%v closed=%v posted=%v", err, remote.closed, remote.posted)
			}
		})
	}
}
