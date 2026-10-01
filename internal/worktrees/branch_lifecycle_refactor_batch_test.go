package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
)

func branchLifecycleQuarantineOps(sha string) branchQuarantinePlanOps {
	return branchQuarantinePlanOps{
		git: func(_ context.Context, _ string, args ...string) (string, error) {
			command := strings.Join(args, " ")
			switch {
			case strings.Contains(command, "^{commit}"):
				return sha + "\n", nil
			case command == "rev-parse --abbrev-ref HEAD":
				return "main\n", nil
			case strings.Contains(command, "refs/heads/retired/"):
				return "", errors.New("not found")
			default:
				return "", fmtUnexpectedArguments(args)
			}
		},
		checkedOut: func(context.Context, string) (map[string]bool, string) {
			return map[string]bool{}, ""
		},
		inUse: func(context.Context, string, string) (map[string]string, string) {
			return map[string]string{}, ""
		},
		pullRequests: func(context.Context, string, string, string) ([]githubPullRequest, error) {
			return nil, nil
		},
		openBasePull: func(context.Context, string, string, string) (*PullRequest, error) {
			return nil, nil
		},
		rename: func(context.Context, string, string, string, string) error { return nil },
	}
}

func fmtUnexpectedArguments(args []string) error {
	return errors.New("unexpected arguments: " + strings.Join(args, " "))
}

func TestBranchLifecycleRefactorBatchQuarantineApply(t *testing.T) {
	t.Parallel()

	const sha = "0123456789abcdef0123456789abcdef01234567"
	request := BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/old", SHA: sha, Reason: "retired"}
	now := time.Date(2026, time.September, 28, 0, 0, 0, 0, time.UTC)
	planned := planBranchQuarantineWithOps(context.Background(), "/projects", "/repo", request, now, branchLifecycleQuarantineOps(sha))
	if planned.Outcome != "planned" {
		t.Fatalf("quarantine plan = %#v", planned)
	}

	result := planned
	applyBranchQuarantineWithOps(context.Background(), "/projects", "/repo", &result, branchLifecycleQuarantineOps(sha))
	if result.Outcome != "quarantined" || result.Error != "" {
		t.Fatalf("quarantine apply = %#v", result)
	}

	for _, tc := range []struct {
		name   string
		change func(*branchQuarantinePlanOps)
		want   string
	}{
		{name: "missing source", change: func(ops *branchQuarantinePlanOps) {
			ops.git = func(context.Context, string, ...string) (string, error) { return "", errors.New("missing") }
		}, want: "source disappeared before apply"},
		{name: "moved source", change: func(ops *branchQuarantinePlanOps) {
			baseGit := ops.git
			ops.git = func(ctx context.Context, path string, args ...string) (string, error) {
				if strings.Contains(strings.Join(args, " "), "^{commit}") {
					return strings.Repeat("f", 40), nil
				}
				return baseGit(ctx, path, args...)
			}
		}, want: "source moved"},
		{name: "protected source", change: func(ops *branchQuarantinePlanOps) {
			baseGit := ops.git
			ops.git = func(ctx context.Context, path string, args ...string) (string, error) {
				if strings.Join(args, " ") == "rev-parse --abbrev-ref HEAD" {
					return request.Ref, nil
				}
				return baseGit(ctx, path, args...)
			}
		}, want: "became protected"},
		{name: "checkout proof", change: func(ops *branchQuarantinePlanOps) {
			ops.checkedOut = func(context.Context, string) (map[string]bool, string) { return nil, "worktree list failed" }
		}, want: "worktree list failed"},
		{name: "checked out", change: func(ops *branchQuarantinePlanOps) {
			ops.checkedOut = func(context.Context, string) (map[string]bool, string) { return map[string]bool{request.Ref: true}, "" }
		}, want: "became checked out"},
		{name: "destination appeared", change: func(ops *branchQuarantinePlanOps) {
			baseGit := ops.git
			ops.git = func(ctx context.Context, path string, args ...string) (string, error) {
				if strings.Contains(strings.Join(args, " "), "refs/heads/retired/") {
					return sha, nil
				}
				return baseGit(ctx, path, args...)
			}
		}, want: "destination appeared"},
		{name: "head pull proof", change: func(ops *branchQuarantinePlanOps) {
			ops.pullRequests = func(context.Context, string, string, string) ([]githubPullRequest, error) {
				return nil, errors.New("offline")
			}
		}, want: "cannot re-prove pull-request safety"},
		{name: "open head pull", change: func(ops *branchQuarantinePlanOps) {
			ops.pullRequests = func(context.Context, string, string, string) ([]githubPullRequest, error) {
				return []githubPullRequest{{Number: 12, URL: "https://example.test/12", State: "open", Head: githubRef{Ref: request.Ref, SHA: sha, Repo: &githubRepository{FullName: request.Repository}}}}, nil
			}
		}, want: "became head of open pull request"},
		{name: "base pull proof", change: func(ops *branchQuarantinePlanOps) {
			ops.openBasePull = func(context.Context, string, string, string) (*PullRequest, error) { return nil, errors.New("offline") }
		}, want: "cannot re-prove pull-request base safety"},
		{name: "open base pull", change: func(ops *branchQuarantinePlanOps) {
			ops.openBasePull = func(context.Context, string, string, string) (*PullRequest, error) {
				return &PullRequest{URL: "https://example.test/13"}, nil
			}
		}, want: "became base of open pull request"},
		{name: "claim proof", change: func(ops *branchQuarantinePlanOps) {
			ops.inUse = func(context.Context, string, string) (map[string]string, string) { return nil, "claim index failed" }
		}, want: "claim index failed"},
		{name: "claimed", change: func(ops *branchQuarantinePlanOps) {
			ops.inUse = func(context.Context, string, string) (map[string]string, string) {
				return map[string]string{branchInUseKey(request.Repository, request.Ref): "task"}, ""
			}
		}, want: "became claimed"},
		{name: "rename", change: func(ops *branchQuarantinePlanOps) {
			ops.rename = func(context.Context, string, string, string, string) error { return errors.New("race") }
		}, want: "local CAS rename"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ops := branchLifecycleQuarantineOps(sha)
			tc.change(&ops)
			attempt := planned
			applyBranchQuarantineWithOps(context.Background(), "/projects", "/repo", &attempt, ops)
			if attempt.Outcome != "failed" || !strings.Contains(attempt.Error, tc.want) {
				t.Fatalf("apply result = %#v, want %q", attempt, tc.want)
			}
		})
	}

	claimedOps := branchLifecycleQuarantineOps(sha)
	claimedOps.inUse = func(context.Context, string, string) (map[string]string, string) {
		return map[string]string{branchInUseKey(request.Repository, request.Ref): "task"}, ""
	}
	if claimed := planBranchQuarantineWithOps(context.Background(), "/projects", "/repo", request, now, claimedOps); !strings.Contains(claimed.Error, "is claimed") {
		t.Fatalf("plan claim refusal = %q", claimed.Error)
	}
	if source, refusal := inspectBranchQuarantineCandidate(context.Background(), "/projects", "/repo", request, planned.Destination, true, branchLifecycleQuarantineOps(sha)); source != sha || refusal != "" {
		t.Fatalf("direct candidate inspection = %q, %q", source, refusal)
	}
	realOps := realBranchQuarantineOps()
	if realOps.git == nil || realOps.checkedOut == nil || realOps.inUse == nil || realOps.pullRequests == nil || realOps.openBasePull == nil || realOps.rename == nil {
		t.Fatal("real quarantine operations are incomplete")
	}
}

func TestBranchLifecycleRefactorBatchOptionAndRequestBoundaries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if _, err := normalizeBranchListOptions(BranchListOptions{ProjectsRoot: root, Scope: "sideways"}); err == nil {
		t.Fatal("invalid branch-list scope was accepted")
	}
	for _, options := range []BranchCleanupOptions{
		{ProjectsRoot: root, SupersededBy: "receipt.json"},
		{ProjectsRoot: root, Scope: BranchScopeRemote, Repository: "acme/app", Branch: "feature", PeerEvidence: []string{"evidence"}},
		{ProjectsRoot: root, Scope: BranchScopeLocal, Repository: "acme/app", Branch: "feature", PeerEvidence: []string{"evidence"}, RequireHosts: []string{"host"}},
	} {
		if _, err := normalizeBranchCleanupOptions(options); err == nil {
			t.Fatalf("invalid branch cleanup options were accepted: %#v", options)
		}
	}
	if _, err := BranchCleanup(context.Background(), BranchCleanupOptions{ProjectsRoot: root, Scope: "sideways"}); err == nil {
		t.Fatal("branch cleanup accepted an invalid scope")
	}
	if _, err := BranchQuarantine(context.Background(), BranchQuarantineOptions{ProjectsRoot: root}); err == nil {
		t.Fatal("branch quarantine accepted an incomplete request")
	}
	if _, err := quarantineRequests(BranchQuarantineOptions{Repository: "acme/app"}); err == nil {
		t.Fatal("incomplete direct quarantine request was accepted")
	}
	valid, err := validateQuarantineRequests([]BranchQuarantineRequest{{Repository: "acme/app", Ref: "feature", Reason: "retired"}})
	if err != nil || len(valid) != 1 {
		t.Fatalf("valid quarantine request = %#v, %v", valid, err)
	}
	if _, err := validateQuarantineRequests([]BranchQuarantineRequest{
		{Repository: "acme/app", Ref: "feature", Reason: "one"},
		{Repository: "acme/app", Ref: "feature", Reason: "two"},
	}); err == nil {
		t.Fatal("duplicate quarantine request was accepted")
	}
	manifest := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{"entries":[{"repository":"invalid","ref":"feature","reason":"retired"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := quarantineRequests(BranchQuarantineOptions{Manifest: manifest}); err == nil {
		t.Fatal("invalid manifest request was accepted")
	}
}

func TestBranchLifecycleRefactorBatchClassificationBoundaries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	target := strings.Repeat("b", 40)
	repository := discover.Repo{Org: "acme", Name: "app", Path: "/fixture/repository"}
	entry := classifyBranch(ctx, repository, branchSweepOptions{Base: "main"}, branchRef{Name: "main", SHA: sha}, BranchScopeLocal, target, "trunk", nil, nil, nil)
	if entry.Disposition != BranchProtected {
		t.Fatalf("protected branch = %#v", entry)
	}
	cache := map[string][]githubPullRequest{sha: {}}
	if receipt, note := classifyLandingReceipt(ctx, repository, branchRef{Name: "feature", SHA: sha}, "main", target, cache); receipt != nil || !strings.Contains(note, "no merged") {
		t.Fatalf("missing landing receipt = %#v, %q", receipt, note)
	}

	branchTree := strings.Repeat("c", 40)
	absorbedContext := lifecycleGitContext(t, repository.Path,
		lifecycleGitReply{operation: "cherry", output: "+ " + sha + "\n"},
		lifecycleGitReply{operation: "rev-parse", output: branchTree + "\n"},
		lifecycleGitReply{operation: "rev-parse", output: branchTree + "\n"},
	)
	absorbed, _, unique, err := classifyAbsorbedOrUnique(absorbedContext, repository.Path, target, sha)
	if err != nil || !absorbed || unique != 0 {
		t.Fatalf("tree-equal classification = %t, %d, %v", absorbed, unique, err)
	}

	inspectContext := lifecycleGitContext(t, repository.Path,
		lifecycleGitReply{operation: "fetch", err: errors.New("offline")},
		lifecycleGitReply{operation: "update-ref"},
	)
	entries, diagnostic := inspectRepositoryBranches(inspectContext, repository, branchSweepOptions{Base: "main", Scope: BranchScopeAll}, nil)
	if len(entries) != 1 || entries[0].Disposition != BranchUnreadable || diagnostic == "" {
		t.Fatalf("unreadable branch inventory = %#v, %q", entries, diagnostic)
	}
	tagsContext := lifecycleGitContext(t, repository.Path, lifecycleGitReply{operation: "ls-remote", err: errors.New("offline")})
	if tags, diagnostic := listRetiredTags(tagsContext, repository.Path, true, false); tags != nil || diagnostic == "" {
		t.Fatalf("unreadable retired tags = %#v, %q", tags, diagnostic)
	}

	fileRoot := filepath.Join(t.TempDir(), "projects-file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, _, diagnostics := countRetiredBranches(ctx, branchSweepOptions{ProjectsRoot: fileRoot, Scope: BranchScopeAll})
	if len(diagnostics) == 0 {
		t.Fatal("retired count hid repository discovery failure")
	}
}

func TestBranchLifecycleRefactorBatchCleanupAndGCFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	result := BranchCleanupResult{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "feature", Base: "main", Scope: BranchScopeRemote}, Eligible: true, Outcome: "planned"}
	results := []BranchCleanupResult{result}
	applyBranchCleanup(ctx, results, map[string]string{}, BranchCleanupOptions{}, time.Now())
	if results[0].Outcome != "failed" || !strings.Contains(results[0].Error, "path was not retained") {
		t.Fatalf("missing cleanup path = %#v", results[0])
	}
	archiveContext := lifecycleGitContext(t, "/fixture/repository", lifecycleGitReply{operation: "worktree", err: errors.New("unreadable")})
	if _, err := archiveReviewedBranch(archiveContext, t.TempDir(), "/fixture/repository", result); err == nil {
		t.Fatal("reviewed branch archive accepted unreadable repository roots")
	}
	remoteContext := lifecycleGitContext(t, "/fixture/repository", lifecycleGitReply{operation: "fetch", err: errors.New("offline")})
	applyRemoteBranchDeletion(remoteContext, "/fixture/repository", &result, BranchCleanupOptions{})
	if result.Outcome != "failed" || !strings.Contains(result.Error, "refetch --prune") {
		t.Fatalf("remote deletion fetch failure = %#v", result)
	}

	now := time.Date(2026, time.September, 28, 0, 0, 0, 0, time.UTC)
	base := ListResult{Task: "task", Repository: "acme/app", WorktreeDir: filepath.Join(t.TempDir(), "missing"), Clean: true, HeadSHA: strings.Repeat("a", 40)}
	options := GCOptions{SessionFreshness: DisableSessionFreshness}
	cases := []struct {
		name   string
		change func(*ListResult)
		class  string
	}{
		{name: "locked", change: func(result *ListResult) { result.Locked = true }, class: GCClassClaimedLive},
		{name: "supersession rejected", change: func(result *ListResult) { result.SupersessionRejection = "mismatch" }, class: GCClassUnmerged},
		{name: "superseded", change: func(result *ListResult) {
			result.SupersededAtOrigin, result.SupersessionReceipt, result.SupersessionReviewer = true, "receipt.json", "reviewer"
		}, class: GCClassLandedClean},
		{name: "truncated", change: func(result *ListResult) { result.Landing = &LandingEvidence{Truncated: true} }, class: GCClassUnmerged},
		{name: "unpushed", change: func(result *ListResult) { result.HeadUnknownToRemote = true }, class: GCClassUnpushed},
		{name: "unmerged", change: func(*ListResult) {}, class: GCClassUnmerged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			candidate := base
			tc.change(&candidate)
			entry := classifyForGC(candidate, options, now)
			if entry.Class != tc.class {
				t.Fatalf("GC class = %#v, want %q", entry, tc.class)
			}
		})
	}
	if _, err := GC(ctx, GCOptions{ProjectsRoot: t.TempDir(), SupersededBy: "receipt.json", Tasks: []string{"one", "two"}}); err == nil {
		t.Fatal("GC accepted one supersession receipt for multiple tasks")
	}
	outcome := GCOutcome{}
	if err := applyGC(ctx, GCOptions{ProjectsRoot: t.TempDir(), Now: func() time.Time { return now }}, &outcome); err != nil {
		t.Fatalf("empty GC apply = %v", err)
	}
}
