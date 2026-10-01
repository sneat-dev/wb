package worktreelanding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeproof"
)

func TestLandingWalkAndResidue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	calls := []string{}
	git := func(_ context.Context, repo string, args ...string) (string, error) {
		if repo != "repo" {
			t.Fatalf("repo %q", repo)
		}
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "rev-list":
			return "head\nlanded\n", nil
		case "log":
			return "newsha fix one\nother another subject\n", nil
		}
		t.Fatalf("args %q", args)
		return "", nil
	}
	verified := func(_ context.Context, worktree, repo, slug, candidate, base, target string) (*VerifiedCandidate, error) {
		if worktree != "wt" || repo != "repo" || slug != "org/repo" || candidate != "landed" || base != "main" || target != "target" {
			t.Fatalf("verification inputs %q %q %q %q %q %q", worktree, repo, slug, candidate, base, target)
		}
		return &VerifiedCandidate{LandingSHA: "targetsha", PullRequest: &worktreeproof.PullRequest{URL: "https://example.test/1"}}, nil
	}
	got, err := LandingEvidenceFor(ctx, "wt", "repo", "org/repo", "head", "main", "target", 2, git, verified)
	if err != nil || got == nil || got.LandedSHA != "landed" || len(got.Residue) != 2 || got.Residue[0].Subject != "fix one" {
		t.Fatalf("landing %#v %v", got, err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "--max-count=3") || !strings.Contains(calls[1], "--not target landed") {
		t.Fatalf("git calls %q", calls)
	}
	if !LandedWithResidue(got) || LandedWithResidue(nil) || LandedWithResidue(&LandingEvidence{LandedSHA: "x"}) {
		t.Fatal("residue eligibility")
	}
	if !strings.Contains(ResidueReason(got), "via https://example.test/1") || !strings.Contains(ResidueReason(got), "2 residual commits") || got.ResidueSummary() != "newsha fix one; other another subject" {
		t.Fatalf("residue reason %q, summary %q", ResidueReason(got), got.ResidueSummary())
	}
	if ResidueReason(nil) != "" || (*LandingEvidence)(nil).ResidueSummary() != "" || (&LandingEvidence{}).ResidueSummary() != "" {
		t.Fatal("empty residue output")
	}
	if PluralCommits(1) != "1 residual commit" || PluralCommits(2) != "2 residual commits" {
		t.Fatal("plural")
	}
	if !strings.Contains(DetachedRefusal("123456789012345", "main", true), "never pushed") || !strings.Contains(DetachedRefusal("123456789012345", "main", false), "origin") {
		t.Fatal("detached refusal")
	}
	if got := SplitNonEmptyLines("\n a \n\n b\n"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("split %q", got)
	}
}

func TestLandingWalkRefusalsAndFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	git := func(context.Context, string, ...string) (string, error) { return "head\nlanded\nextra\n", nil }
	verify := func(context.Context, string, string, string, string, string, string) (*VerifiedCandidate, error) {
		return nil, nil
	}
	for _, tc := range []struct {
		depth        int
		target, head string
	}{{-1, "target", "head"}, {1, "", "head"}, {1, "target", ""}} {
		got, err := LandingEvidenceFor(ctx, "wt", "repo", "slug", tc.head, "main", tc.target, tc.depth, git, verify)
		if got != nil || err != nil {
			t.Fatalf("invalid input %#v %v", got, err)
		}
	}
	got, err := LandingEvidenceFor(ctx, "wt", "repo", "slug", "head", "main", "target", 2, git, verify)
	if err != nil || got == nil || !got.Truncated {
		t.Fatalf("truncation %#v %v", got, err)
	}
	empty := func(context.Context, string, ...string) (string, error) { return "", nil }
	got, err = LandingEvidenceFor(ctx, "wt", "repo", "slug", "head", "main", "target", 2, empty, verify)
	if err != nil || got != nil {
		t.Fatalf("empty walk %#v %v", got, err)
	}
	bad := errors.New("git failed")
	fail := func(context.Context, string, ...string) (string, error) { return "", bad }
	if _, err := LandingEvidenceFor(ctx, "wt", "repo", "slug", "head", "main", "target", 2, fail, verify); !errors.Is(err, bad) {
		t.Fatalf("git failure %v", err)
	}
	if _, err := ResidualCommits(ctx, "repo", "target", "head", "landed", 2, fail); !errors.Is(err, bad) {
		t.Fatalf("log failure %v", err)
	}
	if _, err := LandingEvidenceFor(ctx, "wt", "repo", "slug", "head", "main", "target", 2, git, func(context.Context, string, string, string, string, string, string) (*VerifiedCandidate, error) {
		return nil, bad
	}); !errors.Is(err, bad) {
		t.Fatalf("verifier failure %v", err)
	}
	zeroDepth := func(_ context.Context, _ string, args ...string) (string, error) {
		if !strings.Contains(args[1], "11") {
			t.Fatalf("default depth %q", args)
		}
		return "", nil
	}
	_, _ = LandingEvidenceFor(ctx, "wt", "repo", "slug", "head", "main", "target", 0, zeroDepth, verify)
	logFail := func(_ context.Context, _ string, args ...string) (string, error) {
		if args[0] == "log" {
			return "", bad
		}
		return "head\nlanded\n", nil
	}
	if _, err := LandingEvidenceFor(ctx, "wt", "repo", "slug", "head", "main", "target", 2, logFail, func(context.Context, string, string, string, string, string, string) (*VerifiedCandidate, error) {
		return &VerifiedCandidate{LandingSHA: "landing"}, nil
	}); !errors.Is(err, bad) {
		t.Fatalf("residual failure %v", err)
	}
	noTrunc := func(context.Context, string, ...string) (string, error) { return "head\n", nil }
	got, err = LandingEvidenceFor(ctx, "wt", "repo", "slug", "head", "main", "target", 2, noTrunc, verify)
	if got != nil || err != nil {
		t.Fatalf("head-only %#v %v", got, err)
	}
	if got := ResidueReason(&LandingEvidence{LandedSHA: "a", LandingSHA: "b"}); !strings.Contains(got, "0 residual commits") {
		t.Fatalf("no PR reason %q", got)
	}
}

func TestRemoteQueriesAndTargetCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	git := func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "ls-remote":
			if args[1] == "--heads" {
				return strings.Repeat("a", 40) + "\trefs/heads/main\n", nil
			}
			return "ref: refs/heads/main HEAD\n", nil
		}
		return "", fmt.Errorf("unexpected %q", args)
	}
	if got, err := RemoteBranchHead(ctx, "repo", "main", git); err != nil || got != strings.Repeat("a", 40) {
		t.Fatalf("branch %q %v", got, err)
	}
	if got, err := RemoteDefaultBranch(ctx, "repo", git, func(v string) bool { return v == "main" }); err != nil || got != "main" {
		t.Fatalf("default %q %v", got, err)
	}
	missing := func(context.Context, string, ...string) (string, error) { return "", nil }
	if got, err := RemoteBranchHead(ctx, "repo", "main", missing); err != nil || got != "" {
		t.Fatalf("missing branch %q %v", got, err)
	}
	malformed := func(context.Context, string, ...string) (string, error) { return "a b c", nil }
	if _, err := RemoteBranchHead(ctx, "repo", "main", malformed); err == nil {
		t.Fatal("malformed branch accepted")
	}
	bad := errors.New("offline")
	fail := func(context.Context, string, ...string) (string, error) { return "", bad }
	if _, err := RemoteBranchHead(ctx, "repo", "main", fail); !errors.Is(err, bad) {
		t.Fatalf("branch failure %v", err)
	}
	if _, err := RemoteDefaultBranch(ctx, "repo", fail, func(string) bool { return true }); !errors.Is(err, bad) {
		t.Fatalf("default failure %v", err)
	}
	for _, message := range []string{"couldn't find remote ref", "could not find remote ref", "remote ref does not exist", "no such ref"} {
		if !IsMissingRemoteTargetError(errors.New(strings.ToUpper(message))) {
			t.Fatalf("missing marker %q", message)
		}
	}
	if IsMissingRemoteTargetError(nil) || IsMissingRemoteTargetError(errors.New("timeout")) {
		t.Fatal("generic remote failure misclassified")
	}
	calls := 0
	cached := WithTargetHeadCache(ctx)
	fetch := func(context.Context, string, string) (string, error) { calls++; return "sha", nil }
	for i := 0; i < 3; i++ {
		if got, err := FetchRemoteTargetHead(cached, "repo", "main", time.Second, fetch); err != nil || got != "sha" {
			t.Fatalf("cached fetch %q %v", got, err)
		}
	}
	if calls != 1 {
		t.Fatalf("cached calls %d", calls)
	}
	if got, err := FetchRemoteTargetHead(ctx, "repo", "main", time.Second, fetch); err != nil || got != "sha" || calls != 2 {
		t.Fatalf("live fetch %q %v, calls %d", got, err, calls)
	}
	if TargetHeadCacheFrom(ctx) != nil || TargetHeadCacheFrom(cached) == nil {
		t.Fatal("cache context scope")
	}
	cache := NewTargetHeadCache()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.Resolve("repo", "main", func() (string, error) { return "sha", nil }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got, err := cache.Resolve("repo", "other", func() (string, error) { return "", bad }); !errors.Is(err, bad) || got != "" {
		t.Fatalf("failure cache %q %v", got, err)
	}
	if got, err := cache.Resolve("repo", "other", func() (string, error) { t.Fatal("failure refetched"); return "", nil }); !errors.Is(err, bad) || got != "" {
		t.Fatalf("memoized failure %q %v", got, err)
	}
	if _, err := FetchRemoteTargetHeadUncached(ctx, "repo", "main", time.Second, func(context.Context, string, string) (string, error) { return "", bad }); !errors.Is(err, bad) {
		t.Fatalf("fetch failure %v", err)
	}
	if _, err := FetchRemoteTargetHeadUncached(ctx, "repo", "main", time.Millisecond, func(fetchCtx context.Context, _, _ string) (string, error) {
		<-fetchCtx.Done()
		return "", fetchCtx.Err()
	}); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("timeout %v", err)
	}
}
