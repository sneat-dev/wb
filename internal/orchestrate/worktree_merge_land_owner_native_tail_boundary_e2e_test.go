//go:build e2e

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/progress"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // actual private native Git with original scripted provider process-wide PATH/XDG state
func TestE2ELandOwnerFreshNativeTailErrorsRetainExactRemoteAndDurableEvidence(t *testing.T) {
	for _, stage := range []string{"canonical root alias loop", "reconciliation Git refusal", "PR text Git refusal", "existing PR verification refusal", "existing PR verification success"} {
		//nolint:paralleltest // Process-wide environment changes in TestE2ELandOwnerFreshNativeTailErrorsRetainExactRemoteAndDurableEvidence, installWorktreeMergeDirectGH, installWorktreeMergePublishOnlyPRGH; these rows share their parent environment and remain sequential.
		t.Run(stage, func(t *testing.T) {
			if stage == "canonical root alias loop" && runtime.GOOS == "windows" {
				t.Skip("owned symlink replacement requires Unix symlink permissions")
			}
			fixture, receipt := landOwnerNativeFixture(t)
			route := WorktreeMergeRouteDirect
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
			t.Setenv("WB_TEST_GH_LOG", filepath.Join(t.TempDir(), "provider.log"))
			if strings.HasPrefix(stage, "existing PR") || stage == "PR text Git refusal" {
				route = WorktreeMergeRoutePullRequest
				installWorktreeMergePublishOnlyPRGH(t)
				t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
				if strings.HasPrefix(stage, "existing PR") {
					match := []map[string]any{{"html_url": "https://example.test/acme/app/pull/41", "state": "open", "head": map[string]any{"sha": receipt.Candidate.SHA, "ref": receipt.Candidate.Branch, "repo": map[string]any{"full_name": receipt.Repository}}, "base": map[string]any{"ref": receipt.Target}}}
					data, err := json.Marshal(match)
					if err != nil {
						t.Fatal(err)
					}
					t.Setenv("WB_TEST_EXISTING_PR_JSON", string(data))
				}
			}
			requestedRoot := fixture.githubDir
			alias := ""
			if stage == "canonical root alias loop" {
				alias = filepath.Join(t.TempDir(), "owned-root-alias")
				if err := os.Symlink(fixture.githubDir, alias); err != nil {
					t.Fatal(err)
				}
				requestedRoot = alias
			}
			armed := false
			marker := ""
			movedGit := ""
			options := WorktreeMergeLandOptions{ProjectsRoot: requestedRoot, Receipt: receipt.ReceiptPath, Route: route, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second, StopBeforeMerge: stage == "existing PR verification success", Progress: func(e progress.Event) {
				if stage == "canonical root alias loop" && e.Phase == "target_checks" && e.State == progress.Waiting && !armed {
					if err := os.Remove(alias); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(alias, alias); err != nil {
						t.Fatal(err)
					}
					armed = true
				}
				if stage == "reconciliation Git refusal" && e.Phase == "reconcile_source_prs" && e.State == progress.Started && !armed {
					movedGit = filepath.Join(fixture.canonical, ".git.owned-preserved")
					if err := os.Rename(filepath.Join(fixture.canonical, ".git"), movedGit); err != nil {
						t.Fatal(err)
					}
					armed = true
				}
				if e.Phase == "publish_candidate" && e.State == progress.Completed && !armed {
					if stage == "PR text Git refusal" {
						movedGit = filepath.Join(receipt.Candidate.Worktree, ".git.owned-preserved")
						if err := os.Rename(filepath.Join(receipt.Candidate.Worktree, ".git"), movedGit); err != nil {
							t.Fatal(err)
						}
						armed = true
					}
					if stage == "existing PR verification refusal" {
						marker = landOwnerPrivateProviderRefusal(t, "'pr view '*\" --json state,headRefOid,baseRefName\"")
						armed = true
					}
				}
			}}
			got, runErr := LandWorktreeMerge(context.Background(), options)
			if stage == "existing PR verification success" {
				if runErr != nil || got.Status != WorktreeMergePublished || got.PullRequest != "https://example.test/acme/app/pull/41" {
					t.Fatalf("actual existing PR handoff=%+v %v", got, runErr)
				}
			} else if !armed || runErr == nil {
				t.Fatalf("selected native %s refusal=%+v %v armed=%v", stage, got, runErr, armed)
			}
			if stage == "canonical root alias loop" {
				if got.Status != WorktreeMergeCanonicalSyncBlocked || got.LandingSHA != receipt.Candidate.SHA {
					t.Fatalf("actual resolver refusal lost landed identity: %+v %v", got, runErr)
				}
			}
			if stage == "reconciliation Git refusal" {
				if got.Status != WorktreeMergeLanded || !strings.Contains(runErr.Error(), "discover absorbed source heads") {
					t.Fatalf("actual native reconciliation refusal=%+v %v", got, runErr)
				}
			}
			if stage == "existing PR verification refusal" {
				if _, err := os.Stat(marker); err != nil || !strings.Contains(runErr.Error(), "verify adopted pull request") {
					t.Fatalf("exact adopted PR refusal=%v marker=%v", runErr, err)
				}
			}
			if movedGit != "" {
				if _, err := os.Stat(movedGit); err != nil {
					t.Fatalf("owned native Git identity was not preserved: %v", err)
				}
			}
			stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != got.Status || stored.Candidate.SHA != receipt.Candidate.SHA || runErr != nil && stored.Failure != runErr.Error() {
				t.Fatalf("actual durable failure evidence=%+v returned=%+v error=%v", stored, got, runErr)
			}
			remoteRef := "refs/heads/main"
			want := receipt.Candidate.SHA
			if route == WorktreeMergeRoutePullRequest {
				remoteRef = "refs/heads/" + receipt.Candidate.Branch
				if target := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); target != receipt.TargetSHA {
					t.Fatalf("PR refusal changed target: %s", target)
				}
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", remoteRef)); remote != want {
				t.Fatalf("actual native published identity=%s want %s", remote, want)
			}
			if len(got.CleanedTasks) != 0 {
				t.Fatalf("tail refusal performed cleanup: %+v", got)
			}
			if alias != "" {
				if _, err := filepath.EvalSymlinks(alias); err == nil || errors.Is(err, os.ErrNotExist) {
					t.Fatalf("actual owned root loop lost real resolution refusal: %v", err)
				}
			}
		})
	}
}
