package streamsync

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// AC: sync-writes-no-bump-that-renovate-already-landed — the rebase happens
// FIRST, so a bump Renovate already merged is present in the tree and sync
// writes nothing for it; a second library still below target does get its one
// commit; and a second run produces no new commits at all.
func TestSyncWritesNoBumpRenovateAlreadyLanded(t *testing.T) {
	engine, git, bumper, _, _ := newTestEngine()
	options := baseOptions()
	options.Libraries = []Library{
		{Name: "L", Target: "v1.2.0", Ecosystem: "go"},
		{Name: "M", Target: "v1.3.0", Ecosystem: "go"},
	}
	// After the rebase the tree already carries Renovate's L v1.2.0.
	bumper.required["L"] = "v1.2.0"
	bumper.required["M"] = "v1.0.0"

	result, err := engine.Sync(context.Background(), options)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	byLibrary := map[string]BumpResult{}
	for _, bump := range result.Bumps {
		byLibrary[bump.Library.Name] = bump
	}
	if byLibrary["L"].Action != "already-at-target" || byLibrary["L"].Commit != "" {
		t.Fatalf("L = %#v; want no commit — no duplicate, no revert", byLibrary["L"])
	}
	if byLibrary["M"].Action != "bumped" || byLibrary["M"].Commit == "" {
		t.Fatalf("M = %#v; want its one bump commit in the same run", byLibrary["M"])
	}
	// The ordering is the mechanism: the rebase must precede every comparison.
	rebaseAt, commitAt := -1, -1
	for index, call := range git.calls {
		if strings.HasPrefix(call, "rebase stream/checkout") && rebaseAt < 0 {
			rebaseAt = index
		}
		if strings.HasPrefix(call, "commit ") && commitAt < 0 {
			commitAt = index
		}
	}
	if rebaseAt < 0 || commitAt < 0 || rebaseAt > commitAt {
		t.Fatalf("calls = %v; the rebase must precede the first bump commit", git.calls)
	}

	// Second run, nothing else changed: no new commits at all.
	before := len(bumper.applied)
	second, err := engine.Sync(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	for _, bump := range second.Bumps {
		if bump.Action == "bumped" {
			t.Fatalf("a second sync wrote %s again: %#v", bump.Library.Name, bump)
		}
	}
	if len(bumper.applied) != before {
		t.Fatalf("a second sync applied %d more bump(s)", len(bumper.applied)-before)
	}
}

// A bump whose apply changes nothing on disk must not leave an empty commit:
// that would make a re-run non-idempotent.
func TestABumpThatChangesNothingWritesNoCommit(t *testing.T) {
	engine, git, bumper, _, _ := newTestEngine()
	options := baseOptions()
	options.Libraries = []Library{{Name: "L", Target: "v2.0.0", Ecosystem: "go"}}
	bumper.required["L"] = "v1.0.0"
	bumper.changesNothing["L"] = true
	git.nothingToDo[BumpMessage(options.Libraries[0], "v1.0.0")] = true

	result, err := engine.Sync(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Bumps[0].Action != "already-at-target" || result.Bumps[0].Commit != "" {
		t.Fatalf("bump = %#v; want no commit when apply changed nothing", result.Bumps[0])
	}
}

// AC: sync-rebases-and-reports-conflicts-per-agent — the stream branch rebases
// onto origin/main with no merge, the non-conflicting agent branches are
// rebased, and the conflict names the branch, its agent and the paths while the
// others are still reported.
func TestSyncReportsConflictsPerAgentBranch(t *testing.T) {
	engine, git, _, _, _ := newTestEngine()
	options := baseOptions()
	options.AgentBranches = []AgentBranch{
		{Branch: "agent/one", Agent: "wbs-1"},
		{Branch: "agent/two", Agent: "wbs-2"},
		{Branch: "agent/three", Agent: "wbs-3"},
	}
	git.conflicts["agent/two"] = []string{"backend/handler.go", "backend/router.go"}

	result, err := engine.Sync(context.Background(), options)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !result.StreamRebase.Rebased {
		t.Fatalf("stream rebase = %#v", result.StreamRebase)
	}
	if len(result.AgentRebases) != 3 {
		t.Fatalf("agent rebases = %#v; a conflict in one must not stop the others", result.AgentRebases)
	}
	byBranch := map[string]RebaseResult{}
	for _, rebase := range result.AgentRebases {
		byBranch[rebase.Branch] = rebase
	}
	if !byBranch["agent/one"].Rebased || !byBranch["agent/three"].Rebased {
		t.Errorf("the non-conflicting branches were not rebased: %#v", result.AgentRebases)
	}
	conflicted := byBranch["agent/two"]
	if conflicted.Rebased || len(conflicted.Conflicts) != 2 {
		t.Fatalf("agent/two = %#v; want the conflict reported", conflicted)
	}
	if conflicted.Agent != "wbs-2" {
		t.Errorf("the conflict does not name its claiming agent: %#v", conflicted)
	}
	// No merge is ever created.
	for _, call := range git.calls {
		if strings.HasPrefix(call, "merge") {
			t.Fatalf("sync created a merge: %v", git.calls)
		}
	}
}

// Sync refuses by default while a branch is under review, because rebasing it
// invalidates the review that pinned its patch set.
func TestSyncRefusesWhileABranchIsUnderReview(t *testing.T) {
	engine, git, _, _, _ := newTestEngine()
	options := baseOptions()
	options.AgentBranches = []AgentBranch{{Branch: "agent/one", Agent: "wbs-1", InReview: true}}

	_, err := engine.Sync(context.Background(), options)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != "review-in-progress" {
		t.Fatalf("error = %v, want a review-in-progress refusal", err)
	}
	if !strings.Contains(strings.Join(refusal.Sanctioned, " "), "--allow-mid-review") {
		t.Errorf("refusal does not name the override: %v", refusal.Sanctioned)
	}
	if len(git.calls) != 0 {
		t.Errorf("a refused sync still acted: %v", git.calls)
	}

	options.AllowMidReview = true
	if _, err := engine.Sync(context.Background(), options); err != nil {
		t.Fatalf("--allow-mid-review: %v", err)
	}
}

// AC: ten-bumps-are-one-push-and-one-ci-run — ten bumps become ten LOCAL
// commits and zero pushes; a sync with no trigger says the remote was left
// untouched; --push without --reason is refused naming the four triggers.
func TestTenBumpsAreTenLocalCommitsAndZeroPushes(t *testing.T) {
	engine, git, bumper, verifier, _ := newTestEngine()
	options := baseOptions()
	options.Verify = true
	for index := 0; index < 10; index++ {
		name := string(rune('A' + index))
		options.Libraries = append(options.Libraries, Library{Name: name, Target: "v2.0.0", Ecosystem: "go"})
		bumper.required[name] = "v1.0.0"
	}
	git.ahead = 10
	verifier.runs = []VerificationRun{{Passed: true, Command: "go build ./... && go test -p 1 ./..."}}

	result, err := engine.Sync(context.Background(), options)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	bumped := 0
	for _, bump := range result.Bumps {
		if bump.Action == "bumped" {
			bumped++
		}
	}
	if bumped != 10 {
		t.Fatalf("bumped = %d, want 10 local commits", bumped)
	}
	if git.pushed() {
		t.Fatalf("sync pushed: %v", git.calls)
	}
	if result.Push != nil {
		t.Fatalf("push = %#v, want none: a dependency bump is not a trigger", result.Push)
	}
	// The batch is verified exactly once for all ten.
	if result.Batch == nil || !result.Batch.Passed || result.Batch.Runs != 1 {
		t.Fatalf("batch = %#v, want one passing run for the whole batch", result.Batch)
	}
	if verifier.calls != 1 {
		t.Fatalf("verifier ran %d times, want once", verifier.calls)
	}
	// The remote being untouched is stated, not left to inference.
	if !strings.Contains(result.PushSkipped, "left untouched") {
		t.Fatalf("push_skipped = %q, want it to say the remote was untouched", result.PushSkipped)
	}
	if result.Unpushed.Commits != 10 {
		t.Fatalf("unpushed = %#v, want the ten local commits reported", result.Unpushed)
	}
	if !strings.Contains(result.Unpushed.String(), "10 local commit(s) not pushed") {
		t.Errorf("status line = %q", result.Unpushed.String())
	}
}

func TestPushWithoutAReasonIsRefusedNamingTheFourTriggers(t *testing.T) {
	engine, _, _, _, _ := newTestEngine()
	options := baseOptions()
	options.PushTrigger = TriggerExplicit

	_, err := engine.Sync(context.Background(), options)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != RefusalUnjustifiedPush {
		t.Fatalf("error = %v, want an %s refusal", err, RefusalUnjustifiedPush)
	}
	if !strings.Contains(refusal.Message, "--reason") {
		t.Errorf("refusal does not require a reason: %s", refusal.Message)
	}
}

func TestAnUnrecognisedPushTriggerIsRefusedListingAllFour(t *testing.T) {
	_, err := JustifyPush("because-i-said-so", "")
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != RefusalUnjustifiedPush {
		t.Fatalf("error = %v, want an %s refusal", err, RefusalUnjustifiedPush)
	}
	for _, trigger := range PushTriggers() {
		if !strings.Contains(refusal.Message, string(trigger)) {
			t.Errorf("refusal does not list %q: %s", trigger, refusal.Message)
		}
	}
}

// Every justified push writes an event carrying its trigger and reason, so
// pushes per stream can be counted after the fact.
func TestAJustifiedPushRecordsItsTriggerAndReason(t *testing.T) {
	engine, _, _, _, events := newTestEngine()
	options := baseOptions()
	options.PushTrigger = TriggerLanding

	result, err := engine.Sync(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Push == nil || result.Push.Trigger != TriggerLanding {
		t.Fatalf("push = %#v", result.Push)
	}
	recorded := events.withPhase("push")
	if len(recorded) != 1 {
		t.Fatalf("push events = %#v, want exactly one", recorded)
	}
	if recorded[0].Evidence["trigger"] != string(TriggerLanding) || recorded[0].Evidence["reason"] == "" {
		t.Fatalf("event = %#v, want trigger and reason recorded", recorded[0])
	}
}

// A dirty worktree is refused: sync rebases and commits, so it will not run
// over uncommitted work.
func TestSyncRefusesADirtyWorktree(t *testing.T) {
	engine, git, _, _, _ := newTestEngine()
	git.clean = false
	_, err := engine.Sync(context.Background(), baseOptions())
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != "dirty-worktree" {
		t.Fatalf("error = %v, want a dirty-worktree refusal", err)
	}
}

func TestBelowIsTheIdempotenceComparison(t *testing.T) {
	for _, testCase := range []struct {
		required, target string
		want             bool
	}{
		{"v1.1.0", "v1.2.0", true},
		{"v1.2.0", "v1.2.0", false},
		{"v1.3.0", "v1.2.0", false},
		{"v1.2.0-rc.1", "v1.2.0", true},
		{"^1.1.0", "1.2.0", true},
		// Unreadable on either side writes no commit: rewriting a manifest on
		// a comparison that never happened is the worse mistake.
		{"workspace:*", "1.2.0", false},
		{"v1.2.0", "latest", false},
	} {
		if got := below(testCase.required, testCase.target); got != testCase.want {
			t.Errorf("below(%q, %q) = %t, want %t", testCase.required, testCase.target, got, testCase.want)
		}
	}
}

// MF-1. A justified push REALLY pushes, under --force-with-lease against the
// recorded head, and the reported SHA is what the remote holds. Setting
// result.Push and eventing success without pushing reported an effect that did
// not exist.
func TestAJustifiedPushActuallyPushesUnderALease(t *testing.T) {
	engine, git, _, _, events := newTestEngine()
	options := baseOptions()
	options.PushTrigger = TriggerExplicit
	options.PushReason = "handing off to the release lane"
	options.RecordedRemoteHead = "recorded-head-sha"

	result, err := engine.Sync(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !git.pushed() {
		t.Fatalf("nothing was pushed: %v", git.calls)
	}
	if len(git.pushes) != 1 || git.pushes[0] != options.Branch {
		t.Fatalf("pushes = %v, want the stream branch once", git.pushes)
	}
	// The lease is what stops a rebase force-push discarding another agent.
	leased := false
	for _, call := range git.calls {
		if strings.Contains(call, "push "+options.Branch+" lease=recorded-head-sha") {
			leased = true
		}
	}
	if !leased {
		t.Fatalf("calls = %v, want --force-with-lease against the recorded head", git.calls)
	}
	if result.Push == nil || result.Push.SHA != "pushed-sha" {
		t.Fatalf("push = %#v, want the verified remote SHA", result.Push)
	}
	pushEvents := events.withPhase("push")
	if len(pushEvents) != 1 || pushEvents[0].Outcome != "success" || pushEvents[0].Evidence["sha"] != "pushed-sha" {
		t.Fatalf("push events = %#v, want one success carrying the pushed SHA", pushEvents)
	}
}

// A push that FAILS is reported as a failure, and no push is claimed.
func TestAFailedPushIsReportedAndNotClaimed(t *testing.T) {
	engine, git, _, _, events := newTestEngine()
	git.pushErr = errors.New("stale info: remote ref moved")
	options := baseOptions()
	options.PushTrigger = TriggerLanding

	result, err := engine.Sync(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Push != nil {
		t.Fatalf("push = %#v, want nothing claimed after a failed push", result.Push)
	}
	if !result.Failed() {
		t.Fatal("a failed push reported success")
	}
	pushEvents := events.withPhase("push")
	if len(pushEvents) != 1 || pushEvents[0].Outcome != "findings" {
		t.Fatalf("push events = %#v, want the event to follow the real outcome", pushEvents)
	}
}

// MF-2. A failed bump is a FAILURE, and the worktree is restored to the state
// sync found it in — a half-applied manifest would make the next sync refuse
// as dirty and tell the operator to commit it.
func TestAFailedBumpFailsTheRunAndRestoresTheWorktree(t *testing.T) {
	engine, git, bumper, _, _ := newTestEngine()
	options := baseOptions()
	options.Libraries = []Library{{Name: "L", Target: "v2.0.0", Ecosystem: "go"}}
	bumper.required["L"] = "v1.0.0"
	bumper.applyErr["L"] = errors.New("go mod tidy: checksum mismatch")
	git.heads["HEAD"] = "pre-bump-head"

	result, err := engine.Sync(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed() {
		t.Fatal("a failed bump exited 0")
	}
	if result.Bumps[0].Action != BumpFailed {
		t.Fatalf("bump = %#v, want it marked failed", result.Bumps[0])
	}
	if len(git.restored) != 1 || git.restored[0] != "pre-bump-head" {
		t.Fatalf("restored = %v, want the worktree returned to the pre-bump head", git.restored)
	}
	if !strings.Contains(result.Bumps[0].Detail, "restored") {
		t.Errorf("detail does not say the worktree was restored: %q", result.Bumps[0].Detail)
	}
}

// SF-1. An unreadable version is reported as unreadable, not as
// "already-at-target": no commit either way, but only one of those is true.
func TestAnUnreadableVersionIsReportedAsUnreadable(t *testing.T) {
	engine, _, bumper, _, _ := newTestEngine()
	options := baseOptions()
	options.Libraries = []Library{{Name: "L", Target: "v2.0.0", Ecosystem: "go"}}
	bumper.required["L"] = "workspace:*"

	result, err := engine.Sync(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Bumps[0].Action != BumpUnreadableVersion {
		t.Fatalf("action = %q, want %q", result.Bumps[0].Action, BumpUnreadableVersion)
	}
	if result.Bumps[0].Commit != "" {
		t.Error("an unreadable version wrote a commit")
	}
}

// MF-3. EVERY exit writes exactly one terminal event, including the failure
// paths that previously wrote none.
func TestEveryExitWritesExactlyOneTerminalEvent(t *testing.T) {
	t.Run("stream conflict", func(t *testing.T) {
		engine, git, _, _, events := newTestEngine()
		git.conflicts["stream/checkout"] = []string{"backend/handler.go"}
		if _, err := engine.Sync(context.Background(), baseOptions()); err != nil {
			t.Fatal(err)
		}
		assertOneTerminalEvent(t, events, "findings")
	})
	t.Run("agent conflict", func(t *testing.T) {
		engine, git, _, _, events := newTestEngine()
		options := baseOptions()
		options.AgentBranches = []AgentBranch{{Branch: "agent/one", Agent: "wbs-1"}}
		git.conflicts["agent/one"] = []string{"backend/handler.go"}
		if _, err := engine.Sync(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		assertOneTerminalEvent(t, events, "findings")
	})
	t.Run("failed bump", func(t *testing.T) {
		engine, _, bumper, _, events := newTestEngine()
		options := baseOptions()
		options.Libraries = []Library{{Name: "L", Target: "v2.0.0", Ecosystem: "go"}}
		bumper.required["L"] = "v1.0.0"
		bumper.applyErr["L"] = errors.New("boom")
		if _, err := engine.Sync(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		assertOneTerminalEvent(t, events, "findings")
	})
	t.Run("review-in-progress refusal", func(t *testing.T) {
		engine, _, _, _, events := newTestEngine()
		options := baseOptions()
		options.AgentBranches = []AgentBranch{{Branch: "agent/one", InReview: true}}
		if _, err := engine.Sync(context.Background(), options); err == nil {
			t.Fatal("expected a refusal")
		}
		event := assertOneTerminalEvent(t, events, "refused")
		if event.Evidence["refusal_code"] != "review-in-progress" {
			t.Errorf("event = %#v, want the refusal code recorded", event)
		}
	})
	t.Run("dirty-worktree refusal", func(t *testing.T) {
		engine, git, _, _, events := newTestEngine()
		git.clean = false
		if _, err := engine.Sync(context.Background(), baseOptions()); err == nil {
			t.Fatal("expected a refusal")
		}
		event := assertOneTerminalEvent(t, events, "refused")
		if event.Evidence["refusal_code"] != "dirty-worktree" {
			t.Errorf("event = %#v, want the refusal code recorded", event)
		}
	})
	t.Run("success", func(t *testing.T) {
		engine, _, _, _, events := newTestEngine()
		if _, err := engine.Sync(context.Background(), baseOptions()); err != nil {
			t.Fatal(err)
		}
		assertOneTerminalEvent(t, events, "success")
	})
}

func assertOneTerminalEvent(t *testing.T, events *fakeEvents, wantOutcome string) Event {
	t.Helper()
	terminal := events.withPhase("complete")
	if len(terminal) != 1 {
		t.Fatalf("terminal events = %#v, want exactly one per invocation", terminal)
	}
	if terminal[0].Outcome != wantOutcome {
		t.Fatalf("outcome = %q, want %q", terminal[0].Outcome, wantOutcome)
	}
	return terminal[0]
}
