package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestEnginePRCheckWindowPreservesBoundsAndDiagnostics(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name              string
		timeout, interval time.Duration
		label, want       string
	}{
		{"default window", 0, 8 * time.Minute, "merge", "CI poll interval 8m0s must be shorter than bounded merge slice 8m0s"},
		{"larger timeout remains bounded", 9 * time.Minute, 8 * time.Minute, "PR-check", "CI poll interval 8m0s must be shorter than bounded PR-check slice 8m0s"},
		{"positive timeout caps window", time.Second, time.Second, "merge", "CI poll interval 1s must be shorter than bounded merge slice 1s"},
		{"default interval remains bounded", time.Second, 0, "PR-check", "CI poll interval 30s must be shorter than bounded PR-check slice 1s"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Get: func(context.Context, githubobserver.GetRequest) (githubobserver.Response, error) {
				calls++
				return githubobserver.Response{}, fmt.Errorf("unexpected provider read")
			}})
			receipt, err := waitEnginePRCheckReceipt(ctx, Options{Timeout: row.timeout, CheckPollInterval: row.interval}, githubchecks.PullRequestWaitOptions{}, row.label)
			if err == nil || err.Error() != row.want || receipt.Status != "" || calls != 0 {
				t.Fatalf("receipt=%+v err=%v calls=%d", receipt, err, calls)
			}
		})
	}
	if got := githubChecksPollInterval(Options{}); got != githubchecks.DefaultCheckPollInterval {
		t.Fatalf("default interval=%v", got)
	}
	if got := githubChecksPollInterval(Options{CheckPollInterval: time.Millisecond}); got != time.Millisecond {
		t.Fatalf("explicit interval=%v", got)
	}
}

func TestEnginePRCheckCallersPreserveObservationOutcomes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"merge", "validation"} {
		for _, outcome := range []string{"invalid identity", "pending observation", "failed observation"} {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				canonical := filepath.Join(root, "canonical")
				worktree := filepath.Join(root, "worktree")
				for _, dir := range []string{canonical, worktree} {
					if err := os.Mkdir(dir, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "sentinel"), []byte("unchanged"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				memo := NewFetchMemo()
				memo.MarkFetched(canonical)
				result := Result[string]{Repository: "acme/app", PR: "7", Ref: "main", Commit: "head", CanonicalDir: canonical, WorktreeDir: worktree, Status: "pr_open"}
				calls := 0
				ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
					calls++
					if request.Endpoint != "repos/acme/app/pulls/7" || request.Repository != "acme/app" {
						t.Fatalf("unexpected request %+v", request)
					}
					if outcome == "pending observation" {
						return githubobserver.Response{}, githubobserver.ErrTransientRetriesExhausted
					}
					return githubobserver.Response{Body: []byte(`{"number":7,"head":{"sha":"different"},"base":{"ref":"main"}}`)}, nil
				}})
				if outcome == "invalid identity" {
					result.Commit = ""
				}
				options := Options{Timeout: time.Minute, CheckPollInterval: time.Millisecond, FetchMemo: memo, run: runnertest.New(t)}
				var err error
				if mode == "merge" {
					err = waitAndMerge(ctx, options, &result)
				} else {
					err = waitForPRChecks(ctx, options, &result)
				}
				switch outcome {
				case "invalid identity":
					if err == nil || err.Error() != "repository, target, and exact head are required" || calls != 0 {
						t.Fatalf("error=%v calls=%d", err, calls)
					}
				case "pending observation":
					if calls != 1 {
						t.Fatalf("completed observations=%d", calls)
					}
					if mode == "merge" {
						if err == nil || !strings.Contains(err.Error(), "GitHub CI receipt is pending for 7 at head") {
							t.Fatalf("merge pending error=%v", err)
						}
					} else if err != nil || result.Status != "awaiting_merge" || !strings.Contains(result.Reason, "exact PR-head GitHub checks remain pending") {
						t.Fatalf("validation pending result=%+v err=%v", result, err)
					}
				case "failed observation":
					if calls != 1 || err == nil || !strings.Contains(err.Error(), "GitHub CI receipt failed for 7 at head: pull request head drifted from head to different") {
						t.Fatalf("failure result=%+v err=%v calls=%d", result, err, calls)
					}
				}
				if result.Merged || result.Checks != nil || !memo.SkipFetch(canonical) {
					t.Fatalf("unapproved observation mutated result/memo: %+v", result)
				}
				if (mode != "validation" || outcome != "pending observation") && result.Status != "pr_open" {
					t.Fatalf("status changed on refusal: %+v", result)
				}
				for _, dir := range []string{canonical, worktree} {
					data, readErr := os.ReadFile(filepath.Join(dir, "sentinel"))
					if readErr != nil || string(data) != "unchanged" {
						t.Fatalf("sentinel %s=%q err=%v", dir, data, readErr)
					}
				}
			})
		}
	}
}
