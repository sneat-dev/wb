package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixture deliberately makes the claim branch survive at a different
// local and remote head. The live branch is the landed head, so reconciliation
// must preserve both recovery coordinates before it retires either one.
func prepareBranchReconciliationFixture(t *testing.T) (*gitFixture, CreateResult, string, string, string, time.Time) {
	t.Helper()
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "branch-reconcile",
		WorkLog:      WorkLogOptions{RunID: "branch-reconcile-run", Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	claimBranch := result.Branch
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "landed.txt"), []byte("landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", "landed.txt")
	gitTest(t, result.WorktreeDir, "commit", "-m", "landed work")
	landedHead := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", claimBranch)

	// Keep the local claim ref at its first, now-landed value.
	gitTest(t, fixture.canonical, "merge", "--no-ff", claimBranch, "-m", "merge landed work")
	gitTest(t, fixture.canonical, "push", "origin", "main")

	// Advance only the remote claim branch from a separate clone, making the
	// old local and remote heads distinct recovery assets.
	other := filepath.Join(t.TempDir(), "other")
	gitTest(t, fixture.projectsRoot, "clone", fixture.remote, other)
	configureGitUser(t, other)
	gitTest(t, other, "checkout", "-b", claimBranch, "origin/"+claimBranch)
	if err := os.WriteFile(filepath.Join(other, "remote-only.txt"), []byte("remote only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, other, "add", "remote-only.txt")
	gitTest(t, other, "commit", "-m", "remote claim advance")
	remoteClaimHead := gitTestOutput(t, other, "rev-parse", "HEAD")
	gitTest(t, other, "push", "origin", claimBranch)

	// The landing branch points at the immutable landing commit. It is a
	// branch rename, not a new commit, and therefore can be reconciled back.
	liveBranch := "codex/branch-reconcile-landing"
	gitTest(t, result.WorktreeDir, "branch", "-m", liveBranch)
	gitTest(t, fixture.canonical, "update-ref", "refs/heads/"+claimBranch, landedHead)
	installMergedPullRequestFixture(t, landedHead, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC))
	return fixture, result, liveBranch, landedHead, remoteClaimHead, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
}

func reconcileOptions(fixture *gitFixture, result CreateResult, liveBranch, head string) LogRecoverOptions {
	return LogRecoverOptions{
		ProjectsRoot:    fixture.projectsRoot,
		Worktree:        result.WorktreeDir,
		ReconcileBranch: liveBranch,
		ExpectedHead:    head,
		Remote:          true,
		Actor:           "alex",
		Reason:          "restore immutable Work Log branch identity after landing",
		EventID:         "branch-reconcile-test-event",
	}
}

func TestReconcileClaimBranchDryRunIsReadOnly(t *testing.T) {
	fixture, result, liveBranch, head, remoteHead, _ := prepareBranchReconciliationFixture(t)
	options := reconcileOptions(fixture, result, liveBranch, head)
	beforeEvents, err := readLocalEvents(result.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	dry, err := LogRecover(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Applied || dry.ReadyForNormalCleanup {
		t.Fatalf("dry reconciliation = %#v", dry)
	}
	if gitTestOutput(t, result.WorktreeDir, "branch", "--show-current") != liveBranch {
		t.Fatal("dry reconciliation changed the live branch")
	}
	if got := remoteBranchForTest(t, fixture.canonical, result.Branch); got != remoteHead {
		t.Fatalf("dry reconciliation changed remote claim ref to %s, want %s", got, remoteHead)
	}
	afterEvents, err := readLocalEvents(result.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("dry reconciliation appended events: before=%d after=%d", len(beforeEvents), len(afterEvents))
	}
}

func TestReconcileClaimBranchApplyPreservesOldLocalAndRemoteHeads(t *testing.T) {
	fixture, result, liveBranch, head, remoteHead, _ := prepareBranchReconciliationFixture(t)
	claimHead := gitTestOutput(t, fixture.canonical, "rev-parse", "refs/heads/"+result.Branch)
	if claimHead == remoteHead {
		t.Fatal("fixture did not create distinct local and remote claim heads")
	}
	options := reconcileOptions(fixture, result, liveBranch, head)
	options.Apply = true
	resultValue, err := LogRecover(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !resultValue.Applied || !resultValue.ReadyForNormalCleanup {
		t.Fatalf("applied reconciliation = %#v", resultValue)
	}
	if got := gitTestOutput(t, result.WorktreeDir, "branch", "--show-current"); got != result.Branch {
		t.Fatalf("live branch = %q, want immutable claim %q", got, result.Branch)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "refs/heads/"+result.Branch); got != head {
		t.Fatalf("rebound immutable claim ref = %s, want live head %s", got, head)
	}
	if got := remoteBranchForTest(t, fixture.canonical, result.Branch); got != "" {
		t.Fatalf("old remote claim ref survived reconciliation at %s", got)
	}
	for _, head := range []string{claimHead, remoteHead} {
		if !reconciliationBundleContains(t, fixture.home, options.EventID, head) {
			t.Fatalf("no verified recovery bundle preserves %s", head)
		}
	}
	for kind, want := range map[string]struct {
		ref  string
		head string
	}{
		"local":  {ref: "refs/heads/" + result.Branch, head: claimHead},
		"remote": {ref: "refs/remotes/origin/" + result.Branch, head: remoteHead},
	} {
		if got := reconciliationBundleRefs(t, fixture.canonical, fixture.home, options.EventID, kind)[want.ref]; got != want.head {
			t.Fatalf("%s recovery bundle ref %q = %q, want %q", kind, want.ref, got, want.head)
		}
	}
}

func TestReconcileClaimBranchApplyReportsReadyForCleanup(t *testing.T) {
	fixture, result, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
	options := reconcileOptions(fixture, result, liveBranch, head)
	options.Apply = true
	resultValue, err := LogRecover(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !resultValue.ReadyForNormalCleanup || !strings.Contains(strings.Join(resultValue.Notes, " "), "ready for normal cleanup") {
		t.Fatalf("ready-for-cleanup receipt = %#v", resultValue)
	}
}

func TestReconcileClaimBranchDoesNotEditImmutableClaim(t *testing.T) {
	fixture, result, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
	projection, err := readWorkLogProjection(result.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "claims", projection.ClaimID+".json")
	before, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	options := reconcileOptions(fixture, result, liveBranch, head)
	options.Apply = true
	if _, err := LogRecover(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("reconciliation edited the immutable claim")
	}
}

func TestReconcileClaimBranchRefusesWrongLiveBranchOrHead(t *testing.T) {
	fixture, result, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
	for name, mutate := range map[string]func(*LogRecoverOptions){
		"wrong branch": func(options *LogRecoverOptions) { options.ReconcileBranch = "codex/not-live" },
		"wrong head":   func(options *LogRecoverOptions) { options.ExpectedHead = strings.Repeat("a", 40) },
	} {
		t.Run(name, func(t *testing.T) {
			options := reconcileOptions(fixture, result, liveBranch, head)
			options.Apply = true
			mutate(&options)
			if _, err := LogRecover(context.Background(), options); err == nil {
				t.Fatal("unsafe reconciliation unexpectedly succeeded")
			}
			if got := gitTestOutput(t, result.WorktreeDir, "branch", "--show-current"); got != liveBranch {
				t.Fatalf("unsafe reconciliation changed branch to %q", got)
			}
		})
	}
}

func TestReconcileClaimBranchRefusesUnreadableOwnerMetadata(t *testing.T) {
	fixture, result, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
	ownerPath := filepath.Join(result.WorktreeDir, ".wb", "local", "worklog", "events.jsonl")
	if err := os.Chmod(ownerPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ownerPath, 0o600) })
	options := reconcileOptions(fixture, result, liveBranch, head)
	options.Apply = true
	if _, err := LogRecover(context.Background(), options); err == nil {
		t.Fatal("unreadable owner metadata unexpectedly reconciled")
	}
}

func TestReconcileClaimBranchRefusesMovedOldLocalOrRemoteRef(t *testing.T) {
	for name, move := range map[string]func(*gitFixture, CreateResult){
		"local": func(fixture *gitFixture, result CreateResult) {
			gitTest(t, fixture.canonical, "update-ref", "refs/heads/"+result.Branch, "origin/main")
		},
		"remote": func(fixture *gitFixture, result CreateResult) {
			gitTest(t, fixture.canonical, "push", "--force", "origin", "origin/main:refs/heads/"+result.Branch)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture, result, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
			options := reconcileOptions(fixture, result, liveBranch, head)
			options.Apply = true
			options.testBeforeBundleCheck = func() { move(fixture, result) }
			if _, err := LogRecover(context.Background(), options); err == nil {
				t.Fatal("moved claim ref bypassed exact preservation lease")
			}
			if got := gitTestOutput(t, result.WorktreeDir, "branch", "--show-current"); got != liveBranch {
				t.Fatalf("moved claim ref changed live branch to %q", got)
			}
		})
	}
}

func TestReconcileClaimBranchRefusesOpenDependentPR(t *testing.T) {
	fixture, result, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
	installOpenReconciliationPullRequestFixture(t, head)
	options := reconcileOptions(fixture, result, liveBranch, head)
	if _, err := LogRecover(context.Background(), options); err == nil || !strings.Contains(err.Error(), "open pull request") {
		t.Fatalf("open dependent PR error = %v", err)
	}
}

func TestReconcileClaimBranchRefusesTargetRegression(t *testing.T) {
	fixture, result, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
	gitTest(t, fixture.canonical, "reset", "--hard", "HEAD~1")
	gitTest(t, fixture.canonical, "push", "--force", "origin", "main")
	options := reconcileOptions(fixture, result, liveBranch, head)
	if _, err := LogRecover(context.Background(), options); err == nil || !strings.Contains(err.Error(), "not integrated") {
		t.Fatalf("target regression error = %v", err)
	}
}

func TestReconcileClaimBranchBundleFailureLeavesAllRefsUntouched(t *testing.T) {
	fixture, result, liveBranch, head, remoteHead, _ := prepareBranchReconciliationFixture(t)
	claimHead := gitTestOutput(t, fixture.canonical, "rev-parse", "refs/heads/"+result.Branch)
	options := reconcileOptions(fixture, result, liveBranch, head)
	options.Apply = true
	options.testFailAfterBundle = "local"
	if _, err := LogRecover(context.Background(), options); err == nil {
		t.Fatal("injected bundle failure unexpectedly succeeded")
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "refs/heads/"+result.Branch); got != claimHead {
		t.Fatalf("bundle failure changed local claim ref to %s", got)
	}
	if got := remoteBranchForTest(t, fixture.canonical, result.Branch); got != remoteHead {
		t.Fatalf("bundle failure changed remote claim ref to %s", got)
	}
}

func TestReconcileClaimBranchResumeAfterRemoteRetirement(t *testing.T) {
	assertReconciliationResume(t, "remote")
}

func TestReconcileClaimBranchResumeAfterLocalRetirement(t *testing.T) {
	assertReconciliationResume(t, "local")
}

func TestReconcileClaimBranchResumeAfterBranchRebind(t *testing.T) {
	assertReconciliationResume(t, "rebound")
}

func TestReconcileClaimBranchResumeAfterEventAppend(t *testing.T) {
	assertReconciliationResume(t, "event")
}

func assertReconciliationResume(t *testing.T, stage string) {
	t.Helper()
	fixture, result, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
	options := reconcileOptions(fixture, result, liveBranch, head)
	options.Apply = true
	options.testStopAfterStage = stage
	if _, err := LogRecover(context.Background(), options); err == nil {
		t.Fatalf("injected %s interruption unexpectedly completed", stage)
	}
	if _, err := LogRecover(context.Background(), options); err != nil {
		t.Fatalf("resume after %s retirement: %v", stage, err)
	}
	if got := gitTestOutput(t, result.WorktreeDir, "branch", "--show-current"); got != result.Branch {
		t.Fatalf("resumed live branch = %q, want %q", got, result.Branch)
	}
	_ = fixture
}

func TestReconcileClaimBranchIsIdempotent(t *testing.T) {
	fixture, result, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
	options := reconcileOptions(fixture, result, liveBranch, head)
	options.Apply = true
	first, err := LogRecover(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LogRecover(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Event == nil || second.Event == nil || first.Event.ID != second.Event.ID {
		t.Fatalf("reconciliation was not event-idempotent: first=%#v second=%#v", first, second)
	}
	events, err := readLocalEvents(result.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == LocalEventBranchReconciled {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("branch reconciliation events = %d, want 1", count)
	}
}

func reconciliationBundleContains(t *testing.T, home, eventID, head string) bool {
	t.Helper()
	root := filepath.Join(home, "worklogs")
	found := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.Contains(path, eventID) || !strings.HasSuffix(path, ".json") {
			return err
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var evidence map[string]any
		if json.Unmarshal(contents, &evidence) == nil && evidence["head"] == head {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func reconciliationBundleRefs(t *testing.T, canonical, home, eventID, kind string) map[string]string {
	t.Helper()
	var bundle string
	err := filepath.WalkDir(filepath.Join(home, "worklogs"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.Contains(path, eventID) && filepath.Base(path) == kind+".bundle" {
			bundle = path
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if bundle == "" {
		t.Fatalf("%s recovery bundle was not retained", kind)
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(gitTestOutput(t, canonical, "bundle", "list-heads", bundle)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			refs[fields[1]] = fields[0]
		}
	}
	return refs
}

func installOpenReconciliationPullRequestFixture(t *testing.T, head string) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$WB_TEST_OPEN_PULL\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal([]map[string]any{{
		"number": 99, "html_url": "https://github.com/acme/app/pull/99", "state": "open",
		"merged_at": nil, "head": map[string]any{"ref": "codex/branch-reconcile-landing", "sha": head},
		"base": map[string]any{"ref": "main", "sha": ""}, "merge_commit_sha": "",
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_TEST_OPEN_PULL", string(payload))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

