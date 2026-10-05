package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
)

//nolint:paralleltest // original strict hosted-check provider changes process PATH/XDG; actual Git roots and quality records are private
func TestLandOwnerPublishedRefreshAndStaleDeferralRefusalsPreserveNativeEvidence(t *testing.T) {
	for _, stage := range []string{"native refresh conflict", "refreshed direct policy", "stale deferral save"} {
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			original := receipt.Candidate.SHA
			runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", original+":refs/heads/"+receipt.Candidate.Branch)
			gh := installWorktreeMergeEngineGH(t, fixture, original, receipt.Candidate.Branch)
			receipt.Status, receipt.PullRequest, receipt.PublishedCandidateSHA = WorktreeMergePublished, gh.pr, original
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			target := receipt.TargetSHA
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second}
			if stage == "stale deferral save" {
				published, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second})
				if err != nil || published.ValidationDeferral == nil || published.Validation.Status != quality.StatusSkipped || published.PublishedCandidateSHA != original {
					t.Fatalf("actual strict CI publication did not produce stale-deferral premise: %+v %v", published, err)
				}
				receipt = published
				options.ValidateLocally = true
			} else {
				file := "owned-refresh-target.txt"
				data := "real independent target\n"
				if stage == "native refresh conflict" {
					file = "land-owner.txt"
					data = "different actual native target addition\n"
				}
				writeEngineFile(t, filepath.Join(fixture.canonical, file), data)
				runEngineGit(t, fixture.canonical, "add", file)
				runEngineGit(t, fixture.canonical, "commit", "-m", "test: real published target drift")
				runEngineGit(t, fixture.canonical, "push", "origin", "main")
				target = strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
				if stage == "refreshed direct policy" {
					options.Route = WorktreeMergeRouteDirect
				}
			}
			phase := ""
			options.Progress = func(e progress.Event) { phase = e.Phase }
			sentinel := errors.New("selected stale-deferral local completion save refusal")
			refused := false
			durable, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			got, runErr := landWorktreeMerge(context.Background(), options, func(r WorktreeMergeReceipt) error {
				if stage == "stale deferral save" && phase == "revalidate_candidate" && r.Status == WorktreeMergePrepared && r.Validation.Status == quality.StatusPassed {
					refused = true
					return sentinel
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					return err
				}
				var err error
				durable, err = os.ReadFile(r.ReceiptPath)
				return err
			})
			if runErr == nil {
				t.Fatalf("selected %s refusal absent: %+v", stage, got)
			}
			switch stage {
			case "native refresh conflict":
				if got.Status != WorktreeMergeConflict || !strings.Contains(runErr.Error(), "conflicts in land-owner.txt") || got.Candidate.SHA != original || phase != "refresh_published_candidate" {
					t.Fatalf("actual native merge-conflict observation=%+v %v phase=%s", got, runErr, phase)
				}
				if status := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "status", "--porcelain=v1")); status != "" {
					t.Fatalf("real failed refresh was not aborted cleanly: %s", status)
				}
			case "refreshed direct policy":
				if got.Status != WorktreeMergeConflict || !strings.Contains(runErr.Error(), "direct route is not authoritatively permitted") || got.TargetSHA != target || got.Candidate.SHA == original || len(got.TargetRefreshes) != 1 || phase != "validate_refreshed_candidate" {
					t.Fatalf("actual refreshed candidate did not precede late policy refusal: %+v %v phase=%s", got, runErr, phase)
				}
				if ancestor := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "merge-base", target, got.Candidate.SHA)); ancestor != target {
					t.Fatalf("real refreshed head lacks target: %s", ancestor)
				}
			case "stale deferral save":
				if !refused || !errors.Is(runErr, sentinel) || got.Status != WorktreeMergePrepared || got.Validation.Status != quality.StatusPassed || got.Validation.Revision != original || got.ValidationDeferral != nil {
					t.Fatalf("actual local revalidation checkpoint=%+v %v refused=%v", got, runErr, refused)
				}
			}
			if b, err := os.ReadFile(receipt.ReceiptPath); err != nil || string(b) != string(durable) {
				t.Fatalf("selected refusal changed last actual durable evidence: %v", err)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != original {
				t.Fatalf("pre-publication refusal changed actual candidate publication: %s", remote)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != target {
				t.Fatalf("selected refusal changed actual target: %s", remote)
			}
			if got.LandingSHA != "" || got.CanonicalSync != "" || len(got.CleanedTasks) != 0 {
				t.Fatalf("refusal performed later native landing: %+v", got)
			}
		})
	}
}
