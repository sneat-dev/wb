package worktrees

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestFetchRemoteTargetHeadReportsEachGitFailureAndDeletesItsPrivateRef(t *testing.T) {
	t.Parallel()
	const head = "0123456789abcdef0123456789abcdef01234567"
	const repository = "/fixture/repository"
	boom := errors.New("injected git failure")
	cases := []struct {
		name       string
		failCall   int
		wantDetail string
	}{
		{"fetch", 1, "fetch exact origin/main target"},
		{"resolve fetched commit", 2, "rev-parse"},
		{"delete private ref", 3, "delete private fetch ref"},
		{"success", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			var privateRef string
			fake.Expect(func(call runnertest.Call) bool {
				if !ordinaryGitCall(call, repository, "fetch") || len(call.Args) != 9 {
					return false
				}
				refspec := call.Args[8]
				if !strings.HasPrefix(refspec, "+refs/heads/main:refs/wb/fetch-base/") {
					return false
				}
				privateRef = strings.TrimPrefix(refspec, "+refs/heads/main:")
				return true
			}, runner.Result{}, nil)
			fake.Expect(func(call runnertest.Call) bool {
				return ordinaryGitCall(call, repository, "rev-parse") &&
					len(call.Args) == 5 && call.Args[3] == "--verify" && call.Args[4] == privateRef+"^{commit}"
			}, runner.Result{CombinedOutput: head + "\n"}, nil)
			fake.Expect(func(call runnertest.Call) bool {
				return ordinaryGitCall(call, repository, "update-ref") &&
					len(call.Args) == 5 && call.Args[3] == "-d" && call.Args[4] == privateRef
			}, runner.Result{}, nil)
			if tc.failCall != 0 {
				fake.FailCall(tc.failCall, boom)
			}
			ctx := withGitRunner(context.Background(), fake)
			got, err := fetchRemoteTargetHeadUncached(ctx, repository, "main")
			if tc.failCall == 0 {
				if err != nil || got != head {
					t.Fatalf("fetch target = (%q, %v), want (%q, nil)", got, err, head)
				}
			} else {
				if got != "" || err == nil || !strings.Contains(err.Error(), "injected git failure") ||
					!strings.Contains(err.Error(), tc.wantDetail) {
					t.Fatalf("failed fetch target = (%q, %v), want empty head and %q", got, err, tc.wantDetail)
				}
			}
			calls := fake.Calls()
			wantCalls := 3
			if tc.failCall == 1 {
				wantCalls = 2 // failed fetch is followed by deferred private-ref cleanup
			}
			if len(calls) != wantCalls || !ordinaryGitCall(calls[len(calls)-1], repository, "update-ref") {
				t.Fatalf("Git calls = %#v, want %d calls ending in private-ref deletion", calls, wantCalls)
			}
			if privateRef == "" {
				t.Fatal("fetch did not select an invocation-private ref")
			}
		})
	}
}

func ordinaryGitCall(call runnertest.Call, directory, operation string) bool {
	return call.Op == "RunOpts" && call.Dir == directory && call.Name == "git" &&
		len(call.Args) >= 3 && call.Args[0] == "-C" && call.Args[1] == directory && call.Args[2] == operation &&
		call.Opts.CaptureCombined && call.Opts.WaitDelay == gitCancellationGraceDelay
}

func TestValidateExistingWorktreeStopsAtEachFailedGitQuery(t *testing.T) {
	t.Parallel()
	const worktree = "/fixture/worktree"
	const canonicalPath = "/fixture/canonical"
	responses := []struct {
		args   []string
		output string
	}{
		{[]string{"rev-parse", "--show-toplevel"}, worktree},
		{[]string{"rev-parse", "--absolute-git-dir"}, canonicalPath + "/.git/worktrees/worktree"},
		{[]string{"rev-parse", "--path-format=absolute", "--git-common-dir"}, canonicalPath + "/.git"},
		{[]string{"branch", "--show-current"}, "feature"},
	}
	for failCall := 0; failCall <= len(responses); failCall++ {
		name := "success"
		if failCall != 0 {
			name = responses[failCall-1].args[0] + " call " + string(rune('0'+failCall))
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			for _, response := range responses {
				argv := append([]string{"git", "-C", worktree}, response.args...)
				fake.ExpectArgv(argv, runner.Result{CombinedOutput: response.output + "\n"}, nil)
			}
			if failCall != 0 {
				fake.FailCall(failCall, errors.New("injected worktree query failure"))
			}
			ctx := withGitRunner(context.Background(), fake)
			err := validateExistingWorktree(ctx, &canonicalRepository{path: canonicalPath}, worktree, "feature")
			if failCall == 0 {
				if err != nil {
					t.Fatalf("validate existing worktree: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "injected worktree query failure") {
				t.Fatalf("failed Git query %d returned %v", failCall, err)
			}
			wantCalls := failCall
			if wantCalls == 0 {
				wantCalls = len(responses)
			}
			if got := fake.CallCount(); got != wantCalls {
				t.Fatalf("Git calls = %d, want %d; validation must stop on the first failure", got, wantCalls)
			}
		})
	}
}

func TestInjectedGitRunnerKeepsReadOnlyMemoAndCombinedDiagnostics(t *testing.T) {
	t.Parallel()
	const repository = "/fixture/repository"
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "-C", repository, "rev-parse", "HEAD"},
		runner.Result{CombinedOutput: "head-sha\n"}, nil)
	fake.ExpectArgv([]string{"git", "-C", repository, "fetch", "origin"},
		runner.Result{CombinedOutput: "fatal: remote rejected\n"}, errors.New("process exited"))
	ctx := withGitQueryMemo(withGitRunner(context.Background(), fake))
	for repeat := 0; repeat < 2; repeat++ {
		got, err := git(ctx, repository, "rev-parse", "HEAD")
		if err != nil || got != "head-sha" {
			t.Fatalf("memoized Git query %d = (%q, %v)", repeat, got, err)
		}
	}
	if got := fake.CallCount(); got != 1 {
		t.Fatalf("identical read-only query ran %d times, want once", got)
	}
	_, err := git(ctx, repository, "fetch", "origin")
	if err == nil || !strings.Contains(err.Error(), "fatal: remote rejected") || strings.Contains(err.Error(), "process exited") {
		t.Fatalf("Git failure = %v, want combined command output as diagnostic", err)
	}
	if got := fake.CallCount(); got != 2 {
		t.Fatalf("mutating Git call count = %d, want second external call", got)
	}
}
