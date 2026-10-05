package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func landOwnerNativeFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "land-owner-source", "feature/land-owner-source", "land-owner.txt", "land owner\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return fixture, receipt
}

// landOwnerFaultRunner refuses one exact observation and delegates every
// successful command to the guarded real runner. It cannot grant custody.
type landOwnerFaultRunner struct {
	runner.Runner
	refuse  func(string, string, []string) bool
	failure error
	refused bool
	later   int
	calls   []string
}

func (r *landOwnerFaultRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	r.calls = append(r.calls, dir+": "+name+" "+strings.Join(args, " "))
	if r.refused {
		r.later++
	}
	if !r.refused && r.refuse(dir, name, args) {
		r.refused = true
		return runner.Result{}, r.failure
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

func TestLandOwnerRunnerRefusalsPreserveNativeStateAndReleaseLock(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		status WorktreeMergeStatus
		source bool
		args   []string
		want   string
	}{
		{name: "candidate clean", args: []string{"status", "--porcelain=v1"}, want: "git status"},
		{name: "candidate HEAD", args: []string{"rev-parse", "--verify", "HEAD^{commit}"}, want: "git rev-parse"},
		{name: "source clean", source: true, args: []string{"status", "--porcelain=v1"}, want: "changed during prepare"},
		{name: "source HEAD", source: true, args: []string{"rev-parse", "--verify", "HEAD^{commit}"}, want: "git rev-parse"},
		{name: "exact fetch", args: []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}, want: "fetch exact remote target main"},
		{name: "fetched revision", args: []string{"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}, want: "git rev-parse"},
		{name: "target ancestry", args: []string{"merge-base"}, want: "git merge-base"},
		{name: "interrupted clean", status: WorktreeMergePreparing, args: []string{"status", "--porcelain=v1"}, want: "interrupted candidate is not clean"},
		{name: "interrupted HEAD", status: WorktreeMergePreparing, args: []string{"rev-parse", "--verify", "HEAD^{commit}"}, want: "read interrupted candidate head"},
		{name: "failed prepare clean", status: WorktreeMergeValidationFailed, args: []string{"status", "--porcelain=v1"}, want: "validation_failed candidate is not safely resumable"},
		{name: "failed prepare HEAD", status: WorktreeMergeValidationFailed, args: []string{"rev-parse", "--verify", "HEAD^{commit}"}, want: "git rev-parse"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture, receipt := landOwnerNativeFixture(t)
			if tt.status != "" {
				receipt.Status = tt.status
				if err := persistWorktreeMergeReceipt(receipt); err != nil {
					t.Fatal(err)
				}
			}
			path := receipt.Candidate.Worktree
			if tt.source {
				path = receipt.Sources[0].Worktree
			}
			expectedArgs := tt.args
			if tt.name == "target ancestry" {
				expectedArgs = []string{"merge-base", receipt.TargetSHA, receipt.Candidate.SHA}
			}
			sentinel := errors.New("selected native observation refusal")
			run := &landOwnerFaultRunner{Runner: defaultRunner, failure: sentinel, refuse: func(dir, name string, args []string) bool {
				return dir == path && name == "git" && reflect.DeepEqual(args, expectedArgs)
			}}
			got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, run: run})
			if !run.refused || run.later != 0 || !errors.Is(err, sentinel) || !strings.Contains(err.Error(), tt.want) || got.Status != WorktreeMergeConflict || got.Candidate.SHA != receipt.Candidate.SHA {
				t.Fatalf("stage=%s receipt=%+v error=%v refused=%v later=%d calls=%v", tt.name, got, err, run.refused, run.later, run.calls)
			}
			persisted, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if readErr != nil || persisted.Status != got.Status || persisted.Failure != err.Error() {
				t.Fatalf("durable refusal=%+v %v", persisted, readErr)
			}
			if head := strings.TrimSpace(runEngineGit(t, receipt.Sources[0].Worktree, "rev-parse", "HEAD")); head != receipt.Sources[0].SHA {
				t.Fatalf("source changed: %s", head)
			}
			lock, lockErr := AcquireOperationLock(fixture.githubDir, receipt.Lane, true)
			if lockErr != nil {
				t.Fatalf("refusal retained lock: %v", lockErr)
			}
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLandOwnerNativeEntryAndChangedCandidateRefusals(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		prepare func(*testing.T, engineFixture, *WorktreeMergeReceipt)
		want    string
	}{
		{name: "missing candidate", prepare: func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) { r.Candidate.Worktree = "" }, want: "no prepared candidate"},
		{name: "empty candidate identity is not recoverable", prepare: func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) { r.Candidate.SHA = "" }, want: "no recoverable prepared candidate"},
		{name: "candidate actual dirty file", prepare: func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "untracked.txt"), "dirty\n")
		}, want: "worktree is dirty"},
		{name: "candidate actual new commit", prepare: func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "new.txt"), "new\n")
			runEngineGit(t, r.Candidate.Worktree, "add", "new.txt")
			runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "test: candidate changed")
		}, want: "candidate head drifted"},
		{name: "source actual new commit", prepare: func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(r.Sources[0].Worktree, "new.txt"), "new\n")
			runEngineGit(t, r.Sources[0].Worktree, "add", "new.txt")
			runEngineGit(t, r.Sources[0].Worktree, "commit", "-m", "test: source changed")
		}, want: "advanced from"},
		{name: "failed candidate actual new commit", prepare: func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) {
			r.Status = WorktreeMergeValidationFailed
			writeEngineFile(t, filepath.Join(r.Candidate.Worktree, "new.txt"), "new\n")
			runEngineGit(t, r.Candidate.Worktree, "add", "new.txt")
			runEngineGit(t, r.Candidate.Worktree, "commit", "-m", "test: failed candidate changed")
		}, want: "candidate head drifted"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture, receipt := landOwnerNativeFixture(t)
			tt.prepare(t, fixture, &receipt)
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath})
			if err == nil || !strings.Contains(err.Error(), tt.want) || got.LandingSHA != "" {
				t.Fatalf("refusal receipt=%+v error=%v want %q", got, err, tt.want)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main")); !strings.HasPrefix(remote, receipt.TargetSHA+"\t") {
				t.Fatalf("refusal published target: %q", remote)
			}
		})
	}
}

func TestLandOwnerRealOperationLockRefusesConcurrentEntry(t *testing.T) {
	t.Parallel()
	fixture, receipt := landOwnerNativeFixture(t)
	receipt.Lane = "" // Exercise the actual repository/target lane fallback.
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	lane := worktreeMergeLaneID(receipt.Repository, receipt.Target)
	lock, err := AcquireOperationLock(fixture.githubDir, lane, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath})
	if err == nil || !strings.Contains(err.Error(), "already active") || got.Candidate.SHA != receipt.Candidate.SHA {
		t.Fatalf("live lock refusal=%+v %v", got, err)
	}
}

func TestLandOwnerOverridesPersistBeforeInterruptedValidation(t *testing.T) {
	t.Parallel()
	for _, status := range []WorktreeMergeStatus{WorktreeMergePreparing, WorktreeMergeValidationFailed} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			fixture, receipt := landOwnerNativeFixture(t)
			receipt.Status = status
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("override checkpoint failed")
			attempts := 0
			got, err := landWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, CheckTimeout: 7 * time.Minute, ShardAttemptTimeout: 2 * time.Minute}, func(r WorktreeMergeReceipt) error {
				attempts++
				if r.ValidationTimeouts != nil && r.ValidationTimeouts.Check == 7*time.Minute {
					return sentinel
				}
				return persistWorktreeMergeReceipt(r)
			})
			if err != sentinel || attempts != 1 || got.Status != status || got.ValidationTimeouts == nil || got.ValidationTimeouts.Check != 7*time.Minute {
				t.Fatalf("override refusal=%+v %v calls=%d", got, err, attempts)
			}
			stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if readErr != nil || stored.ValidationTimeouts != nil && stored.ValidationTimeouts.Check == 7*time.Minute {
				t.Fatalf("refused write changed storage=%+v %v", stored, readErr)
			}
		})
	}
}

//nolint:paralleltest // Scripted remote policy observes real private Git but installs process-wide PATH/XDG/WB_TEST_* fixtures.
func TestLandOwnerDirectPersistenceCheckpointsKeepActualNativeOrdering(t *testing.T) {
	for _, tt := range []struct {
		name    string
		refuse  func(WorktreeMergeReceipt) bool
		cleanup bool
		phase   WorktreeMergePhase
		status  WorktreeMergeStatus
	}{
		{name: "route intent", refuse: func(r WorktreeMergeReceipt) bool { return r.Phase == WorktreeMergePhaseLand && r.PushGate == nil }},
		{name: "push gate", refuse: func(r WorktreeMergeReceipt) bool { return r.PushGate != nil && r.LandingSHA == "" }},
		{name: "landing record", refuse: func(r WorktreeMergeReceipt) bool { return r.LandingSHA != "" && r.CanonicalSync == "" }},
		{name: "canonical receipt", refuse: func(r WorktreeMergeReceipt) bool { return r.CanonicalSync != "" && r.Status != WorktreeMergeComplete }},
		{name: "completed cleanup", cleanup: true, refuse: func(r WorktreeMergeReceipt) bool { return r.Status == WorktreeMergeComplete }},
		{name: "interrupted prepared receipt", status: WorktreeMergePreparing, refuse: func(r WorktreeMergeReceipt) bool {
			return r.Status == WorktreeMergePrepared && r.Phase == WorktreeMergePhasePrepare
		}},
		{name: "failed prepare revalidated receipt", status: WorktreeMergeValidationFailed, refuse: func(r WorktreeMergeReceipt) bool {
			return r.Status == WorktreeMergePrepared && r.Phase == WorktreeMergePhasePrepare
		}},
		{name: "resumed exact revalidation limits", phase: WorktreeMergePhaseLand, status: WorktreeMergeValidationFailed, refuse: func(r WorktreeMergeReceipt) bool { return r.ValidationTimeouts != nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			if tt.phase != "" {
				receipt.Phase = tt.phase
			}
			if tt.status != "" {
				receipt.Status = tt.status
			}
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
			sentinel := errors.New("selected " + tt.name + " persistence refusal")
			failed := false
			successful := 0
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto, Cleanup: tt.cleanup, Timeout: 5 * time.Second, PrepareTimeout: time.Minute, CheckPollInterval: time.Millisecond}
			if tt.phase == WorktreeMergePhaseLand {
				options.CheckTimeout = time.Minute
				options.ShardAttemptTimeout = time.Minute
			}
			got, err := landWorktreeMerge(context.Background(), options, func(r WorktreeMergeReceipt) error {
				if !failed && tt.refuse(r) {
					failed = true
					return sentinel
				}
				successful++
				return persistWorktreeMergeReceipt(r)
			})
			if !failed || err != sentinel {
				t.Fatalf("checkpoint=%s receipt=%+v error=%v failed=%v real saves=%d", tt.name, got, err, failed, successful)
			}
			if got.Candidate.SHA != receipt.Candidate.SHA {
				t.Fatalf("storage error altered candidate: %+v", got.Candidate)
			}
			stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tt.name == "route intent" || tt.name == "push gate" {
				remote := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main"))
				if !strings.HasPrefix(remote, receipt.TargetSHA+"\t") {
					t.Fatalf("pushed after failed prerequisite: %q", remote)
				}
			}
			if tt.name == "completed cleanup" && (got.Status != WorktreeMergeComplete || stored.Status == WorktreeMergeComplete) {
				t.Fatalf("completion failure contradicted durable checkpoint: returned=%+v stored=%+v", got, stored)
			}
		})
	}
}

//nolint:paralleltest // The actual merged-PR observation fixture installs process-wide PATH/XDG/WB_TEST_* state.
func TestLandOwnerRecursionReleasesRealLockAndRetainsSaveObservation(t *testing.T) {
	fixture, receipt := landOwnerNativeFixture(t)
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/main")
	receipt.PullRequest = "https://example.test/acme/app/pull/17"
	receipt.PublishedCandidateSHA = receipt.Candidate.SHA
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeMergedPRGH(t)
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
	reads := 0
	releasedBeforeRecursion := false
	sentinel := errors.New("recursive landed checkpoint refuses")
	failed := false
	options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, Progress: func(e progress.Event) {
		if e.Phase == "read_receipt" && e.State == progress.Started {
			reads++
			if reads == 2 {
				lock, err := AcquireOperationLock(fixture.githubDir, receipt.Lane, true)
				if err != nil {
					t.Errorf("prior lock held across recursive entry: %v", err)
					return
				}
				releasedBeforeRecursion = true
				if err := lock.Release(); err != nil {
					t.Error(err)
				}
			}
		}
	}}
	got, err := landWorktreeMerge(context.Background(), options, func(r WorktreeMergeReceipt) error {
		if reads >= 2 && r.LandingSHA != "" {
			failed = true
			return sentinel
		}
		return persistWorktreeMergeReceipt(r)
	})
	if !releasedBeforeRecursion || !failed || err != sentinel || got.LandingSHA == "" {
		t.Fatalf("recursive result=%+v error=%v reads=%d released=%v failed=%v", got, err, reads, releasedBeforeRecursion, failed)
	}
}

//nolint:paralleltest // The native direct journey uses a process-wide scripted remote provider; the lock fault is confined to its own acquired inode.
func TestLandOwnerReleaseRefusesActualOwnedLockReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support renaming this open owned lock inode; native Unix replacement contract")
	}
	fixture, receipt := landOwnerNativeFixture(t)
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
	home, err := wbhome.Root(fixture.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(home, "worktrees", receipt.Lane, ".lock")
	heldPath := lockPath + ".held"
	replaced := false
	options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Cleanup: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond}
	got, err := landWorktreeMerge(context.Background(), options, func(r WorktreeMergeReceipt) error {
		if err := persistWorktreeMergeReceipt(r); err != nil {
			return err
		}
		if !replaced && r.CanonicalSync != "" && r.LandingSHA != "" {
			if err := os.Rename(lockPath, heldPath); err != nil {
				return err
			}
			if err := os.WriteFile(lockPath, []byte("operation=private-successor\npid=1\n"), 0600); err != nil {
				return err
			}
			replaced = true
		}
		return nil
	})
	if !replaced || err == nil || !strings.Contains(err.Error(), "operation lock changed before retirement") || got.Status != WorktreeMergeLanded {
		t.Fatalf("release refusal=%+v %v replaced=%v", got, err, replaced)
	}
	if _, err := os.Stat(receipt.Candidate.Worktree); err != nil {
		t.Fatalf("cleanup ran after release refusal: %v", err)
	}
	contents, readErr := os.ReadFile(lockPath)
	if readErr != nil || string(contents) != "operation=private-successor\npid=1\n" {
		t.Fatalf("successor changed: %q %v", contents, readErr)
	}
	if _, err := os.Stat(heldPath); err != nil {
		t.Fatalf("owned original inode not preserved: %v", err)
	}
}

//nolint:paralleltest // Native resumed landing uses a scripted provider on process-wide PATH; each mutable Git/store fixture is private.
func TestLandOwnerResumedLandingPreservesCanonicalAndCleanupFailures(t *testing.T) {
	for _, tt := range []struct {
		name    string
		change  func(*testing.T, engineFixture, *WorktreeMergeReceipt)
		cleanup bool
		want    WorktreeMergeStatus
		context string
	}{
		{name: "invalid canonical repository", change: func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) {
			r.Repository = "invalid"
			r.CanonicalSync = ""
		}, want: WorktreeMergeCanonicalSyncBlocked, context: "repository"},
		{name: "actual dirty canonical", change: func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(f.canonical, "untracked-after-landing.txt"), "dirty\n")
			r.CanonicalSync = ""
		}, want: WorktreeMergeCanonicalSyncBlocked, context: "dirty"},
		{name: "actual invalid absorbed range", change: func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) { r.TargetSHA = "not-a-commit" }, want: WorktreeMergeLanded, context: "discover absorbed source heads"},
		{name: "cleanup refuses actual dirty source", cleanup: true, change: func(t *testing.T, f engineFixture, r *WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(r.Sources[0].Worktree, "dirty-cleanup.txt"), "dirty source\n")
		}, want: WorktreeMergeLanded, context: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond}
			landed, err := LandWorktreeMerge(context.Background(), options)
			if err != nil || landed.LandingSHA == "" {
				t.Fatalf("native positive landing=%+v %v", landed, err)
			}
			sourcePath := landed.Sources[0].Worktree
			tt.change(t, fixture, &landed)
			if err := persistWorktreeMergeReceipt(landed); err != nil {
				t.Fatal(err)
			}
			options.Cleanup = tt.cleanup
			got, err := LandWorktreeMerge(context.Background(), options)
			if err == nil || got.Status != tt.want || !strings.Contains(err.Error(), tt.context) || got.LandingSHA != landed.LandingSHA {
				t.Fatalf("late refusal=%+v error=%v want status=%s context=%q", got, err, tt.want, tt.context)
			}
			if _, err := os.Stat(sourcePath); err != nil {
				t.Fatalf("late refusal removed source: %v", err)
			}
			stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if readErr != nil || stored.Status != got.Status || stored.Failure != err.Error() {
				t.Fatalf("late durable refusal=%+v %v", stored, readErr)
			}
		})
	}
}

//nolint:paralleltest // The actual CI wait consumes a serial scripted GitHub provider with process-wide PATH/XDG state.
func TestLandOwnerResumedTargetChecksKeepPendingAndHardFailureReceipts(t *testing.T) {
	for _, tt := range []struct {
		name, conclusion string
		want             WorktreeMergeStatus
		slice            time.Duration
	}{
		{name: "pending target checks", conclusion: "", want: WorktreeMergeChecksPending, slice: 20 * time.Millisecond},
		{name: "failed target checks", conclusion: "failure", want: WorktreeMergePostTargetCIFailed, slice: 5 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond}
			landed, err := LandWorktreeMerge(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
			gh.writeState(t, "check-conclusion", tt.conclusion)
			landed.Checks = PullRequestWaitResult{}
			if err := persistWorktreeMergeReceipt(landed); err != nil {
				t.Fatal(err)
			}
			// Failure must be observed through the actual provider subprocess, not a slice deadline.
			options.WaitSlice = tt.slice
			got, err := LandWorktreeMerge(context.Background(), options)
			if err == nil || got.Status != tt.want || got.LandingSHA != landed.LandingSHA || got.CanonicalSync != landed.CanonicalSync {
				t.Fatalf("target checks=%+v error=%v want %s", got, err, tt.want)
			}
			if tt.conclusion == "failure" {
				observedFailure := false
				for _, check := range got.Checks.Checks {
					observedFailure = observedFailure || check.Name == "check-run:CI" && check.Bucket == "fail" && check.Conclusion == "failure"
				}
				if !observedFailure {
					t.Fatalf("hard failure lacks actual completed failing provider check: %+v", got.Checks)
				}
			}
			if _, err := os.Stat(receipt.Candidate.Worktree); err != nil {
				t.Fatalf("check refusal removed candidate: %v", err)
			}
			stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if readErr != nil || stored.Status != got.Status || stored.Failure != err.Error() {
				t.Fatalf("durable check failure=%+v %v", stored, readErr)
			}
		})
	}
}

func TestLandOwnerRegisteredLaneAdmissionKeepsActualSessionIdentity(t *testing.T) {
	t.Parallel()
	for _, different := range []bool{false, true} {
		t.Run(map[bool]string{false: "same registered owner", true: "different live registered owner"}[different], func(t *testing.T) {
			t.Parallel()
			fixture, receipt := landOwnerNativeFixture(t)
			home, err := wbhome.EnsureRoot(fixture.githubDir)
			if err != nil {
				t.Fatal(err)
			}
			owner := landinglane.Owner{WBSessionID: "wbs-land-owner", PID: os.Getpid(), Command: "wb worktree merge land"}
			if _, err := session.Register(filepath.Join(home, session.DirName), session.Record{PID: os.Getpid(), WBSessionID: owner.WBSessionID, Runtime: "codex", Model: "test-model", StartedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			if _, err := landinglane.Acquire(home, landinglane.AcquireRequest{Repository: receipt.Repository, Target: receipt.Target, Self: owner}); err != nil {
				t.Fatal(err)
			}
			requested := owner
			if different {
				requested.WBSessionID = "wbs-different-land-owner"
			}
			sentinel := errors.New("stop after native lane admission")
			run := &landOwnerFaultRunner{Runner: defaultRunner, failure: sentinel, refuse: func(dir, name string, args []string) bool {
				return dir == receipt.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"status", "--porcelain=v1"})
			}}
			got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Lane: LaneGuardRequest{Owner: requested}, run: run})
			if different {
				var conflict *landinglane.ConflictError
				if !errors.As(err, &conflict) || run.refused || conflict.Record.Owner.WBSessionID != owner.WBSessionID {
					t.Fatalf("different live lane=%+v %v runner=%v", got, err, run.calls)
				}
			} else if !errors.Is(err, sentinel) || got.LaneOwner == nil || got.LaneOwner.Owner.WBSessionID != owner.WBSessionID {
				t.Fatalf("same lane=%+v %v", got, err)
			}
		})
	}
}
