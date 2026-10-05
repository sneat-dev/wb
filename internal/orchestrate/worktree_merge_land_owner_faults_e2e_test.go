//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // genuine scripted provider setup changes PATH and XDG_STATE_HOME
func TestE2ELandOwnerRecoveryWritesRefuseOnlySelectedCheckpoint(t *testing.T) {
	for _, stage := range []string{"recovered validation", "advanced identity", "advanced validation"} {
		//nolint:paralleltest // Process-wide environment changes in installWorktreeMergeDirectGH; these rows share their parent environment and remain sequential.
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			receipt.Status = WorktreeMergeConflict
			if stage == "recovered validation" {
				receipt.Candidate.SHA = ""
			} else {
				writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "resolved-descendant.txt"), "owned descendant\n")
				runEngineGit(t, receipt.Candidate.Worktree, "add", "resolved-descendant.txt")
				runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "test: advance owned conflict candidate")
			}
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			installWorktreeMergeDirectGH(t)
			sentinel := errors.New("selected recovery receipt save refusal")
			lastDurable := before
			delegatedPreparing := false
			recovering := false
			refused := false
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Timeout: 5 * time.Second,
				Progress: func(e progress.Event) {
					if e.Phase == "recover_candidate" {
						recovering = e.State == progress.Started
					}
				}}
			got, runErr := landWorktreeMerge(context.Background(), options, func(r WorktreeMergeReceipt) error {
				selected := recovering
				if stage == "advanced identity" {
					selected = selected && r.Status == WorktreeMergePreparing
				} else {
					selected = selected && r.Status == WorktreeMergePrepared
				}
				if selected {
					refused = true
					return sentinel
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					return err
				}
				lastDurable, err = os.ReadFile(r.ReceiptPath)
				delegatedPreparing = delegatedPreparing || r.Status == WorktreeMergePreparing
				return err
			})
			if !refused || !errors.Is(runErr, sentinel) || got.Candidate.SHA == "" {
				t.Fatalf("checkpoint=%s receipt=%+v error=%v refused=%v", stage, got, runErr, refused)
			}
			after, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil || string(after) != string(lastDurable) {
				t.Fatalf("selected save changed the last genuinely persisted checkpoint: %v", err)
			}
			if stage == "advanced validation" && !delegatedPreparing {
				t.Fatal("final validation refusal did not follow its genuine preparing checkpoint")
			}
			if got.LandingSHA != "" || got.PublishedCandidateSHA != "" {
				t.Fatalf("negative recovery checkpoint published or landed: %+v", got)
			}
			lock, err := AcquireOperationLock(fixture.githubDir, receipt.Lane, true)
			if err != nil {
				t.Fatalf("recovery fault retained lock: %v", err)
			}
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestE2ELandOwnerRebaseCommandRefusalPreservesTargetAndAborts(t *testing.T) {
	t.Parallel()
	fixture, receipt := landOwnerNativeFixture(t)
	writeEngineFile(t, filepath.Join(fixture.canonical, "target-only.txt"), "native target advance\n")
	runEngineGit(t, fixture.canonical, "add", "target-only.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance remote for selected rebase refusal")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	sentinel := errors.New("selected actual rebase command refusal")
	expected := []string{"rebase", "--rebase-merges", "--onto", target, receipt.TargetSHA, receipt.Candidate.Branch}
	run := &landOwnerFaultRunner{Runner: defaultRunner, failure: sentinel, refuse: func(dir, name string, args []string) bool {
		return dir == receipt.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, expected)
	}}
	got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, run: run})
	if !run.refused || !errors.Is(err, sentinel) || got.Status != WorktreeMergeConflict || !strings.Contains(err.Error(), "rebase") {
		t.Fatalf("native rebase refusal=%+v %v calls=%v", got, err, run.calls)
	}
	if run.later != 1 || !strings.HasSuffix(run.calls[len(run.calls)-1], ": git rebase --abort") {
		t.Fatalf("refusal must be followed only by actual abort: %v", run.calls)
	}
	if actual := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); actual != target {
		t.Fatalf("refused rebase moved native target: %s", actual)
	}
	if actual := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); actual != receipt.Candidate.SHA {
		t.Fatalf("refused rebase changed candidate: %s", actual)
	}
	stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if readErr != nil || stored.Failure != err.Error() {
		t.Fatalf("durable rebase refusal=%+v %v", stored, readErr)
	}
}

//nolint:paralleltest // genuine scripted provider changes PATH and XDG_STATE_HOME
func TestE2ELandOwnerPublishedRefreshRefusesExactPostRefreshCandidateRead(t *testing.T) {
	fixture, receipt := landOwnerNativeFixture(t)
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/"+receipt.Candidate.Branch)
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	receipt.PullRequest, receipt.PublishedCandidateSHA = gh.pr, receipt.Candidate.SHA
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(fixture.canonical, "published-target.txt"), "target advance\n")
	runEngineGit(t, fixture.canonical, "add", "published-target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance target before actual published refresh")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	refreshed := false
	sentinel := errors.New("selected post-refresh candidate status refusal")
	run := &landOwnerFaultRunner{Runner: defaultRunner, failure: sentinel, refuse: func(dir, name string, args []string) bool {
		return refreshed && dir == receipt.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"})
	}}
	got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, run: run,
		Progress: func(e progress.Event) {
			if e.Phase == "refresh_published_candidate" && e.State == progress.Completed {
				refreshed = true
			}
		}})
	if !refreshed || !run.refused || !errors.Is(err, sentinel) || run.later != 0 || got.Status != WorktreeMergeConflict || got.Candidate.SHA == receipt.Candidate.SHA {
		t.Fatalf("post-refresh refusal=%+v %v calls=%v", got, err, run.calls)
	}
	if actual := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); actual != target {
		t.Fatalf("candidate-read refusal moved target: %s", actual)
	}
	if actual := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)); !strings.HasPrefix(actual, receipt.PublishedCandidateSHA+"\t") {
		t.Fatalf("pre-publication refusal changed original published remote candidate: %q", actual)
	}
	if actual := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); actual != got.Candidate.SHA {
		t.Fatalf("refreshed local candidate was not retained: %s", actual)
	}
	if ancestor := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "merge-base", target, got.Candidate.SHA)); ancestor != target {
		t.Fatalf("refreshed local candidate does not contain advanced target: %s", ancestor)
	}
	stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if readErr != nil || stored.Candidate.SHA != got.Candidate.SHA || stored.PublishedCandidateSHA != receipt.PublishedCandidateSHA || stored.TargetSHA != target || stored.Failure != err.Error() {
		t.Fatalf("durable post-refresh receipt=%+v %v", stored, readErr)
	}
}

//nolint:paralleltest // genuine native landing helper changes provider PATH and XDG_STATE_HOME
func TestE2ELandOwnerMissingCleanupNativeInspectionAndCompletionSaveRefusals(t *testing.T) {
	//nolint:paralleltest // Process-wide environment changes in installWorktreeMergeDirectGH, landedTerminalCleanupFixture, newEngineFixtureOnBranch; these rows share their parent environment and remain sequential.
	t.Run("native acknowledgement stat refusal", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("self-referential Unix symlink stat fault is unsupported by the Windows fixture")
		}
		fixture, _, landed, _ := landedTerminalCleanupFixture(t)
		landed.Cleanup = true
		if err := persistWorktreeMergeReceipt(landed); err != nil {
			t.Fatal(err)
		}
		path := landed.ReceiptPath + worktreeMergeMissingCleanupAcknowledgementSuffix
		if err := os.Symlink(path, path); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(landed.ReceiptPath)
		if err != nil {
			t.Fatal(err)
		}
		got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Cleanup: true})
		var pe *os.PathError
		if !errors.As(err, &pe) || pe.Op != "stat" || pe.Path != path || !strings.Contains(err.Error(), "inspect missing-cleanup acknowledgement") || got.Status != landed.Status {
			t.Fatalf("actual stat refusal=%+v %v", got, err)
		}
		after, readErr := os.ReadFile(landed.ReceiptPath)
		if readErr != nil || string(after) != string(before) {
			t.Fatalf("stat refusal changed receipt: %v", readErr)
		}
	})
	//nolint:paralleltest // Process-wide environment changes in installWorktreeMergeDirectGH, landedTerminalCleanupFixture, newEngineFixtureOnBranch; these rows share their parent environment and remain sequential.
	t.Run("terminalized evidence completion save refusal", func(t *testing.T) {
		fixture, _, landed, claims := landedTerminalCleanupFixture(t)
		intent := WorktreeMergeLandOptions{Cleanup: true, OnFailure: "stop"}
		retainWorktreeMergeLandIntent(&landed, &intent)
		if err := persistWorktreeMergeReceipt(landed); err != nil {
			t.Fatal(err)
		}
		externallyTerminalizeMergeCleanup(t, fixture, &landed)
		if err := os.Remove(terminalWorkLogPath(claims[landed.Sources[0].Task])); err != nil {
			t.Fatal(err)
		}
		_, err := AcknowledgeMissingWorktreeMergeCleanup(context.Background(), WorktreeMergeMissingCleanupAcknowledgementOptions{ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "native missing terminal record after actual cleanup"})
		if err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(landed.ReceiptPath)
		if err != nil {
			t.Fatal(err)
		}
		sentinel := errors.New("selected missing-cleanup completion save refusal")
		refused := false
		got, err := landWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: landed.ReceiptPath, Cleanup: true}, func(r WorktreeMergeReceipt) error {
			if r.Status == WorktreeMergeComplete {
				refused = true
				return sentinel
			}
			return persistWorktreeMergeReceipt(r)
		})
		if !refused || !errors.Is(err, sentinel) || got.Status != WorktreeMergeComplete || len(got.CleanedTasks) != len(sortedUniqueMergeTasks(landed)) {
			t.Fatalf("completion save refusal=%+v %v refused=%v", got, err, refused)
		}
		after, readErr := os.ReadFile(landed.ReceiptPath)
		if readErr != nil || string(after) != string(before) {
			t.Fatalf("completion refusal changed last durable receipt: %v", readErr)
		}
		for _, path := range []string{landed.Candidate.Worktree, landed.Sources[0].Worktree} {
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("actual terminalized path remains: %s %v", path, statErr)
			}
		}
	})
}

//nolint:paralleltest // genuine publish-only provider fixture changes PATH and XDG_STATE_HOME
func TestE2ELandOwnerStopBeforeMergeRefusesFinalHandoffSave(t *testing.T) {
	fixture, receipt := landOwnerNativeFixture(t)
	installWorktreeMergePublishOnlyPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	t.Setenv("WB_TEST_GH_LOG", filepath.Join(t.TempDir(), "gh.log"))
	options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond}
	published, err := LandWorktreeMerge(context.Background(), options)
	if err != nil || published.Status != WorktreeMergePublished {
		t.Fatalf("actual original handoff prerequisite=%+v %v", published, err)
	}
	sentinel := errors.New("selected published handoff save refusal")
	refused := false
	lastPhase := ""
	routeSaves := 0
	var lastDurable []byte
	options.Progress = func(e progress.Event) { lastPhase = e.Phase }
	got, err := landWorktreeMerge(context.Background(), options, func(r WorktreeMergeReceipt) error {
		if lastPhase == "resolve_route" && r.Status == WorktreeMergePublished {
			routeSaves++
			if routeSaves == 2 {
				refused = true
				return sentinel
			}
		}
		if err := persistWorktreeMergeReceipt(r); err != nil {
			return err
		}
		var readErr error
		lastDurable, readErr = os.ReadFile(r.ReceiptPath)
		return readErr
	})
	if !refused || !errors.Is(err, sentinel) || got.LandingSHA != "" || got.PullRequest != published.PullRequest || got.PublishedCandidateSHA != receipt.Candidate.SHA {
		t.Fatalf("final handoff refusal=%+v %v refused=%v routeSaves=%d", got, err, refused, routeSaves)
	}
	if actual := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); actual != receipt.TargetSHA {
		t.Fatalf("handoff refusal advanced target: %s", actual)
	}
	if actual := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)); !strings.HasPrefix(actual, receipt.Candidate.SHA+"\t") {
		t.Fatalf("actual published candidate absent: %q", actual)
	}
	durable, readErr := os.ReadFile(receipt.ReceiptPath)
	if readErr != nil || string(durable) != string(lastDurable) {
		t.Fatalf("refused final handoff replaced preceding real durable receipt: %v", readErr)
	}
	// The source still carries genuine active native custody after a persisted handoff refusal.
	view, viewErr := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: receipt.Sources[0].Worktree})
	if viewErr != nil || view.Claim == nil || view.Claim.Lifecycle != "active" {
		t.Fatalf("handoff fault lost source custody: %+v %v", view, viewErr)
	}
}
