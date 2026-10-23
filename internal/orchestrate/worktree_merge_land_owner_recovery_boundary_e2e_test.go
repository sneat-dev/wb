//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // original remote-only provider fixture mutates PATH/XDG process state; native repositories/locks are owned private fixtures
func TestE2ELandOwnerStrandedNativeLandingReleasesBeforeRecursion(t *testing.T) {
	for _, stage := range []string{"resume", "owned release"} {
		//nolint:paralleltest // Process-wide environment changes in TestE2ELandOwnerStrandedNativeLandingReleasesBeforeRecursion, installStrandedLandingGH; these rows share their parent environment and remain sequential.
		t.Run(stage, func(t *testing.T) {
			if stage == "owned release" && runtime.GOOS == "windows" {
				t.Skip("owned open-lock inode replacement is Unix-only")
			}
			fixture, receipt := landOwnerNativeFixture(t)
			receipt.Phase, receipt.Status = WorktreeMergePhaseLand, WorktreeMergeConflict
			receipt.PullRequest, receipt.PublishedCandidateSHA = "https://example.test/acme/app/pull/91", receipt.Candidate.SHA
			receipt.Failure = "private missing candidate after actual publication"
			runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/"+receipt.Candidate.Branch)
			runEngineGit(t, fixture.canonical, "merge", "--ff-only", receipt.Candidate.SHA)
			runEngineGit(t, fixture.canonical, "push", "origin", "main")
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			preserved := receipt.Candidate.Worktree + ".owned-preserved"
			if err := os.Rename(receipt.Candidate.Worktree, preserved); err != nil {
				t.Fatal(err)
			}
			installStrandedLandingGH(t, receipt.PullRequest, fixture.repository.CloneURL)
			t.Setenv("WB_TEST_PR_STATE", "MERGED")
			t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
			t.Setenv("WB_TEST_MERGE_COMMIT_SHA", receipt.Candidate.SHA)
			location, err := wbhome.Root(fixture.githubDir)
			if err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(location, "worktrees", receipt.Lane, ".lock")
			replacement := "owned successor must remain untouched\n"
			if stage == "owned release" {
				script := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
				body, err := os.ReadFile(script)
				if err != nil {
					t.Fatal(err)
				}
				// The final actual remote ancestry proof executes while the real lock
				// is held. Replace its owned inode, then let the native provider finish
				// the genuine graph proof. No lock acquisition/release result is faked.
				pair := "api repos/acme/app/compare/" + receipt.TargetSHA + "..." + receipt.Candidate.SHA
				injection := "case \"$*\" in\n " + strconv.Quote(pair) + "|" + strconv.Quote(pair+" --include") + ") if [ ! -f " + strconv.Quote(lockPath+".owned-preserved") + " ]; then mv " + strconv.Quote(lockPath) + " " + strconv.Quote(lockPath+".owned-preserved") + "; printf '%s\\n' 'owned successor must remain untouched' > " + strconv.Quote(lockPath) + "; fi ;;\nesac\n"
				updated := strings.Replace(string(body), "set -eu\n", "set -eu\n"+injection, 1)
				if updated == string(body) {
					t.Fatal("native provider insertion missing")
				}
				if err := testenv.WriteExecutableFile(script, []byte(updated), 0755); err != nil {
					t.Fatal(err)
				}
			}
			reads := 0
			got, runErr := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second, Progress: func(e progress.Event) {
				if e.Phase == "read_receipt" && e.State == progress.Started {
					reads++
				}
			}})
			if stage == "resume" {
				if runErr != nil || got.Status != WorktreeMergeLanded || got.LandingSHA != receipt.Candidate.SHA || got.CanonicalSync == "" || reads != 2 {
					t.Fatalf("real stranded recovery did not release and recurse: %+v %v reads=%d", got, runErr, reads)
				}
			} else {
				if runErr == nil || !strings.Contains(runErr.Error(), "operation lock changed before retirement") || got.Status != WorktreeMergeLanded || got.LandingSHA != receipt.Candidate.SHA || reads != 1 {
					t.Fatalf("actual early owned-release refusal=%+v %v reads=%d", got, runErr, reads)
				}
				if b, err := os.ReadFile(lockPath); err != nil || string(b) != replacement {
					t.Fatalf("successor lock changed: %q %v", b, err)
				}
			}
			stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != WorktreeMergeLanded || stored.LandingSHA != receipt.Candidate.SHA || stored.PullRequest != receipt.PullRequest {
				t.Fatalf("native remote proof was not durably retained: %+v", stored)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != receipt.Candidate.SHA {
				t.Fatalf("actual landed target changed: %s", remote)
			}
			if _, err := os.Stat(preserved); err != nil {
				t.Fatalf("private original candidate identity lost: %v", err)
			}
			if len(got.CleanedTasks) != 0 {
				t.Fatalf("recovery unexpectedly cleaned assets: %+v", got)
			}
		})
	}
}

//nolint:paralleltest // original hosted-provider fixture uses PATH/XDG environment; all actual update-branch graph and persistence assets are private
func TestE2ELandOwnerAdoptsActualServerUpdatedCandidateBeforeCheckpoint(t *testing.T) {
	for _, stage := range []string{"save refusal", "handoff"} {
		//nolint:paralleltest // Process-wide environment changes in installWorktreeMergeEngineGH; these rows share their parent environment and remain sequential.
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			original := receipt.Candidate.SHA
			runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", original+":refs/heads/"+receipt.Candidate.Branch)
			gh := installWorktreeMergeEngineGH(t, fixture, original, receipt.Candidate.Branch)
			receipt.Status, receipt.PullRequest, receipt.PublishedCandidateSHA = WorktreeMergePublished, gh.pr, original
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			writeEngineFile(t, filepath.Join(fixture.canonical, "owned-server-target.txt"), "native independent target advance\n")
			runEngineGit(t, fixture.canonical, "add", "owned-server-target.txt")
			runEngineGit(t, fixture.canonical, "commit", "-m", "test: target advanced before server update-branch")
			runEngineGit(t, fixture.canonical, "push", "origin", "main")
			target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			clone := filepath.Join(t.TempDir(), "owned-server-clone")
			runEngineGit(t, fixture.githubDir, "clone", fixture.repository.CloneURL, clone)
			runEngineGit(t, clone, "config", "user.name", "WB Test")
			runEngineGit(t, clone, "config", "user.email", "wb@example.test")
			runEngineGit(t, clone, "checkout", receipt.Candidate.Branch)
			runEngineGit(t, clone, "merge", "--no-ff", "-m", "merge main", "origin/main")
			updated := strings.TrimSpace(runEngineGit(t, clone, "rev-parse", "HEAD"))
			runEngineGit(t, clone, "push", "origin", receipt.Candidate.Branch)
			gh.writeState(t, "head", updated)
			sentinel := errors.New("selected native server adoption checkpoint refusal")
			refused, completed := false, false
			got, runErr := landWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second, Progress: func(e progress.Event) {
				if e.Phase == "recover_candidate" && e.State == progress.Completed {
					completed = true
				}
			}}, func(r WorktreeMergeReceipt) error {
				if stage == "save refusal" && !refused && len(r.TargetRefreshes) > 0 && r.Candidate.SHA == updated {
					refused = true
					return sentinel
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					return err
				}
				var err error
				before, err = os.ReadFile(r.ReceiptPath)
				return err
			})
			if got.Candidate.SHA != updated || got.PublishedCandidateSHA != updated || got.TargetSHA != target || len(got.TargetRefreshes) != 1 || got.TargetRefreshes[0].PreviousCandidateSHA != original || got.TargetRefreshes[0].NewCandidateSHA != updated {
				t.Fatalf("actual native server proof not adopted: %+v %v", got, runErr)
			}
			if stage == "save refusal" {
				if !refused || !errors.Is(runErr, sentinel) || completed {
					t.Fatalf("selected save error precedence=%v refused=%v completed=%v", runErr, refused, completed)
				}
				if b, err := os.ReadFile(receipt.ReceiptPath); err != nil || string(b) != string(before) {
					t.Fatalf("failed save replaced prior durable identity: %v", err)
				}
				stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
				if err != nil || stored.Candidate.SHA != original || stored.TargetSHA != receipt.TargetSHA || len(stored.TargetRefreshes) != 0 {
					t.Fatalf("selected adoption save lost prior durable candidate/target identity: %+v %v", stored, err)
				}
			} else {
				if runErr != nil || !completed || got.Status != WorktreeMergePublished {
					t.Fatalf("actual server adoption handoff=%+v %v completed=%v", got, runErr, completed)
				}
				stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
				if err != nil || stored.Candidate.SHA != updated || len(stored.TargetRefreshes) != 1 {
					t.Fatalf("actual adopted record=%+v %v", stored, err)
				}
			}
			if local := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); local != updated {
				t.Fatalf("real local fast-forward not applied: %s", local)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != updated {
				t.Fatalf("actual updated remote candidate changed: %s", remote)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != target {
				t.Fatalf("handoff changed target: %s", remote)
			}
			if got.LandingSHA != "" || len(got.CleanedTasks) != 0 {
				t.Fatalf("adoption performed later landing: %+v", got)
			}
		})
	}
}
