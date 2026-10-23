package worktrees

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreebranches"
)

// This exercises the quarantine safety decisions without starting git or gh.
// TestContractQuarantinePlanGitSafety checks the real Git boundary in the e2e tier.
func TestPlanBranchQuarantineWithFakeOperations(t *testing.T) {
	t.Parallel()
	const sha = "0123456789abcdef0123456789abcdef01234567"
	request := BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/old", SHA: sha, Reason: "retired"}
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	baseOps := func(t *testing.T) branchQuarantinePlanOps {
		t.Helper()
		return branchQuarantinePlanOps{
			git: func(_ context.Context, path string, args ...string) (string, error) {
				if path != "/repo" {
					t.Fatalf("git path = %q", path)
				}
				switch strings.Join(args, " ") {
				case "rev-parse --verify refs/heads/feature/old^{commit}":
					return sha + "\n", nil
				case "rev-parse --abbrev-ref HEAD":
					return "main\n", nil
				case "rev-parse --verify refs/heads/retired/20260927-feature-old-0123456789ab":
					return "", errors.New("not found")
				default:
					t.Fatalf("unexpected git arguments: %q", args)
					return "", nil
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
		}
	}
	checkedOps := func(t *testing.T, ref string, ops branchQuarantinePlanOps) (branchQuarantinePlanOps, *[]string) {
		t.Helper()
		calls := []string{}
		gitCall := ops.git
		ops.git = func(ctx context.Context, path string, args ...string) (string, error) {
			if path != "/repo" {
				t.Fatalf("git path = %q", path)
			}
			command := strings.Join(args, " ")
			switch command {
			case "rev-parse --verify refs/heads/" + ref + "^{commit}":
				calls = append(calls, "source")
			case "rev-parse --abbrev-ref HEAD":
				calls = append(calls, "head")
			case "rev-parse --verify refs/heads/" + worktreebranches.RetiredBranchDestination(now, ref, sha):
				calls = append(calls, "destination")
			default:
				t.Fatalf("unexpected git query %q", command)
			}
			return gitCall(ctx, path, args...)
		}
		checkedCall := ops.checkedOut
		ops.checkedOut = func(ctx context.Context, path string) (map[string]bool, string) {
			if path != "/repo" {
				t.Fatalf("checked-out path = %q", path)
			}
			calls = append(calls, "checked")
			return checkedCall(ctx, path)
		}
		inUseCall := ops.inUse
		ops.inUse = func(ctx context.Context, root, filter string) (map[string]string, string) {
			if root != "/projects" || filter != "" {
				t.Fatalf("claim query = (%q, %q)", root, filter)
			}
			calls = append(calls, "claims")
			return inUseCall(ctx, root, filter)
		}
		pullCall := ops.pullRequests
		ops.pullRequests = func(ctx context.Context, path, repository, head string) ([]githubPullRequest, error) {
			if path != "/repo" || repository != "acme/app" || head != sha {
				t.Fatalf("head pull query = (%q, %q, %q)", path, repository, head)
			}
			calls = append(calls, "head-pulls")
			return pullCall(ctx, path, repository, head)
		}
		baseCall := ops.openBasePull
		ops.openBasePull = func(ctx context.Context, path, repository, branch string) (*PullRequest, error) {
			if path != "/repo" || repository != "acme/app" || branch != ref {
				t.Fatalf("base pull query = (%q, %q, %q)", path, repository, branch)
			}
			calls = append(calls, "base-pulls")
			return baseCall(ctx, path, repository, branch)
		}
		return ops, &calls
	}

	t.Run("safe branch has one exact destination", func(t *testing.T) {
		t.Parallel()
		ops, calls := checkedOps(t, request.Ref, baseOps(t))
		result := planBranchQuarantineWithOps(context.Background(), "/projects", "/repo", request, now, ops)
		if result.Outcome != "planned" || result.SHA != sha || result.Destination != "retired/20260927-feature-old-0123456789ab" {
			t.Fatalf("safe plan = %#v", result)
		}
		if got := strings.Join(*calls, ","); got != "source,head,checked,claims,head-pulls,base-pulls,destination" {
			t.Fatalf("safety proof order = %q", got)
		}
	})
	t.Run("stale manifest cannot quarantine a moved branch", func(t *testing.T) {
		t.Parallel()
		stale := request
		stale.SHA = strings.Repeat("f", 40)
		ops, _ := checkedOps(t, stale.Ref, baseOps(t))
		result := planBranchQuarantineWithOps(context.Background(), "/projects", "/repo", stale, now, ops)
		if result.Outcome != "refused" || !strings.Contains(result.Error, "source moved from manifest SHA") {
			t.Fatalf("stale manifest plan = %#v", result)
		}
	})
	t.Run("canonical current branch cannot be quarantined", func(t *testing.T) {
		t.Parallel()
		protected := request
		protected.Ref = "main"
		ops := baseOps(t)
		ops.git = func(_ context.Context, path string, args ...string) (string, error) {
			if path != "/repo" {
				t.Fatalf("git path = %q", path)
			}
			switch strings.Join(args, " ") {
			case "rev-parse --verify refs/heads/main^{commit}":
				return sha, nil
			case "rev-parse --abbrev-ref HEAD":
				return "main", nil
			default:
				t.Fatalf("unexpected git arguments: %q", args)
				return "", nil
			}
		}
		ops, _ = checkedOps(t, protected.Ref, ops)
		result := planBranchQuarantineWithOps(context.Background(), "/projects", "/repo", protected, now, ops)
		if result.Outcome != "refused" || !strings.Contains(result.Error, "protected") {
			t.Fatalf("protected branch plan = %#v", result)
		}
	})

	for _, tc := range []struct {
		name   string
		change func(*branchQuarantinePlanOps)
		want   string
	}{
		{"missing source", func(ops *branchQuarantinePlanOps) {
			ops.git = func(context.Context, string, ...string) (string, error) { return "", errors.New("missing") }
		}, "source ref unavailable"},
		{"checked out", func(ops *branchQuarantinePlanOps) {
			ops.checkedOut = func(context.Context, string) (map[string]bool, string) {
				return map[string]bool{"feature/old": true}, ""
			}
		}, "checked out in a linked worktree"},
		{"checkout proof failed", func(ops *branchQuarantinePlanOps) {
			ops.checkedOut = func(context.Context, string) (map[string]bool, string) { return nil, "worktree list failed" }
		}, "worktree list failed"},
		{"claim proof failed", func(ops *branchQuarantinePlanOps) {
			ops.inUse = func(context.Context, string, string) (map[string]string, string) { return nil, "claim index failed" }
		}, "claim index failed"},
		{"claimed branch", func(ops *branchQuarantinePlanOps) {
			ops.inUse = func(context.Context, string, string) (map[string]string, string) {
				return map[string]string{branchInUseKey("acme/app", "feature/old"): "task"}, ""
			}
		}, "claimed by a live WB work log"},
		{"pull request query failed", func(ops *branchQuarantinePlanOps) {
			ops.pullRequests = func(context.Context, string, string, string) ([]githubPullRequest, error) {
				return nil, errors.New("offline")
			}
		}, "cannot prove pull-request safety"},
		{"open head pull request", func(ops *branchQuarantinePlanOps) {
			ops.pullRequests = func(context.Context, string, string, string) ([]githubPullRequest, error) {
				return []githubPullRequest{{Number: 12, URL: "https://example.test/12", State: "open",
					Head: githubRef{Ref: "feature/old", SHA: sha, Repo: &githubRepository{FullName: "acme/app"}}}}, nil
			}
		}, "head of open pull request"},
		{"base pull request query failed", func(ops *branchQuarantinePlanOps) {
			ops.openBasePull = func(context.Context, string, string, string) (*PullRequest, error) {
				return nil, errors.New("offline")
			}
		}, "cannot prove pull-request base safety"},
		{"open base pull request", func(ops *branchQuarantinePlanOps) {
			ops.openBasePull = func(context.Context, string, string, string) (*PullRequest, error) {
				return &PullRequest{URL: "https://example.test/17"}, nil
			}
		}, "base of open pull request"},
		{"destination already exists", func(ops *branchQuarantinePlanOps) {
			original := ops.git
			ops.git = func(ctx context.Context, path string, args ...string) (string, error) {
				if strings.Contains(strings.Join(args, " "), "refs/heads/retired/") {
					return sha, nil
				}
				return original(ctx, path, args...)
			}
		}, "destination already exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ops := baseOps(t)
			tc.change(&ops)
			ops, _ = checkedOps(t, request.Ref, ops)
			result := planBranchQuarantineWithOps(context.Background(), "/projects", "/repo", request, now, ops)
			if result.Outcome != "refused" || !strings.Contains(result.Error, tc.want) {
				t.Fatalf("plan = %#v, want refusal containing %q", result, tc.want)
			}
		})
	}
}
