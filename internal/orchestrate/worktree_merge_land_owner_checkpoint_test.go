package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/worktrees"
)

//nolint:paralleltest // actual scripted GitHub observation fixture changes PATH and XDG_STATE_HOME
func TestLandOwnerResumeAndFinalizationSaveCheckpointsPreserveLastDurableBytes(t *testing.T) {
	for _, stage := range []string{"interrupted validation", "failed validation", "landing intent", "route intent", "landed record", "canonical sync", "cleanup completion"} {
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
			if stage == "interrupted validation" {
				receipt.Status = WorktreeMergePreparing
			}
			if stage == "failed validation" {
				receipt.Status = WorktreeMergeValidationFailed
			}
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			durable, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("selected " + stage + " save refusal")
			phase := ""
			validationStage := ""
			refused := false
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteDirect, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, Cleanup: stage == "cleanup completion", PrepareTimeout: 5 * time.Second,
				Progress: func(e progress.Event) {
					phase = e.Phase
					if e.Phase == "validate_candidate" || e.Phase == "revalidate_candidate" {
						validationStage = e.Phase
					}
				}}
			got, runErr := landWorktreeMerge(context.Background(), options, func(r WorktreeMergeReceipt) error {
				selected := false
				switch stage {
				case "interrupted validation":
					selected = validationStage == "validate_candidate" && r.Status == WorktreeMergePrepared
				case "failed validation":
					selected = validationStage == "revalidate_candidate" && r.Status == WorktreeMergePrepared
				case "landing intent":
					selected = phase == "read_receipt" && r.OnFailure == "stop" && len(r.ResumeArgs) > 0
				case "route intent":
					selected = phase == "resolve_route" && r.Phase == WorktreeMergePhaseLand
				case "landed record":
					selected = r.Status == WorktreeMergeLanded && r.CanonicalSync == ""
				case "canonical sync":
					selected = phase == "sync_canonical" && r.CanonicalSync != ""
				case "cleanup completion":
					selected = r.Status == WorktreeMergeComplete
				}
				if selected {
					if stage == "cleanup completion" {
						// Native cleanup persists each completed task outside this owner Save port.
						stored, readErr := readWorktreeMergeReceipt(r.ReceiptPath)
						if readErr != nil || stored.Status != WorktreeMergeLanded || len(stored.CleanedTasks) != len(sortedUniqueMergeTasks(r)) || stored.LandingSHA != r.LandingSHA {
							t.Fatalf("actual nested cleanup durable checkpoint missing: %+v %v", stored, readErr)
						}
						expectedTasks := sortedUniqueMergeTasks(r)
						actualTasks := append([]string(nil), stored.CleanedTasks...)
						sort.Strings(actualTasks)
						if !reflect.DeepEqual(actualTasks, expectedTasks) || len(stored.CleanupReports) != len(expectedTasks) {
							t.Fatalf("nested cleanup exact task/report set mismatch: %+v want %v", stored, expectedTasks)
						}
						if err := worktrees.ValidateTerminalCleanupReports(stored.CleanupReports, stored.Repository, expectedTasks); err != nil {
							t.Fatalf("native report bytes do not prove exact completed task cleanup: %v", err)
						}
						for _, path := range []string{r.Candidate.Worktree, r.Sources[0].Worktree} {
							if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
								t.Fatalf("native cleanup retained path %s: %v", path, statErr)
							}
						}
						durable, err = os.ReadFile(r.ReceiptPath)
						if err != nil {
							t.Fatal(err)
						}
					}
					refused = true
					return sentinel
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					return err
				}
				durable, err = os.ReadFile(r.ReceiptPath)
				return err
			})
			if !refused || !errors.Is(runErr, sentinel) {
				t.Fatalf("%s checkpoint not reached: receipt=%+v error=%v phase=%s", stage, got, runErr, phase)
			}
			after, readErr := os.ReadFile(receipt.ReceiptPath)
			if readErr != nil || string(after) != string(durable) {
				t.Fatalf("selected write replaced last real checkpoint: %v", readErr)
			}
			if stage == "landed record" || stage == "canonical sync" || stage == "cleanup completion" {
				remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main"))
				if remote != got.LandingSHA || got.LandingSHA == "" {
					t.Fatalf("native landing evidence missing: %s %+v", remote, got)
				}
			} else if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != receipt.TargetSHA {
				t.Fatalf("early refusal changed remote target: %s", remote)
			}
			lock, lockErr := AcquireOperationLock(fixture.githubDir, receipt.Lane, true)
			if lockErr != nil {
				t.Fatalf("selected save retained owned lock: %v", lockErr)
			}
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLandOwnerRebaseReadAndSaveFaultsKeepNativeMutationAndTargetCustody(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"prepared ancestry", "refreshed HEAD", "refreshed save", "refreshed clean"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			fixture, receipt := landOwnerNativeFixture(t)
			writeEngineFile(t, filepath.Join(fixture.canonical, "later-target.txt"), "native new target\n")
			runEngineGit(t, fixture.canonical, "add", "later-target.txt")
			runEngineGit(t, fixture.canonical, "commit", "-m", "test: target advance for rebase boundary")
			runEngineGit(t, fixture.canonical, "push", "origin", "main")
			target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			sentinel := errors.New("selected " + stage + " refusal")
			rebasing := false
			refusedSave := false
			run := &landOwnerFaultRunner{Runner: defaultRunner, failure: sentinel, refuse: func(dir, name string, args []string) bool {
				if dir != receipt.Candidate.Worktree || name != "git" {
					return false
				}
				switch stage {
				case "prepared ancestry":
					return reflect.DeepEqual(args, []string{"merge-base", receipt.TargetSHA, receipt.Candidate.SHA})
				case "refreshed HEAD":
					return rebasing && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"})
				case "refreshed clean":
					return rebasing && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"})
				}
				return false
			}}
			durable, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			got, runErr := landWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, run: run, Progress: func(e progress.Event) {
				if e.Phase == "rebase_candidate" && e.State == progress.Started {
					rebasing = true
				}
			}}, func(r WorktreeMergeReceipt) error {
				if stage == "refreshed save" && r.Rebase != nil {
					refusedSave = true
					return sentinel
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					return err
				}
				durable, err = os.ReadFile(r.ReceiptPath)
				return err
			})
			if (!run.refused && !refusedSave) || !errors.Is(runErr, sentinel) {
				t.Fatalf("%s fault not reached: %+v %v calls=%v", stage, got, runErr, run.calls)
			}
			if run.refused && run.later != 0 {
				t.Fatalf("command refusal followed by later commands: %v", run.calls)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != target {
				t.Fatalf("negative checkpoint mutated remote target: %s", remote)
			}
			if rebasing {
				head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
				if head == receipt.Candidate.SHA {
					t.Fatal("actual native rebase did not advance candidate")
				}
				if ancestor := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "merge-base", target, head)); ancestor != target {
					t.Fatalf("rebased native candidate excludes target: %s", ancestor)
				}
			}
			after, readErr := os.ReadFile(receipt.ReceiptPath)
			if readErr != nil || string(after) != string(durable) {
				t.Fatalf("last actual durable checkpoint changed after refusal: %v", readErr)
			}
		})
	}
}

//nolint:paralleltest // original direct-route provider fixture changes PATH and XDG_STATE_HOME
func TestLandOwnerRoutePlanErrorsAtActualResumeStagesDoNotPublish(t *testing.T) {
	for _, stage := range []string{"recovered head", "interrupted validation", "failed validation", "advanced validation", "route decision"} {
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			installWorktreeMergeDirectGH(t)
			switch stage {
			case "recovered head":
				receipt.Status = WorktreeMergeConflict
				receipt.Candidate.SHA = ""
			case "interrupted validation":
				receipt.Status = WorktreeMergePreparing
			case "failed validation":
				receipt.Status = WorktreeMergeValidationFailed
			case "advanced validation":
				receipt.Status = WorktreeMergeConflict
				writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "route-descendant.txt"), "owned descendant\n")
				runEngineGit(t, receipt.Candidate.Worktree, "add", "route-descendant.txt")
				runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "test: owned descendant before route refusal")
			}
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoute("unsupported-test-route"), Timeout: 5 * time.Second})
			if err == nil || got.Status != WorktreeMergeConflict || !strings.Contains(err.Error(), "route") {
				t.Fatalf("%s route refusal=%+v %v", stage, got, err)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != receipt.TargetSHA {
				t.Fatalf("route refusal changed target: %s", remote)
			}
			stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if readErr != nil || stored.Failure != err.Error() || stored.PublishedCandidateSHA != "" {
				t.Fatalf("route refusal durable evidence=%+v %v", stored, readErr)
			}
		})
	}
}

//nolint:paralleltest // genuine scripted provider setup changes PATH and XDG_STATE_HOME
func TestLandOwnerPublishedRefreshSaveAndRouteRefusalsRetainUnpublishedLocalAdvance(t *testing.T) {
	for _, stage := range []string{"refresh save", "refresh route"} {
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/"+receipt.Candidate.Branch)
			gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
			receipt.PullRequest, receipt.PublishedCandidateSHA = gh.pr, receipt.Candidate.SHA
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			writeEngineFile(t, filepath.Join(fixture.canonical, "refresh-boundary-target.txt"), "actual target advance\n")
			runEngineGit(t, fixture.canonical, "add", "refresh-boundary-target.txt")
			runEngineGit(t, fixture.canonical, "commit", "-m", "test: actual target before selected refresh checkpoint")
			runEngineGit(t, fixture.canonical, "push", "origin", "main")
			target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			sentinel := errors.New("selected published refresh save refusal")
			refreshing := false
			refused := false
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Timeout: 5 * time.Second, Progress: func(e progress.Event) {
				if e.Phase == "refresh_published_candidate" {
					refreshing = e.State == progress.Started
				}
			}}
			if stage == "refresh route" {
				options.Route = WorktreeMergeRoute("unsupported-test-route")
			}
			got, runErr := landWorktreeMerge(context.Background(), options, func(r WorktreeMergeReceipt) error {
				if stage == "refresh save" && refreshing && r.Candidate.SHA != receipt.Candidate.SHA {
					refused = true
					return sentinel
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					return err
				}
				before, err = os.ReadFile(r.ReceiptPath)
				return err
			})
			if stage == "refresh save" {
				if !refused || !errors.Is(runErr, sentinel) {
					t.Fatalf("refresh save not reached: %+v %v", got, runErr)
				}
				after, readErr := os.ReadFile(receipt.ReceiptPath)
				if readErr != nil || string(after) != string(before) {
					t.Fatalf("failed refresh checkpoint altered preceding durable receipt: %v", readErr)
				}
			} else {
				if runErr == nil || !strings.Contains(runErr.Error(), "route") || got.Status != WorktreeMergeConflict {
					t.Fatalf("post-refresh route refusal=%+v %v", got, runErr)
				}
				stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
				if readErr != nil || stored.Candidate.SHA != got.Candidate.SHA || stored.Failure != runErr.Error() {
					t.Fatalf("actual refreshed refusal was not persisted: %+v %v", stored, readErr)
				}
			}
			head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
			if head != got.Candidate.SHA || head == receipt.Candidate.SHA {
				t.Fatalf("native local refresh not retained: %s %+v", head, got)
			}
			if ancestor := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "merge-base", target, head)); ancestor != target {
				t.Fatalf("local refresh excludes native target: %s", ancestor)
			}
			if remote := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)); !strings.HasPrefix(remote, receipt.PublishedCandidateSHA+"\t") {
				t.Fatalf("pre-publication refusal altered original remote candidate: %q", remote)
			}
		})
	}
}
