package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
)

//nolint:paralleltest // original scripted hosted-policy provider changes process-wide PATH/XDG state; native repositories remain private
func TestLandOwnerLateRouteRefusalsPreserveNativeCandidateAndTarget(t *testing.T) {
	for _, stage := range []string{"rebased direct policy", "stop-before direct CI mismatch", "merge queue"} {
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			installWorktreeMergePublishOnlyPRGH(t)
			t.Setenv("WB_TEST_GH_LOG", filepath.Join(t.TempDir(), "provider.log"))
			t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
			t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second}
			wantTarget := receipt.TargetSHA
			phase := ""
			options.Progress = func(e progress.Event) { phase = e.Phase }
			switch stage {
			case "rebased direct policy":
				writeEngineFile(t, filepath.Join(fixture.canonical, "owned-target-drift.txt"), "real disjoint target drift\n")
				runEngineGit(t, fixture.canonical, "add", "owned-target-drift.txt")
				runEngineGit(t, fixture.canonical, "commit", "-m", "test: native drift before late route resolution")
				runEngineGit(t, fixture.canonical, "push", "origin", "HEAD:refs/heads/main")
				wantTarget = strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
				options.Route = WorktreeMergeRouteDirect
			case "stop-before direct CI mismatch":
				options.StopBeforeMerge = true
				options.DirectCIPullRequest = "https://example.test/acme/app/pull/41"
			case "merge queue":
				options.Route = WorktreeMergeRouteAuto
				script := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
				body, err := os.ReadFile(script)
				if err != nil {
					t.Fatal(err)
				}
				// Controlled authoritative policy observation only; all native custody,
				// graph, prepared candidate and later write decisions remain real.
				replacement := "case \"$*\" in\n 'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100') printf '%s\\n' '[{\"type\":\"merge_queue\"}]'; exit 0 ;;\nesac\n"
				updated := strings.Replace(string(body), "set -eu\n", "set -eu\n"+replacement, 1)
				if updated == string(body) {
					t.Fatal("private provider insertion point missing")
				}
				if err := os.WriteFile(script, []byte(updated), 0755); err != nil {
					t.Fatal(err)
				}
			}
			got, runErr := LandWorktreeMerge(context.Background(), options)
			if runErr == nil || got.Status != WorktreeMergeConflict {
				t.Fatalf("late %s refusal=%+v %v", stage, got, runErr)
			}
			switch stage {
			case "rebased direct policy":
				if got.Rebase == nil || got.Rebase.TargetAfter != wantTarget || got.Rebase.CandidateAfter != got.Candidate.SHA || got.Candidate.SHA == receipt.Candidate.SHA || phase != "validate_rebased_candidate" || !strings.Contains(runErr.Error(), "direct route is not authoritatively permitted") {
					t.Fatalf("native rebase did not precede exact policy refusal: %+v phase=%s error=%v", got, phase, runErr)
				}
				if out := strings.TrimSpace(runEngineGit(t, got.Candidate.Worktree, "merge-base", wantTarget, got.Candidate.SHA)); out != wantTarget {
					t.Fatalf("real rebased candidate does not contain target: %s", out)
				}
			case "stop-before direct CI mismatch":
				if !strings.Contains(runErr.Error(), "direct CI deferral requires --route direct") || got.Candidate.SHA != receipt.Candidate.SHA {
					t.Fatalf("actual plan projection refusal=%+v %v", got, runErr)
				}
			case "merge queue":
				if !strings.Contains(runErr.Error(), "unsupported target policy: target requires a merge queue") || got.Candidate.SHA != receipt.Candidate.SHA {
					t.Fatalf("unsupported actual policy decision=%+v %v", got, runErr)
				}
			}
			stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != WorktreeMergeConflict || stored.Failure != runErr.Error() || stored.Candidate.SHA != got.Candidate.SHA || stored.TargetSHA != got.TargetSHA {
				t.Fatalf("exact last durable native failure=%+v returned=%+v", stored, got)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != wantTarget {
				t.Fatalf("late route refusal changed native target: %s want %s", remote, wantTarget)
			}
			if got.PullRequest != "" || got.PublishedCandidateSHA != "" || got.LandingSHA != "" || len(got.CleanedTasks) != 0 {
				t.Fatalf("route refusal performed later publication/cleanup: %+v", got)
			}
		})
	}
}

//nolint:paralleltest // original hosted-provider fixture uses process-wide PATH/XDG; every repository/ref mutation is private
func TestLandOwnerNormalPublishedDescendantRefusesRealPredecessorDrift(t *testing.T) {
	fixture, receipt := landOwnerNativeFixture(t)
	original := receipt.Candidate.SHA
	gh := installWorktreeMergeEngineGH(t, fixture, original, receipt.Candidate.Branch)
	published, publishErr := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second})
	if publishErr != nil || published.Status != WorktreeMergePublished || published.Phase != WorktreeMergePhaseLand || published.PullRequest != gh.pr || published.PublishedCandidateSHA != original || published.Candidate.SHA != original || published.LandingSHA != "" {
		t.Fatalf("actual publish-only handoff did not establish ordinary resume history: %+v %v", published, publishErr)
	}
	receipt = published
	writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "owned-normal-descendant.txt"), "actual native local descendant\n")
	runEngineGit(t, receipt.Candidate.Worktree, "add", "owned-normal-descendant.txt")
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "test: native published descendant before ordinary landing")
	descendant := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	observed := false
	options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second, Progress: func(e progress.Event) {
		if e.Phase == "pre_push_gate" && e.State == progress.Started && !observed {
			runEngineGit(t, fixture.repository.CloneURL, "update-ref", "refs/heads/"+receipt.Candidate.Branch, receipt.TargetSHA, original)
			observed = true
		}
	}}
	got, runErr := LandWorktreeMerge(context.Background(), options)
	if !observed || runErr == nil || !strings.Contains(runErr.Error(), "moved from recorded predecessor") || got.Status != WorktreeMergeConflict || got.PushGate == nil || got.PushGate.PreviousRemoteSHA != receipt.TargetSHA {
		t.Fatalf("exact native predecessor refusal=%+v %v observed=%v", got, runErr, observed)
	}
	stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != WorktreeMergeConflict || stored.Failure != runErr.Error() || stored.PublishedCandidateSHA != original || stored.Candidate.SHA != descendant {
		t.Fatalf("native descendant refusal receipt=%+v", stored)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != receipt.TargetSHA {
		t.Fatalf("observed remote predecessor was replaced: %s", remote)
	}
	if target := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); target != receipt.TargetSHA {
		t.Fatalf("refusal landed target: %s", target)
	}
	if got.LandingSHA != "" || got.CanonicalSync != "" || len(got.CleanedTasks) != 0 {
		t.Fatalf("refusal performed later landing: %+v", got)
	}
}
