package worktreelanding

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReceiptReadersRefuseIncompleteEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	head := strings.Repeat("a", 40)
	merged := time.Now()
	response := GitHubCommand{Stdout: []byte(`[{"number":7,"state":"closed","merged_at":"` + merged.Format(time.RFC3339) + `","base":{"ref":"main"},"head":{"ref":"source","sha":"` + head + `"},"merge_commit_sha":"` + strings.Repeat("b", 40) + `"}]`)}
	service := ReceiptService{Ports: ReceiptPorts{
		ValidBranch:   func(_ context.Context, name string) bool { return name == "source" || name == "main" },
		GitHubExecute: func(context.Context, string, ...string) GitHubCommand { return response },
	}}
	if receipt, err := service.ExactDeletedTargetDefaultBranchReceipt(ctx, "/worktree", "acme/app", "source", "main", head); err != nil || receipt == nil || receipt.Number != 7 {
		t.Fatalf("exact receipt = %#v, %v", receipt, err)
	}
	service.Ports.GitHubExecute = func(context.Context, string, ...string) GitHubCommand {
		return GitHubCommand{Err: errors.New("offline")}
	}
	if _, err := service.ExactDeletedTargetDefaultBranchReceipt(ctx, "/worktree", "acme/app", "source", "main", head); err == nil {
		t.Fatal("GitHub failure accepted as a receipt")
	}
	service.Ports.GitHubExecute = func(context.Context, string, ...string) GitHubCommand {
		return GitHubCommand{Stdout: []byte("broken JSON")}
	}
	if _, _, err := service.GitHubPullRequestsForCommit(ctx, "/worktree", "acme/app", head); err == nil {
		t.Fatal("malformed commit response accepted")
	}
}

func TestReceiptContentProofFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	head := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)
	boom := errors.New("proof unavailable")
	service := ReceiptService{Ports: ReceiptPorts{
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			if args[0] != "rev-parse" {
				t.Fatalf("unexpected Git call: %q", args)
			}
			return "", boom
		},
		MergeTreeRun: func(context.Context, string, string, string) (string, string, int, error) { return tree, "", 0, nil },
	}}
	if _, err := service.ContentContained(ctx, "repo", head, head); !errors.Is(err, boom) {
		t.Fatalf("unreadable commit tree error = %v", err)
	}
	service.Ports.MergeTreeRun = func(context.Context, string, string, string) (string, string, int, error) {
		return "invalid", "", 0, nil
	}
	if _, _, err := service.MergeResultTree(ctx, "repo", head, head); err == nil || !strings.Contains(err.Error(), "invalid tree") {
		t.Fatalf("invalid merge tree = %v", err)
	}
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	requests := []GitHubPullRequest{
		{Number: 0, Base: GitHubRef{Ref: "main"}},
		{Number: 0, MergedAt: &older, Base: GitHubRef{Ref: "release"}},
		{Number: 1, MergedAt: &older, Base: GitHubRef{Ref: "main"}},
		{Number: 2, MergedAt: &newer, Base: GitHubRef{Ref: "main"}},
		{Number: 3, MergedAt: &older, Base: GitHubRef{Ref: "main"}},
	}
	if got := service.AbsorbingPullRequest(requests, "main"); got == nil || got.Number != 2 {
		t.Fatalf("newest exact-base receipt = %#v", got)
	}
}

func TestAttestedPullRequestRefusalPaths(t *testing.T) {
	t.Parallel()
	source := strings.Repeat("a", 40)
	pullHead := strings.Repeat("b", 40)
	merge := strings.Repeat("c", 40)
	target := strings.Repeat("d", 40)
	tree := strings.Repeat("e", 40)
	mergedAt := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	request := &PullRequest{Number: 7, Base: "main", HeadSHA: pullHead, MergeSHA: merge, Merged: &mergedAt}
	boom := errors.New("proof unavailable")
	for _, tc := range []struct {
		name, stage, want string
		attested          bool
		wantErr           bool
	}{
		{"ref mismatch", "mismatch", "expected exact API head", false, false},
		{"source absent from PR", "source absent", "does not contain exact source", false, false},
		{"merge absent from target", "merge absent", "not contained", false, false},
		{"merge receipt absent from target", "merge absent", "not contained", false, false},
		{"source absent from merge target", "source target absent", "exact source head", false, false},
		{"attested squash proof error", "fetch error", "", true, true},
		{"attested merge proof error", "merge error", "", true, true},
		{"attested both shapes refused", "both refused", "not contained", true, false},
		{"attested content error", "content error", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service := ReceiptService{Ports: ReceiptPorts{
				Git: func(_ context.Context, _ string, args ...string) (string, error) {
					switch args[0] {
					case "ls-remote":
						if tc.stage == "fetch error" {
							return "", boom
						}
						if tc.stage == "mismatch" || tc.stage == "merge error" || tc.stage == "both refused" {
							return source + " refs/pull/7/head", nil
						}
						return pullHead + " refs/pull/7/head", nil
					case "fetch":
						return "", nil
					case "rev-parse":
						if strings.HasSuffix(args[len(args)-1], "^{commit}") {
							return pullHead, nil
						}
						return tree, nil
					}
					t.Fatalf("unexpected Git call: %q", args)
					return "", nil
				},
				IsAncestor: func(_ context.Context, _ string, ancestor, descendant string) (bool, error) {
					if tc.stage == "merge error" {
						return false, boom
					}
					if tc.stage == "source absent" && ancestor == source && descendant == pullHead ||
						tc.stage == "merge absent" && ancestor == merge && descendant == target ||
						tc.stage == "both refused" && ancestor == merge && descendant == target ||
						tc.stage == "source target absent" && ancestor == source && descendant == target {
						return false, nil
					}
					return true, nil
				},
				GitHubGet: func(context.Context, GitHubGetRequest) ([]byte, error) {
					return []byte(`{"number":7,"state":"closed","merged_at":"` + mergedAt.Format(time.RFC3339) + `","base":{"ref":"main"},"head":{"sha":"` + pullHead + `"},"merge_commit_sha":"` + merge + `"}`), nil
				},
				MergeTreeRun: func(context.Context, string, string, string) (string, string, int, error) {
					if tc.stage == "content error" {
						return "", "unavailable", 2, boom
					}
					return tree, "", 0, nil
				},
			}}
			var rejection string
			var err error
			switch {
			case tc.attested:
				_, rejection, err = service.AttestedAbsorbedReceipt(context.Background(), "/worktree", "repo", "acme/app", source, "main", target, "#7")
			case tc.name == "merge receipt absent from target" || tc.name == "source absent from merge target":
				rejection, err = service.VerifyAttestedMergeCommitPullRequest(context.Background(), "repo", source, target, "#7", request)
			default:
				rejection, err = service.VerifyAttestedSquashPullRequest(context.Background(), "repo", source, target, "#7", request)
			}
			if tc.wantErr && !errors.Is(err, boom) || !tc.wantErr && err != nil || !strings.Contains(rejection, tc.want) {
				t.Fatalf("refusal = %q, error = %v; want %q, error=%t", rejection, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestAttestedReceiptRefusesUnprovedLanding(t *testing.T) {
	t.Parallel()
	const repo = "repo"
	source := strings.Repeat("a", 40)
	landing := strings.Repeat("b", 40)
	target := strings.Repeat("c", 40)
	parent := strings.Repeat("d", 40)
	tree := strings.Repeat("e", 40)
	boom := errors.New("proof unavailable")
	for _, tc := range []struct {
		name, fail string
		wantErr    bool
		want       string
	}{
		{"ancestry failure", "ancestor", true, ""},
		{"landing merge failure", "landing merge", true, ""},
		{"missing landing content", "landing missing", false, "does not contain"},
		{"target merge failure", "target merge", true, ""},
		{"reverted target", "target missing", false, "no longer survives"},
		{"parent failure", "parent", true, ""},
		{"parent merge failure", "parent merge", true, ""},
		{"already contained", "already contained", false, "already contained"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service := ReceiptService{Ports: ReceiptPorts{
				Git: func(_ context.Context, _ string, args ...string) (string, error) {
					switch args[0] {
					case "rev-parse":
						if strings.HasSuffix(args[len(args)-1], "^{commit}") {
							return landing, nil
						}
						return tree, nil
					case "rev-list":
						if tc.fail == "parent" {
							return "", boom
						}
						return landing + " " + parent, nil
					}
					t.Fatalf("unexpected Git call: %q", args)
					return "", nil
				},
				IsAncestor: func(context.Context, string, string, string) (bool, error) {
					if tc.fail == "ancestor" {
						return false, boom
					}
					return true, nil
				},
				MergeTreeRun: func(_ context.Context, _ string, ours, _ string) (string, string, int, error) {
					if tc.fail == "landing merge" && ours == landing || tc.fail == "target merge" && ours == target || tc.fail == "parent merge" && ours == parent {
						return "", "offline", 2, boom
					}
					if tc.fail == "landing missing" && ours == landing || tc.fail == "target missing" && ours == target {
						return source, "", 0, nil
					}
					if ours == parent && tc.fail != "already contained" {
						return source, "", 0, nil
					}
					return tree, "", 0, nil
				},
			}}
			_, rejection, err := service.AttestedAbsorbedReceipt(context.Background(), "/worktree", repo, "acme/app", source, "main", target, landing)
			if tc.wantErr && !errors.Is(err, boom) || !tc.wantErr && err != nil || tc.want != "" && !strings.Contains(rejection, tc.want) {
				t.Fatalf("attested refusal = %q, %v; want %q, error=%t", rejection, err, tc.want, tc.wantErr)
			}
		})
	}
}
