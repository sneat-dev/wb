package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

func terminalSyncOwnerCanonicalFixture(t *testing.T) (engineFixture, string, string) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	before := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	updater := filepath.Join(t.TempDir(), "updater")
	runEngineGit(t, filepath.Dir(updater), "clone", f.repository.CloneURL, updater)
	runEngineGit(t, updater, "config", "user.name", "WB Test")
	runEngineGit(t, updater, "config", "user.email", "wb@example.test")
	writeEngineFile(t, filepath.Join(updater, "remote-advance.txt"), "actual remote advance\n")
	runEngineGit(t, updater, "add", "-A")
	runEngineGit(t, updater, "commit", "-m", "private canonical target advance")
	after := strings.TrimSpace(runEngineGit(t, updater, "rev-parse", "HEAD"))
	runEngineGit(t, updater, "push", "origin", "main")
	if contains, err := isMergeAncestor(t.Context(), updater, before, after); err != nil || !contains || before == after {
		t.Fatalf("native forward graph=%t %v", contains, err)
	}
	return f, before, after
}

type terminalSyncOwnerFaultRunner struct {
	runner.Runner
	path, stage, landing     string
	sentinel                 error
	consumed, merged         bool
	afterHeadFailureAncestry bool
}

func (r *terminalSyncOwnerFaultRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if dir == r.path && name == "git" {
		match := false
		switch r.stage {
		case "branch":
			match = reflect.DeepEqual(args, []string{"branch", "--show-current"})
		case "status":
			match = reflect.DeepEqual(args, []string{"status", "--porcelain=v1"})
		case "before HEAD":
			match = !r.merged && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"})
		case "fetch":
			match = reflect.DeepEqual(args, []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"})
		case "merge":
			match = reflect.DeepEqual(args, []string{"merge", "--ff-only", "refs/remotes/origin/main"})
		case "after HEAD":
			match = r.merged && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"})
		case "ancestry":
			match = r.merged && len(args) == 3 && args[0] == "merge-base" && args[1] == r.landing && args[2] == r.landing
		}
		if match && !r.consumed {
			r.consumed = true
			return runner.Result{}, r.sentinel
		}
		if r.stage == "after HEAD" && r.consumed && reflect.DeepEqual(args, []string{"merge-base", r.landing, ""}) {
			r.afterHeadFailureAncestry = true
		}
	}
	result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
	if dir == r.path && name == "git" && reflect.DeepEqual(args, []string{"merge", "--ff-only", "refs/remotes/origin/main"}) && err == nil && result.ExitCode == 0 {
		r.merged = true
	}
	return result, err
}

type terminalSyncOwnerContextKey struct{}

func TestTerminalSyncOwnerNativeCanonicalNotificationsUseActualHeads(t *testing.T) {
	t.Parallel()
	f, before, after := terminalSyncOwnerCanonicalFixture(t)
	key := terminalSyncOwnerContextKey{}
	ctx := context.WithValue(t.Context(), key, "same actual context")
	var updates []CheckoutUpdate
	notify := func(got context.Context, update CheckoutUpdate) {
		if got != ctx {
			t.Fatal("callback context changed")
		}
		updates = append(updates, update)
	}
	status, err := syncCanonicalMergeTarget(ctx, f.canonical, "main", after, 0, 0, notify)
	if err != nil || status != "fast_forwarded" || len(updates) != 1 || updates[0].Checkout != f.canonical || updates[0].OldSHA != before || updates[0].NewSHA != after || updates[0].Cause != "merge-land" {
		t.Fatalf("native changed checkout=%q %v %+v", status, err, updates)
	}
	status, err = syncCanonicalMergeTarget(ctx, f.canonical, "main", after, 0, 0, notify)
	if err != nil || status != "fast_forwarded" || len(updates) != 1 {
		t.Fatalf("unchanged head notified=%q %v %+v", status, err, updates)
	}
	status, err = syncCanonicalMergeTarget(ctx, f.canonical, "main", after, 0, 0, nil)
	if err != nil || status != "fast_forwarded" {
		t.Fatalf("nil observer=%q %v", status, err)
	}
	runEngineGit(t, f.canonical, "checkout", "-b", "private-other")
	status, err = syncCanonicalMergeTarget(ctx, f.canonical, "main", after, 0, 0, notify)
	if err != nil || status != "not_checked_out" || len(updates) != 1 {
		t.Fatalf("other checkout=%q %v", status, err)
	}
}

func TestTerminalSyncOwnerCanonicalObservationFailuresRemainNative(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"branch", "status", "before HEAD", "fetch", "merge", "after HEAD", "ancestry", "native dirty", "native diverged", "native false landing"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f, before, after := terminalSyncOwnerCanonicalFixture(t)
			landing := after
			switch mode {
			case "native dirty":
				writeEngineFile(t, filepath.Join(f.canonical, "dirty.txt"), "uncommitted\n")
			case "native diverged":
				writeEngineFile(t, filepath.Join(f.canonical, "diverged.txt"), "actual divergent commit\n")
				runEngineGit(t, f.canonical, "add", "-A")
				runEngineGit(t, f.canonical, "commit", "-m", "private divergent target")
			case "native false landing":
				runEngineGit(t, f.canonical, "checkout", "-b", "private-unrelated", before)
				writeEngineFile(t, filepath.Join(f.canonical, "other.txt"), "unrelated landing\n")
				runEngineGit(t, f.canonical, "add", "-A")
				runEngineGit(t, f.canonical, "commit", "-m", "private connected unrelated landing")
				landing = strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
				runEngineGit(t, f.canonical, "checkout", "main")
				if contains, e := isMergeAncestor(t.Context(), f.canonical, landing, before); e != nil || contains {
					t.Fatalf("native unrelated preflight=%t %v", contains, e)
				}
			}
			sentinel := errors.New("selected canonical observation")
			run := &terminalSyncOwnerFaultRunner{Runner: defaultRunner, path: f.canonical, stage: mode, landing: after, sentinel: sentinel}
			notified := false
			status, err := syncCanonicalMergeTargetWithRunner(t.Context(), run, f.canonical, "main", landing, 0, 0, func(context.Context, CheckoutUpdate) { notified = true })
			want := ""
			switch mode {
			case "status", "native dirty":
				want = "blocked_dirty"
			case "fetch":
				want = "blocked_fetch"
			case "merge", "native diverged":
				want = "blocked_diverged"
			case "after HEAD", "ancestry", "native false landing":
				want = "blocked_mismatch"
			}
			if err == nil || status != want || notified {
				t.Fatalf("%s=%q %v notified=%t", mode, status, err, notified)
			}
			if !strings.HasPrefix(mode, "native ") && (!run.consumed || !errors.Is(err, sentinel)) {
				t.Fatalf("exact negative stage not consumed: %s %v %+v", mode, err, run)
			}
			if mode == "after HEAD" && !run.afterHeadFailureAncestry {
				t.Fatal("post-HEAD failure skipped original subsequent ancestry observation")
			}
			if mode == "native false landing" && !strings.Contains(err.Error(), "does not contain exact landed head") {
				t.Fatalf("native false DAG=%v", err)
			}
		})
	}
}

//nolint:paralleltest // The reused native Land fixture installs GH/PATH and WB_PROJECTS_ROOT; terminal/claim records and remote refs remain private and sequentially restored.
func TestTerminalSyncOwnerNativeRecoveryPreservesTerminalAndReceiptAuthority(t *testing.T) {
	f, _, r, claims := landedTerminalCleanupFixture(t)
	intent := WorktreeMergeLandOptions{Cleanup: true, Route: WorktreeMergeRouteAuto, OnFailure: "stop"}
	retainWorktreeMergeLandIntent(&r, &intent)
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	r.UpdatedAt = time.Time{}
	clone := func() WorktreeMergeReceipt {
		copy := r
		copy.CleanedTasks = append([]string(nil), r.CleanedTasks...)
		return copy
	}
	restoreReceipt := func() {
		if err := os.WriteFile(r.ReceiptPath, before, 0600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(restoreReceipt)
	live := clone()
	if complete, e := recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), f.githubDir, &live, 0, 0); e != nil || complete || len(live.CleanedTasks) != 0 {
		t.Fatalf("live assets=%t %v %+v", complete, e, live)
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("private file"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked := clone()
	blocked.Candidate.Worktree = filepath.Join(blocker, "candidate")
	if complete, e := recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), f.githubDir, &blocked, 0, 0); e == nil || complete || !strings.Contains(e.Error(), "inspect receipted cleanup worktree") {
		t.Fatalf("native stat refusal=%t %v", complete, e)
	}
	source := r.Sources[0]
	externallyTerminalizeTask(t, f, &r, source.Task)
	terminal := terminalWorkLogPath(claims[source.Task])
	terminalBytes, err := os.ReadFile(terminal)
	if err != nil {
		t.Fatal(err)
	}
	restoreTerminal := func() {
		if err := os.WriteFile(terminal, terminalBytes, 0600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(restoreTerminal)
	if err := os.Remove(terminal); err != nil {
		t.Fatal(err)
	}
	missing := clone()
	complete, e := recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), f.githubDir, &missing, 0, 0)
	restoreTerminal()
	if e == nil || complete || !strings.Contains(e.Error(), "does not corroborate partially terminalized cleanup") || len(missing.CleanedTasks) != 0 {
		t.Fatalf("partial missing terminal=%t %v %+v", complete, e, missing)
	}
	restoreBranch := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, e := defaultRunner.RunOpts(ctx, f.canonical, runner.RunOptions{CaptureCombined: true}, "git", "update-ref", "-d", "refs/heads/"+source.Branch)
		if e != nil || result.ExitCode != 0 {
			t.Errorf("restore native source branch: %+v %v", result, e)
			return
		}
		result, e = defaultRunner.RunOpts(ctx, f.canonical, runner.RunOptions{CaptureCombined: true}, "git", "for-each-ref", "--format=%(refname)", "refs/heads/"+source.Branch)
		if e != nil || result.ExitCode != 0 || strings.TrimSpace(result.CombinedOutput) != "" {
			t.Errorf("verify restored native branch absence: %+v %v", result, e)
		}
	}
	t.Cleanup(restoreBranch)
	runEngineGit(t, f.canonical, "update-ref", "refs/heads/"+source.Branch, source.SHA)
	branch := clone()
	complete, e = recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), f.githubDir, &branch, 0, 0)
	restoreBranch()
	if e == nil || complete || !strings.Contains(e.Error(), "local branch") || len(branch.CleanedTasks) != 0 {
		t.Fatalf("real remaining branch=%t %v", complete, e)
	}
	failed := clone()
	failed.ReceiptPath = filepath.Join(blocker, "receipt.json")
	complete, e = recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), f.githubDir, &failed, 0, 0)
	if e == nil || complete || !reflect.DeepEqual(failed.CleanedTasks, []string{source.Task}) || failed.UpdatedAt.IsZero() {
		t.Fatalf("native partial persist refusal=%t %v %+v", complete, e, failed)
	}
	partial := clone()
	complete, e = recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), f.githubDir, &partial, 0, 0)
	if e != nil || complete || !reflect.DeepEqual(partial.CleanedTasks, []string{source.Task}) || partial.UpdatedAt.IsZero() {
		t.Fatalf("partial native checkpoint=%t %v %+v", complete, e, partial)
	}
	stored, e := readWorktreeMergeReceipt(r.ReceiptPath)
	if e != nil || !reflect.DeepEqual(stored.CleanedTasks, partial.CleanedTasks) {
		t.Fatalf("durable partial checkpoint=%+v %v", stored, e)
	}
	restoreReceipt()
	already := clone()
	already.CleanedTasks = []string{source.Task, source.Task}
	complete, e = recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), f.githubDir, &already, 0, 0)
	if e != nil || complete || !reflect.DeepEqual(already.CleanedTasks, []string{source.Task, source.Task}) {
		t.Fatalf("original duplicate preservation=%t %v %+v", complete, e, already)
	}
	restoreReceipt()
	externallyTerminalizeTask(t, f, &r, r.Candidate.Task)
	candidateTerminal := terminalWorkLogPath(claims[r.Candidate.Task])
	candidateBytes, err := os.ReadFile(candidateTerminal)
	if err != nil {
		t.Fatal(err)
	}
	restoreCandidate := func() {
		if err := os.WriteFile(candidateTerminal, candidateBytes, 0600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(restoreCandidate)
	if err := os.WriteFile(candidateTerminal, []byte("{invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	invalid := clone()
	complete, e = recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), f.githubDir, &invalid, 0, 0)
	restoreCandidate()
	if e == nil || complete || !strings.Contains(e.Error(), "does not corroborate completed cleanup") {
		t.Fatalf("native invalid terminal=%t %v", complete, e)
	}
	if err := os.Remove(candidateTerminal); err != nil {
		t.Fatal(err)
	}
	unavailable := clone()
	complete, e = recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), f.githubDir, &unavailable, 0, 0)
	if e == nil || complete || !strings.Contains(e.Error(), "audited missing-cleanup recovery unavailable") {
		t.Fatalf("missing native ACK=%t %v", complete, e)
	}
	ack, err := AcknowledgeMissingWorktreeMergeCleanup(t.Context(), WorktreeMergeMissingCleanupAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Actor: "reviewer", Reason: "native removed cleanup evidence", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(ack.AcknowledgementPath); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	audited := clone()
	complete, e = recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), f.githubDir, &audited, 0, 0)
	restoreCandidate()
	if e != nil || !complete || !reflect.DeepEqual(audited.CleanedTasks, sortedUniqueMergeTasks(r)) {
		t.Fatalf("authentic missing-cleanup ACK=%t %v %+v", complete, e, audited)
	}
	full := clone()
	complete, e = recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), f.githubDir, &full, 0, 0)
	if e != nil || !complete || !reflect.DeepEqual(full.CleanedTasks, sortedUniqueMergeTasks(r)) {
		t.Fatalf("native full terminal evidence=%t %v %+v", complete, e, full)
	}
	after, err := os.ReadFile(r.ReceiptPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("historical receipt changed outside partial checkpoint: %v", err)
	}
	actual, e := os.ReadFile(terminal)
	if e != nil || string(actual) != string(terminalBytes) {
		t.Fatalf("native source terminal changed: %v", e)
	}
}
