package worktreelanding

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGitHubPullRequestsForBranchQueriesClosedPullRequestsForTheExactSourceBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := ReceiptService{}
	if _, err := service.GitHubPullRequestsForBranch(ctx, "", "invalid", "source", "main"); err == nil {
		t.Fatal("branch pull-request query accepted an invalid repository")
	}

	execute := func(_ context.Context, dir string, args ...string) GitHubCommand {
		if dir != "/worktree" || len(args) != 3 || args[0] != "api" || args[1] != "--paginate" ||
			!strings.Contains(args[2], "repos/acme/app/pulls?") || !strings.Contains(args[2], "base=main") ||
			!strings.Contains(args[2], "head=acme%3Asource") || !strings.Contains(args[2], "state=closed") {
			t.Fatalf("GitHub query dir=%q arguments=%q", dir, args)
		}
		return GitHubCommand{Stdout: []byte(`[{"number":7,"state":"closed"}]`)}
	}
	pullRequests, err := service.GitHubPullRequestsForBranchWithExecute(ctx, "/worktree", "acme/app", "source", "main", execute)
	if err != nil || len(pullRequests) != 1 || pullRequests[0].Number != 7 {
		t.Fatalf("branch pull requests = %#v, %v", pullRequests, err)
	}
	boom := errors.New("GitHub unavailable")
	if _, err := service.GitHubPullRequestsForBranchWithExecute(ctx, "/worktree", "acme/app", "source", "main", func(context.Context, string, ...string) GitHubCommand {
		return GitHubCommand{Err: boom, Stderr: []byte("offline")}
	}); err == nil || !strings.Contains(err.Error(), "offline") || !errors.Is(err, boom) {
		t.Fatalf("GitHub failure = %v", err)
	}
	if _, err := service.GitHubPullRequestsForBranchWithExecute(ctx, "/worktree", "acme/app", "source", "main", func(context.Context, string, ...string) GitHubCommand {
		return GitHubCommand{Stdout: []byte("not-json")}
	}); err == nil || !strings.Contains(err.Error(), "decode pull requests") {
		t.Fatalf("GitHub decode failure = %v", err)
	}
	for _, tc := range []struct{ name, repository, branch, base string }{
		{"no owner", "app", "source", "main"},
		{"empty owner", "/app", "source", "main"},
		{"no branch", "acme/app", "", "main"},
		{"no base", "acme/app", "source", ""},
	} {
		if _, err := service.GitHubPullRequestsForBranchWithExecute(ctx, "/worktree", tc.repository, tc.branch, tc.base, execute); err == nil {
			t.Fatalf("%s: incomplete query accepted", tc.name)
		}
	}
}

func TestResolveAbsorbedByPullRequestRefusesEveryUnprovenPullRequest(t *testing.T) {
	t.Parallel()
	mergedAt := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	head := strings.Repeat("a", 40)
	merge := strings.Repeat("b", 40)
	validBody := `{"number":9,"html_url":"https://example.test/9","state":"closed","merged_at":"` + mergedAt.Format(time.RFC3339) + `","base":{"ref":"main","sha":"` + head + `"},"head":{"sha":"` + head + `"},"merge_commit_sha":"` + merge + `"}`
	service := ReceiptService{}

	get := func(body string, err error) func(context.Context, GitHubGetRequest) ([]byte, error) {
		return func(_ context.Context, request GitHubGetRequest) ([]byte, error) {
			if request.Endpoint != "repos/acme/app/pulls/9" || request.Dir != "/worktree" || request.Repository != "acme/app" || request.Target != "main" {
				t.Fatalf("GitHub request = %#v", request)
			}
			return []byte(body), err
		}
	}
	if _, _, _, err := service.ResolveAbsorbedByPullRequestWithGet(context.Background(), "/worktree", "acme/app", "main", 9, get("", errors.New("offline"))); err == nil {
		t.Fatal("GitHub read failure accepted")
	}
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "malformed", body: "not-json", want: "decode"},
		{name: "not merged", body: `{"state":"closed"}`, want: "not merged"},
		{name: "not closed", body: strings.Replace(validBody, `"state":"closed"`, `"state":"open"`, 1), want: "not closed"},
		{name: "wrong base", body: strings.Replace(validBody, `"ref":"main"`, `"ref":"release"`, 1), want: "not the requested base"},
		{name: "invalid merge", body: strings.Replace(validBody, merge, "invalid", 1), want: "invalid merge commit"},
		{name: "invalid head", body: strings.Replace(validBody, `"head":{"sha":"`+head+`"}`, `"head":{"sha":"invalid"}`, 1), want: "invalid head commit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, rejection, err := service.ResolveAbsorbedByPullRequestWithGet(context.Background(), "/worktree", "acme/app", "main", 9, get(tc.body, nil))
			combined := rejection
			if err != nil {
				combined += err.Error()
			}
			if !strings.Contains(combined, tc.want) {
				t.Fatalf("result rejection=%q err=%v, want %q", rejection, err, tc.want)
			}
		})
	}
	landing, receipt, rejection, err := service.ResolveAbsorbedByPullRequestWithGet(context.Background(), "/worktree", "acme/app", "main", 9, get(validBody, nil))
	if err != nil || rejection != "" || landing != merge || receipt == nil || receipt.HeadSHA != head || receipt.MergeSHA != merge || receipt.Number != 9 {
		t.Fatalf("valid pull request = %q, %#v, %q, %v", landing, receipt, rejection, err)
	}
}
