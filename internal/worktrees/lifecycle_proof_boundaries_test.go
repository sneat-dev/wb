package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/worktreeproof"
)

type lifecycleGitReply struct {
	operation string
	output    string
	err       error
}

func lifecycleGitContext(t *testing.T, repository string, replies ...lifecycleGitReply) context.Context {
	t.Helper()
	fake := runnertest.New(t)
	for _, reply := range replies {
		reply := reply
		fake.Expect(func(call runnertest.Call) bool {
			return call.Name == "git" && len(call.Args) >= 3 && call.Args[0] == "-C" &&
				call.Args[1] == repository && call.Args[2] == reply.operation
		}, runner.Result{Stdout: reply.output, CombinedOutput: reply.output}, reply.err)
	}
	return withGitRunner(context.Background(), fake)
}

func TestLifecycleProofGitOutputParsers(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	parent := strings.Repeat("b", 40)

	branch, err := worktreeproof.ParseRemoteDefaultBranch("noise\nref: refs/heads/main HEAD\n", func(value string) bool { return value == "main" })
	if err != nil || branch != "main" {
		t.Fatalf("default branch = %q, %v", branch, err)
	}
	if _, err := worktreeproof.ParseRemoteDefaultBranch("ref: refs/tags/v1 HEAD\n", func(string) bool { return true }); err == nil {
		t.Fatal("tag accepted as the remote default branch")
	}
	if _, err := worktreeproof.ParseRemoteDefaultBranch("ref: refs/heads/bad HEAD\n", func(string) bool { return false }); err == nil {
		t.Fatal("invalid remote default branch accepted")
	}
	if got, err := worktreeproof.ParseCommitFirstParent("revision", sha); err != nil || got != "" {
		t.Fatalf("root first parent = %q, %v", got, err)
	}
	if got, err := worktreeproof.ParseCommitFirstParent("revision", sha+" "+parent); err != nil || got != parent {
		t.Fatalf("first parent = %q, %v", got, err)
	}
	if _, err := worktreeproof.ParseCommitFirstParent("revision", sha+" invalid"); err == nil {
		t.Fatal("invalid first parent accepted")
	}
	if got, err := worktreeproof.ParseCommitTree("revision", sha); err != nil || got != sha {
		t.Fatalf("commit tree = %q, %v", got, err)
	}
	if _, err := worktreeproof.ParseCommitTree("revision", "invalid"); err == nil {
		t.Fatal("invalid tree accepted")
	}
}

func TestLifecycleProofGitHubBranchReader(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, err := landingReceiptService().GitHubPullRequestsForBranch(ctx, "", "invalid", "source", "main"); err == nil {
		t.Fatal("branch pull-request query accepted an invalid repository")
	}

	execute := func(_ context.Context, _ string, args ...string) githubobserver.CommandResponse {
		if len(args) != 3 || args[0] != "api" || args[1] != "--paginate" ||
			!strings.Contains(args[2], "base=main") || !strings.Contains(args[2], "head=acme%3Asource") {
			t.Fatalf("GitHub query arguments = %q", args)
		}
		return githubobserver.CommandResponse{Stdout: []byte(`[{"number":7,"state":"closed"}]`)}
	}
	pullRequests, err := githubPullRequestsForBranchWithExecute(ctx, "/worktree", "acme/app", "source", "main", execute)
	if err != nil || len(pullRequests) != 1 || pullRequests[0].Number != 7 {
		t.Fatalf("branch pull requests = %#v, %v", pullRequests, err)
	}
	boom := errors.New("GitHub unavailable")
	if _, err := githubPullRequestsForBranchWithExecute(ctx, "/worktree", "acme/app", "source", "main", func(context.Context, string, ...string) githubobserver.CommandResponse {
		return githubobserver.CommandResponse{Err: boom, Stderr: []byte("offline")}
	}); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("GitHub failure = %v", err)
	}
	if _, err := githubPullRequestsForBranchWithExecute(ctx, "/worktree", "acme/app", "source", "main", func(context.Context, string, ...string) githubobserver.CommandResponse {
		return githubobserver.CommandResponse{Stdout: []byte("not-json")}
	}); err == nil || !strings.Contains(err.Error(), "decode pull requests") {
		t.Fatalf("GitHub decode failure = %v", err)
	}
}

func TestLifecycleProofPullRequestResponseValidation(t *testing.T) {
	t.Parallel()
	mergedAt := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	head := strings.Repeat("a", 40)
	merge := strings.Repeat("b", 40)
	validBody := `{"number":9,"html_url":"https://example.test/9","state":"closed","merged_at":"` + mergedAt.Format(time.RFC3339) + `","base":{"ref":"main","sha":"` + head + `"},"head":{"sha":"` + head + `"},"merge_commit_sha":"` + merge + `"}`

	get := func(body string, err error) func(context.Context, githubobserver.GetRequest) (githubobserver.Response, error) {
		return func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
			if request.Endpoint != "repos/acme/app/pulls/9" || request.FreshWindow != 0 {
				t.Fatalf("GitHub request = %#v", request)
			}
			return githubobserver.Response{Body: []byte(body)}, err
		}
	}
	if _, _, _, err := resolveAbsorbedByPullRequestWithGet(context.Background(), "/worktree", "acme/app", "main", 9, get("", errors.New("offline"))); err == nil {
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
			_, _, rejection, err := resolveAbsorbedByPullRequestWithGet(context.Background(), "/worktree", "acme/app", "main", 9, get(tc.body, nil))
			combined := rejection
			if err != nil {
				combined += err.Error()
			}
			if !strings.Contains(combined, tc.want) {
				t.Fatalf("result rejection=%q err=%v, want %q", rejection, err, tc.want)
			}
		})
	}
	landing, receipt, rejection, err := resolveAbsorbedByPullRequestWithGet(context.Background(), "/worktree", "acme/app", "main", 9, get(validBody, nil))
	if err != nil || rejection != "" || landing != merge || receipt == nil || receipt.HeadSHA != head {
		t.Fatalf("valid pull request = %q, %#v, %q, %v", landing, receipt, rejection, err)
	}
}

//nolint:paralleltest // This test installs temporary entries in the package-wide branch validation cache.
func TestLifecycleProofExactReceiptSelection(t *testing.T) {
	validBranchMemo.Store("source", true)
	validBranchMemo.Store("main", true)
	validBranchMemo.Store("bad\nbase", false)
	t.Cleanup(func() {
		validBranchMemo.Delete("source")
		validBranchMemo.Delete("main")
		validBranchMemo.Delete("bad\nbase")
	})
	head := strings.Repeat("a", 40)
	merge := strings.Repeat("b", 40)
	mergedAt := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	candidate := githubPullRequest{
		Number: 1, State: "closed", MergedAt: &mergedAt,
		Head: githubRef{Ref: "source", SHA: head}, Base: githubRef{Ref: "main"}, MergeCommitSHA: merge,
	}
	duplicate := candidate
	duplicate.Number = 2
	if _, err := landingReceiptService().SelectExactDeletedTargetDefaultBranchReceipt(context.Background(), "acme/app", []githubPullRequest{candidate, duplicate}, "source", "main", head); err == nil {
		t.Fatal("ambiguous exact pull-request receipts accepted")
	}
	if _, err := exactDeletedTargetDefaultBranchReceipt(context.Background(), "", "acme/app", "source", "main", "invalid"); err == nil {
		t.Fatal("invalid deleted-target recovery identity accepted")
	}
	if target, ok := mergedPullRequestTarget(context.Background(), []githubPullRequest{{Head: githubRef{SHA: head}, Base: githubRef{Ref: "bad\nbase"}, MergedAt: &mergedAt}}, head, "source"); ok || target != "" {
		t.Fatalf("invalid replacement target = %q, %t", target, ok)
	}
	if receipt := absorbingPullRequest([]githubPullRequest{candidate}, "main"); receipt == nil || receipt.Number != candidate.Number {
		t.Fatalf("absorbing pull request = %#v", receipt)
	}
}

func TestLifecycleProofFetchExactRemotePullRequestHeadFailures(t *testing.T) {
	t.Parallel()
	expected := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	if _, err := landingReceiptService().FetchExactRemotePullRequestHeadWithRun(context.Background(), "repo", 0, expected, nil); err == nil {
		t.Fatal("non-positive pull request number accepted")
	}
	if _, err := landingReceiptService().FetchExactRemotePullRequestHeadWithRun(context.Background(), "repo", 1, "invalid", nil); err == nil {
		t.Fatal("invalid expected head accepted")
	}
	for _, tc := range []struct {
		name    string
		results []struct {
			output string
			err    error
		}
		want string
	}{
		{name: "remote error", results: []struct {
			output string
			err    error
		}{{err: errors.New("offline")}}, want: "offline"},
		{name: "malformed remote", results: []struct {
			output string
			err    error
		}{{output: "malformed"}}, want: "malformed"},
		{name: "remote mismatch", results: []struct {
			output string
			err    error
		}{{output: other + " refs/pull/1/head"}}, want: "expected exact API head"},
		{name: "fetch error", results: []struct {
			output string
			err    error
		}{{output: expected + " refs/pull/1/head"}, {err: errors.New("fetch failed")}}, want: "fetch failed"},
		{name: "resolve error", results: []struct {
			output string
			err    error
		}{{output: expected + " refs/pull/1/head"}, {}, {err: errors.New("resolve failed")}}, want: "resolve failed"},
		{name: "fetched mismatch", results: []struct {
			output string
			err    error
		}{{output: expected + " refs/pull/1/head"}, {}, {output: other}}, want: "expected exact API head"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := 0
			run := func(context.Context, ...string) (string, error) {
				result := tc.results[call]
				call++
				return result.output, result.err
			}
			if _, err := landingReceiptService().FetchExactRemotePullRequestHeadWithRun(context.Background(), "repo", 1, expected, run); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("fetch error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLifecycleProofAttestedLandingPaths(t *testing.T) {
	t.Parallel()
	repository := "/repository"
	head := strings.Repeat("a", 40)
	landing := strings.Repeat("b", 40)
	tree := strings.Repeat("c", 40)
	ctx := lifecycleGitContext(t, repository,
		lifecycleGitReply{operation: "rev-parse", output: landing},
		lifecycleGitReply{operation: "merge-tree", output: tree},
		lifecycleGitReply{operation: "rev-parse", output: tree},
		lifecycleGitReply{operation: "merge-tree", output: tree},
		lifecycleGitReply{operation: "rev-parse", output: tree},
		lifecycleGitReply{operation: "rev-list", output: landing},
	)
	receipt, rejection, err := attestedAbsorbedReceipt(ctx, "/worktree", repository, "acme/app", head, "main", landing, landing)
	if err != nil || rejection != "" || receipt == nil || receipt.LandingSHA != landing {
		t.Fatalf("attested landing = %#v, %q, %v", receipt, rejection, err)
	}
}

func TestLifecycleProofAttestedPullRequestShapes(t *testing.T) {
	t.Parallel()
	repository := "/repository"
	source := strings.Repeat("a", 40)
	pullHead := strings.Repeat("b", 40)
	merge := strings.Repeat("c", 40)
	tree := strings.Repeat("d", 40)
	mergedAt := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	pullRequest := &PullRequest{Number: 7, Base: "main", HeadSHA: pullHead, MergeSHA: merge, Merged: &mergedAt}
	ctx := lifecycleGitContext(t, repository,
		lifecycleGitReply{operation: "ls-remote", output: pullHead + " refs/pull/7/head"},
		lifecycleGitReply{operation: "fetch"},
		lifecycleGitReply{operation: "rev-parse", output: pullHead},
		lifecycleGitReply{operation: "merge-base"},
		lifecycleGitReply{operation: "rev-parse", output: tree},
		lifecycleGitReply{operation: "rev-parse", output: tree},
	)
	if rejection, err := landingReceiptService().VerifyAttestedSquashPullRequest(ctx, repository, source, merge, "#7", pullRequest); err != nil || rejection != "" {
		t.Fatalf("squash receipt = %q, %v", rejection, err)
	}
	if rejection, err := landingReceiptService().VerifyAttestedMergeCommitPullRequest(context.Background(), repository, merge, merge, "#7", pullRequest); err != nil || rejection != "" {
		t.Fatalf("merge receipt = %q, %v", rejection, err)
	}
	if ok, err := rebaseMergedPullRequestIntegrated(lifecycleGitContext(t, repository,
		lifecycleGitReply{operation: "rev-parse", output: tree},
		lifecycleGitReply{operation: "rev-parse", output: tree},
	), repository, pullHead, merge, pullRequest); err != nil || !ok {
		t.Fatalf("rebase receipt = %t, %v", ok, err)
	}
}

func TestLifecycleProofAttestedPullRequestFailures(t *testing.T) {
	t.Parallel()
	repository := "/repository"
	source := strings.Repeat("a", 40)
	pullHead := strings.Repeat("b", 40)
	merge := strings.Repeat("c", 40)
	target := strings.Repeat("d", 40)
	tree := strings.Repeat("e", 40)
	mergedAt := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	pullRequest := &PullRequest{Number: 7, Base: "main", HeadSHA: pullHead, MergeSHA: merge, Merged: &mergedAt}
	boom := errors.New("injected proof failure")

	if rejection, err := landingReceiptService().VerifyAttestedSquashPullRequest(context.Background(), repository, source, target, "#7", nil); err != nil || !strings.Contains(rejection, "incomplete") {
		t.Fatalf("incomplete squash receipt = %q, %v", rejection, err)
	}
	if rejection, err := landingReceiptService().VerifyAttestedMergeCommitPullRequest(context.Background(), repository, source, target, "#7", nil); err != nil || !strings.Contains(rejection, "incomplete") {
		t.Fatalf("incomplete merge receipt = %q, %v", rejection, err)
	}

	squashCases := []struct {
		name    string
		source  string
		target  string
		replies []lifecycleGitReply
	}{
		{name: "fetch", source: source, target: target, replies: []lifecycleGitReply{{operation: "ls-remote", err: boom}}},
		{name: "source ancestry", source: source, target: target, replies: []lifecycleGitReply{
			{operation: "ls-remote", output: pullHead + " refs/pull/7/head"}, {operation: "fetch"},
			{operation: "rev-parse", output: pullHead}, {operation: "merge-base", err: boom},
		}},
		{name: "merge ancestry", source: pullHead, target: target, replies: []lifecycleGitReply{
			{operation: "ls-remote", output: pullHead + " refs/pull/7/head"}, {operation: "fetch"},
			{operation: "rev-parse", output: pullHead}, {operation: "merge-base", err: boom},
		}},
		{name: "pull request tree", source: pullHead, target: merge, replies: []lifecycleGitReply{
			{operation: "ls-remote", output: pullHead + " refs/pull/7/head"}, {operation: "fetch"},
			{operation: "rev-parse", output: pullHead}, {operation: "rev-parse", err: boom},
		}},
		{name: "merge tree", source: pullHead, target: merge, replies: []lifecycleGitReply{
			{operation: "ls-remote", output: pullHead + " refs/pull/7/head"}, {operation: "fetch"},
			{operation: "rev-parse", output: pullHead}, {operation: "rev-parse", output: tree},
			{operation: "rev-parse", err: boom},
		}},
	}
	for _, tc := range squashCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := landingReceiptService().VerifyAttestedSquashPullRequest(lifecycleGitContext(t, repository, tc.replies...), repository, tc.source, tc.target, "#7", pullRequest); err == nil {
				t.Fatal("squash proof failure was accepted")
			}
		})
	}

	if _, err := landingReceiptService().VerifyAttestedMergeCommitPullRequest(lifecycleGitContext(t, repository,
		lifecycleGitReply{operation: "merge-base", err: boom},
	), repository, source, target, "#7", pullRequest); err == nil {
		t.Fatal("merge-target ancestry failure was accepted")
	}
	if _, err := landingReceiptService().VerifyAttestedMergeCommitPullRequest(lifecycleGitContext(t, repository,
		lifecycleGitReply{operation: "merge-base", err: boom},
	), repository, source, merge, "#7", pullRequest); err == nil {
		t.Fatal("source-target ancestry failure was accepted")
	}

	rebasePullRequest := &PullRequest{HeadSHA: source, MergeSHA: merge}
	if _, err := rebaseMergedPullRequestIntegrated(lifecycleGitContext(t, repository,
		lifecycleGitReply{operation: "merge-base", err: boom},
	), repository, source, target, rebasePullRequest); err == nil {
		t.Fatal("rebase merge-target ancestry failure was accepted")
	}
	if _, err := rebaseMergedPullRequestIntegrated(lifecycleGitContext(t, repository,
		lifecycleGitReply{operation: "rev-parse", err: boom},
	), repository, source, merge, rebasePullRequest); err == nil {
		t.Fatal("rebase source-tree failure was accepted")
	}
	if _, err := rebaseMergedPullRequestIntegrated(lifecycleGitContext(t, repository,
		lifecycleGitReply{operation: "rev-parse", output: tree},
		lifecycleGitReply{operation: "rev-parse", err: boom},
	), repository, source, merge, rebasePullRequest); err == nil {
		t.Fatal("rebase merge-tree failure was accepted")
	}
}

func TestLifecycleProofCleanupBoundaries(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	mergedAt := now.Add(-time.Minute)
	entry := ListResult{Clean: true, IntegratedAtOrigin: true, MergedPullRequest: &PullRequest{Merged: &mergedAt}}
	if eligible, reason := cleanupSafetyEligibility(entry, time.Hour, now, false); eligible || !strings.Contains(reason, "newer") {
		t.Fatalf("recent merge eligibility = %t, %q", eligible, reason)
	}
	residue := ListResult{Clean: true, Landing: &LandingEvidence{
		LandedSHA: strings.Repeat("a", 40), LandingSHA: strings.Repeat("b", 40),
		Residue: []ResidualCommit{{SHA: strings.Repeat("c", 40)}},
	}}
	if eligible, reason := cleanupSafetyEligibility(residue, 0, now, false); eligible || !strings.Contains(reason, "landed + residue") {
		t.Fatalf("residual landing eligibility = %t, %q", eligible, reason)
	}

	proofEntry := ListResult{WorktreeDir: "/worktree"}
	if err := applyMergeReceiptCleanupProof(context.Background(), []MergeReceiptCleanupProof{{SourceWorktree: "/other"}}, &proofEntry); err != nil {
		t.Fatal(err)
	}
	if rejection := mergeReceiptCleanupProofRejection(context.Background(), MergeReceiptCleanupProof{}, proofEntry); !strings.Contains(rejection, "receipt has no") {
		t.Fatalf("empty receipt rejection = %q", rejection)
	}
	if _, err := preflightCleanupRepository(context.Background(), CleanupOptions{}, now, &cleanupTaskHandle{}, CleanupResult{}, ""); err == nil {
		t.Fatal("preflight accepted an invalid cleanup task handle")
	}
	if directory, path, err := localOriginDirectoryForSecurePush(context.Background(), nil, []string{"status"}); err != nil || directory != nil || path != "" {
		t.Fatalf("non-push local origin = %v, %q, %v", directory, path, err)
	}
	canonical := &canonicalRepository{path: t.TempDir()}
	if _, _, err := localOriginDirectoryForSecurePush(context.Background(), canonical, []string{"push", "missing"}); err == nil || !strings.Contains(err.Error(), "resolve local push remote") {
		t.Fatalf("missing relative push remote error = %v", err)
	}
	fileRemote := filepath.Join(canonical.path, "remote-file")
	if err := os.WriteFile(fileRemote, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := localOriginDirectoryForSecurePush(context.Background(), canonical, []string{"push", fileRemote}); err == nil || !strings.Contains(err.Error(), "open local push remote") {
		t.Fatalf("file push remote error = %v", err)
	}
}

func TestLifecycleProofMergeReceiptGitFailures(t *testing.T) {
	t.Parallel()
	repository := "/repository"
	source := strings.Repeat("a", 40)
	landing := strings.Repeat("b", 40)
	target := strings.Repeat("c", 40)
	tree := strings.Repeat("d", 40)
	boom := errors.New("injected receipt failure")
	proof := MergeReceiptCleanupProof{
		Repository: "acme/app", Target: "main", SourceTask: "task", SourceWorktree: "/worktree",
		SourceBranch: "source", SourceSHA: source, CandidateSHA: source, LandingSHA: landing,
	}
	entry := ListResult{
		Repository: "acme/app", Base: "main", Task: "task", WorktreeDir: "/worktree",
		Branch: "source", HeadSHA: source, RemoteTargetSHA: target, CanonicalDir: repository,
	}
	for _, tc := range []struct {
		name    string
		replies []lifecycleGitReply
		want    string
	}{
		{name: "candidate tree", replies: []lifecycleGitReply{{operation: "rev-parse", err: boom}}, want: "resolve candidate tree"},
		{name: "landing tree", replies: []lifecycleGitReply{{operation: "rev-parse", output: tree}, {operation: "rev-parse", err: boom}}, want: "resolve landing tree"},
		{name: "landing ancestry", replies: []lifecycleGitReply{{operation: "rev-parse", output: tree}, {operation: "rev-parse", output: tree}, {operation: "merge-base", err: boom}}, want: "verify landing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rejection := mergeReceiptCleanupProofRejection(lifecycleGitContext(t, repository, tc.replies...), proof, entry)
			if !strings.Contains(rejection, tc.want) {
				t.Fatalf("rejection = %q, want %q", rejection, tc.want)
			}
		})
	}
	if contained, err := contentContained(lifecycleGitContext(t, repository,
		lifecycleGitReply{operation: "merge-tree", err: boom},
	), repository, source, landing); err == nil || contained {
		t.Fatalf("content failure = %t, %v", contained, err)
	}
}

func TestLifecycleProofRemoteAndCommitFailures(t *testing.T) {
	t.Parallel()
	repository := "/repository"
	boom := errors.New("injected failure")
	if _, err := remoteDefaultBranch(lifecycleGitContext(t, repository, lifecycleGitReply{operation: "ls-remote", err: boom}), repository); err == nil || !strings.Contains(err.Error(), "read origin default branch") {
		t.Fatalf("remote default failure = %v", err)
	}
	if _, err := worktreeproof.CommitFirstParent(lifecycleGitContext(t, repository, lifecycleGitReply{operation: "rev-list", err: boom}), repository, "revision", git); err == nil || !strings.Contains(err.Error(), "resolve parents") {
		t.Fatalf("first-parent failure = %v", err)
	}
	if _, err := commitTree(lifecycleGitContext(t, repository, lifecycleGitReply{operation: "rev-parse", err: boom}), repository, "revision"); err == nil || !strings.Contains(err.Error(), "resolve tree") {
		t.Fatalf("tree failure = %v", err)
	}
	if _, _, rejection, err := landingReceiptService().ResolveAbsorbedBy(context.Background(), "", repository, "acme/app", "main", ""); err != nil || !strings.Contains(rejection, "requires") {
		t.Fatalf("empty absorbed-by = %q, %v", rejection, err)
	}
	if _, _, rejection, err := landingReceiptService().ResolveAbsorbedBy(context.Background(), "", repository, "acme/app", "main", "https://github.com/acme/other/pull/1"); err != nil || !strings.Contains(rejection, "not the requested") {
		t.Fatalf("cross-repository absorbed-by = %q, %v", rejection, err)
	}
	if _, _, rejection, err := landingReceiptService().ResolveAbsorbedBy(context.Background(), "", repository, "acme/app", "main", "#0"); err != nil || !strings.Contains(rejection, "not positive") {
		t.Fatalf("non-positive absorbed-by = %q, %v", rejection, err)
	}
	tooLarge := "https://github.com/acme/app/pull/999999999999999999999999999999999999999999"
	if _, _, rejection, err := landingReceiptService().ResolveAbsorbedBy(context.Background(), "", repository, "acme/app", "main", tooLarge); err != nil || !strings.Contains(rejection, "invalid pull request number") {
		t.Fatalf("oversized absorbed-by = %q, %v", rejection, err)
	}
	if _, _, rejection, err := landingReceiptService().ResolveAbsorbedBy(lifecycleGitContext(t, repository,
		lifecycleGitReply{operation: "rev-parse", output: "invalid"},
	), "", repository, "acme/app", "main", "landing"); err != nil || !strings.Contains(rejection, "invalid commit") {
		t.Fatalf("invalid commit absorbed-by = %q, %v", rejection, err)
	}
}
