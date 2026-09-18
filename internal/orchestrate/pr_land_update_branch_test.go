package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureMarker(t *testing.T, fixture *landFixture, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.state, name), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixtureHasMarker(fixture *landFixture, name string) bool {
	_, err := os.Stat(filepath.Join(fixture.state, name))
	return err == nil
}

// TestLandArmsAutoMergeBeforeWaiting pins the ordering that makes every later
// failure survivable. Arming after the wait would protect only the ending that
// already reported itself; arming first means a killed host, an elapsed budget
// or a dead session all still land the change.
func TestLandArmsAutoMergeBeforeWaiting(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if !fixtureHasMarker(fixture, "auto-merge") {
		t.Fatalf("auto-merge was never armed; outcome = %s reason = %s", result.Outcome, result.Reason)
	}
	if !result.AutoMergeArmed {
		t.Errorf("AutoMergeArmed = false, want true so the receipt records who authorized the unattended merge")
	}
}

// TestLandSurvivesAnUnarmableAutoMerge proves arming is best effort. A
// repository with auto-merge disabled must still land normally: failing to set
// up the safety net is not a reason to refuse the work.
func TestLandSurvivesAnUnarmableAutoMerge(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	fixtureMarker(t, fixture, "auto-merge-unavailable")
	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s, want success; reason = %s", result.Outcome, result.Reason)
	}
	if result.AutoMergeArmed {
		t.Error("AutoMergeArmed = true although the mutation failed")
	}
	if !strings.Contains(result.Evidence["auto_merge"], "not armed") {
		t.Errorf("evidence did not record the failure: %q", result.Evidence["auto_merge"])
	}
}

// TestLandDoesNotArmAutoMergeWhenRefused honours the opt-out.
func TestLandDoesNotArmAutoMergeWhenRefused(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	options := landOptions(fixture)
	options.NoAutoMerge = true
	if _, err := LandPullRequest(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if fixtureHasMarker(fixture, "auto-merge") {
		t.Error("auto-merge was armed despite --no-auto-merge")
	}
}

// TestLandRefusesAConflictingUpdateWithoutWithdrawingAutoMerge covers the case
// the founder decided: a conflict is the author's to resolve, and the arming
// stays, so their resolution lands once CI passes against it. CI is the gate,
// not an approval recorded against a head that no longer exists.
func TestLandRefusesAConflictingUpdateWithoutWithdrawingAutoMerge(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	fixtureMarker(t, fixture, "update-conflict")
	advanceLandTarget(t, fixture)

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.RefusalCode != LandRefusalUpdateConflict {
		t.Fatalf("refusal = %q, want %q; reason = %s", result.RefusalCode, LandRefusalUpdateConflict, result.Reason)
	}
	if !result.AutoMergeArmed {
		t.Error("auto-merge was withdrawn on a conflict; it must stay armed so a pushed resolution lands once CI passes")
	}
	if !strings.Contains(result.SanctionedCommand, "resolve the conflict") {
		t.Errorf("refusal did not name what the author must do: %q", result.SanctionedCommand)
	}
}

func TestUpdateBranchConflictClassification(t *testing.T) {
	for reason, want := range map[string]bool{
		"update pull request branch: merge conflict between base and head": true,
		"update pull request branch: not mergeable":                        true,
		"update pull request branch: HTTP 403 forbidden":                   false,
		"update pull request branch: HTTP 502 bad gateway":                 false,
		"update pull request branch: HTTP 409: Conflict":                   false,
	} {
		if got := updateBranchConflict(reason); got != want {
			t.Errorf("updateBranchConflict(%q) = %v, want %v", reason, got, want)
		}
	}
}

// TestWaitDeadlineSpendsOneBudgetAcrossUpdates is the guard against a target
// that keeps advancing extending a landing forever. Updating mid-wait must
// spend the same budget, not restart it.
func TestWaitDeadlineSpendsOneBudgetAcrossUpdates(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	options := PullRequestLandOptions{Slice: 30 * time.Minute, Now: func() time.Time { return start }}
	deadline := waitDeadline(options)
	if got := deadline.Sub(start); got != 30*time.Minute {
		t.Fatalf("budget = %s, want 30m", got)
	}
	// A second call at a later moment must not push the deadline out; callers
	// compute it once, before the loop.
	later := PullRequestLandOptions{Slice: 30 * time.Minute, Now: func() time.Time { return start.Add(10 * time.Minute) }}
	if waitDeadline(later).Sub(start) != 40*time.Minute {
		t.Fatal("waitDeadline is relative to now, so it must be computed once before the loop")
	}
	if waitDeadline(PullRequestLandOptions{Now: func() time.Time { return start }}).Sub(start) != MaxForegroundCheckWaitSlice {
		t.Error("a zero budget did not fall back to the bounded slice")
	}
}

// advanceLandTarget moves the fixture's main forward so the candidate is
// genuinely behind, which is the state the update path exists for.
func advanceLandTarget(t *testing.T, fixture *landFixture) {
	t.Helper()
	tree := runEngineGit(t, fixture.remote, "rev-parse", "refs/heads/main^{tree}")
	parent := runEngineGit(t, fixture.remote, "rev-parse", "refs/heads/main")
	commit := runEngineGit(t, fixture.remote, "commit-tree", strings.TrimSpace(tree),
		"-p", strings.TrimSpace(parent), "-m", "another session landed first")
	runEngineGit(t, fixture.remote, "update-ref", "refs/heads/main", strings.TrimSpace(commit))
}

// TestLandUpdatesABehindCandidateInsteadOfRefusing is the headline behaviour.
// On a target with a strict up-to-date policy every candidate falls behind the
// moment anything else lands, and refusing then costs a manual merge plus a
// full fresh CI cycle. Measured 2026-09-18: three landings, three such round
// trips.
func TestLandUpdatesABehindCandidateInsteadOfRefusing(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	advanceLandTarget(t, fixture)

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if !fixtureHasMarker(fixture, "update-branch") {
		t.Fatalf("a behind candidate was not updated; outcome = %s refusal = %s reason = %s",
			result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s, want success after the update; reason = %s", result.Outcome, result.Reason)
	}
	// The receipt must name the head that actually merged, not the one read
	// before the update: the checks that authorized this were observed on the
	// updated head.
	if result.Evidence["updated_onto_target"] == "" {
		t.Error("receipt did not record that the candidate was brought up to date")
	}
}

// TestLandHonoursNoUpdateBranch keeps the historical refusal available.
func TestLandHonoursNoUpdateBranch(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	advanceLandTarget(t, fixture)
	options := landOptions(fixture)
	options.NoUpdateBranch = true

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if fixtureHasMarker(fixture, "update-branch") {
		t.Fatal("the candidate was updated despite --no-update-branch")
	}
	if result.Outcome == LandSuccess {
		t.Fatalf("a behind candidate landed with --no-update-branch; reason = %s", result.Reason)
	}
}

// TestLandDoesNotArmAutoMergeOnAnUnfencedTarget proves arming never skips the
// --allow-unfenced guard. GitHub's auto-merge enforces branch protection only;
// on a target without a strict up-to-date policy it would merge a head that was
// green once, which is the merge this verb refuses unless told otherwise.
func TestLandDoesNotArmAutoMergeOnAnUnfencedTarget(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	fixtureMarker(t, fixture, "unfenced")
	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if fixtureHasMarker(fixture, "auto-merge") || result.AutoMergeArmed {
		t.Fatal("auto-merge was armed on an unfenced target without --allow-unfenced")
	}
	if !strings.Contains(result.Evidence["auto_merge"], "no strict up-to-date policy") {
		t.Errorf("evidence did not name the skipped guard: %q", result.Evidence["auto_merge"])
	}
}

// TestLandArmsAutoMergeOnAnUnfencedTargetWhenAllowed proves --allow-unfenced
// is the one explicit choice that covers both the landing and the arming.
func TestLandArmsAutoMergeOnAnUnfencedTargetWhenAllowed(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	fixtureMarker(t, fixture, "unfenced")
	options := landOptions(fixture)
	options.AllowUnfenced = true
	if _, err := LandPullRequest(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if !fixtureHasMarker(fixture, "auto-merge") {
		t.Fatal("auto-merge was not armed although --allow-unfenced was explicit")
	}
}

// TestAutoMergeNeverArmsForKeptCommits: --keep-commits merges a branch WB
// rebuilds, so an armed auto-merge would let GitHub squash the original.
func TestAutoMergeNeverArmsForKeptCommits(t *testing.T) {
	options := PullRequestLandOptions{KeepCommits: []string{"4f2a1c9"}, AllowUnfenced: true}
	if got := autoMergeBypassesAGuard(context.Background(), options, "main"); !strings.Contains(got, "--keep-commits") {
		t.Fatalf("autoMergeBypassesAGuard = %q, want the --keep-commits guard named", got)
	}
}

// TestLandTreatsAGitHubMergeDuringTheWaitAsALanding is the normal success path
// once auto-merge is armed: GitHub merges within seconds of green, usually
// before WB's confirming observation, and the wait then sees the target move
// past the head. Reporting that as checks-failed would call a landing a
// failure and skip the sync, branch deletion and cleanup.
func TestLandTreatsAGitHubMergeDuringTheWaitAsALanding(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	fixtureMarker(t, fixture, "github-merges-on-arm")
	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.Evidence["merged_by"] != "github auto-merge" {
		t.Errorf("merged_by = %q, want the merge attributed to GitHub", result.Evidence["merged_by"])
	}
	if !result.LandingOnBase {
		t.Error("the GitHub merge was not verified on the base")
	}
}

// TestLandArmsWithTheObservedHeadAndWBsMessage: GitHub merges with what it
// was armed with, so the arming must carry WB's subject and be pinned to the
// head WB observed.
func TestLandArmsWithTheObservedHeadAndWBsMessage(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	head := fixture.readState(t, "head")
	if _, err := LandPullRequest(context.Background(), landOptions(fixture)); err != nil {
		t.Fatal(err)
	}
	args := fixture.readState(t, "auto-merge-args")
	for _, want := range []string{"expectedHeadOid", "head=" + head, "subject=feat: the change (#7)"} {
		if !strings.Contains(args, want) {
			t.Errorf("arming arguments lack %q: %s", want, args)
		}
	}
}

// TestLandUpdatesWhenTheTargetMovesDuringTheWait: the wait reports a target
// that advanced past the head as a failure. With updating allowed that is not
// a verdict on the work, so the verb updates and waits again.
func TestLandUpdatesWhenTheTargetMovesDuringTheWait(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	fixtureMarker(t, fixture, "advance-on-checks")
	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if !fixtureHasMarker(fixture, "update-branch") {
		t.Fatalf("the candidate was not updated after the target moved; outcome = %s (%s): %s",
			result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
}

// TestLandTreatsABudgetShorterThanOnePollAsSpent: a budget that cannot hold
// one observation is spent, which is pending, never an error.
func TestLandTreatsABudgetShorterThanOnePollAsSpent(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	options := landOptions(fixture)
	options.CheckPollInterval = time.Minute
	options.Slice = 30 * time.Second
	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatalf("a short budget must end pending, not in an error: %v", err)
	}
	if result.RefusalCode != LandRefusalChecksPending {
		t.Fatalf("refusal = %s, want %s: %s", result.RefusalCode, LandRefusalChecksPending, result.Reason)
	}
}

// TestLandChecksPendingResumeCarriesATimeoutFloor pins #584: re-running the
// printed resume command with no --timeout at all gets another bite of the
// same short-lived default and cannot converge. With auto-merge disabled
// (so the plain resume command is printed, rather than the auto-merge-armed
// "GitHub lands it" alternative) a budget below the recommended floor must
// have the resume command carry that floor instead of the budget that just
// ran out.
func TestLandChecksPendingResumeCarriesATimeoutFloor(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	options := landOptions(fixture)
	options.CheckPollInterval = time.Minute
	options.Slice = 30 * time.Second
	options.NoAutoMerge = true
	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatalf("a short budget must end pending, not in an error: %v", err)
	}
	if result.RefusalCode != LandRefusalChecksPending {
		t.Fatalf("refusal = %s, want %s: %s", result.RefusalCode, LandRefusalChecksPending, result.Reason)
	}
	// --keep (this fixture's default) and --no-auto-merge (set by this test)
	// are both carried through: resuming without them would silently revert
	// behavior the original invocation explicitly chose.
	want := "wb pr land " + options.Repository + "#" + "7" + " --timeout 45m --keep --no-auto-merge"
	if result.SanctionedCommand != want {
		t.Fatalf("checks-pending resume command = %q, want %q", result.SanctionedCommand, want)
	}
	if !strings.Contains(result.Reason, "run the resume command in the background") {
		t.Fatalf("reason must say to run the resume command in the background: %q", result.Reason)
	}
}

// TestPullRequestLandResumeCommandCarriesOriginalFlagsQuoted pins #584: the
// resume command must reproduce every flag that changed this invocation's
// behavior - --timeout, --no-auto-merge, --allow-unfenced, --approved-by,
// --keep-commits/--reason - with free-text values POSIX single-quoted
// (round 4: shellSingleQuote, not strconv.Quote, is this function's
// convention for --reason/--subject/--approved-by) so a review string
// containing a space, a double quote, a backtick, or "$" cannot break the
// printed command, be misread as a second flag, or shell-expand on
// copy-paste.
func TestPullRequestLandResumeCommandCarriesOriginalFlagsQuoted(t *testing.T) {
	t.Parallel()
	got := pullRequestLandResumeCommand(PullRequestLandOptions{
		Repository:    "acme/app",
		NoAutoMerge:   true,
		AllowUnfenced: true,
		ApprovedBy:    `review with a space and a ' quote`,
		KeepCommits:   []string{"abc123", "def456"},
		Reason:        "kept for audit",
	}, "9", "45m")
	want := `wb pr land acme/app#9 --timeout 45m --no-auto-merge --allow-unfenced --keep-commits abc123,def456 --reason 'kept for audit' --approved-by 'review with a space and a '\'' quote'`
	if got != want {
		t.Fatalf("pullRequestLandResumeCommand = %q, want %q", got, want)
	}
}

// TestPRLandResumeTimeoutFlagNamesAConvergingBudget pins #584: the
// checks-pending resume command must carry --timeout with a budget that can
// actually succeed, never the caller's own just-exhausted budget verbatim
// when that budget was too small to converge.
func TestPRLandResumeTimeoutFlagNamesAConvergingBudget(t *testing.T) {
	tests := []struct {
		name     string
		inEffect time.Duration
		want     string
	}{
		{"below the floor uses the recommended floor", 8 * time.Minute, "45m"},
		{"at the floor is kept", 45 * time.Minute, "45m"},
		{"above the floor is carried through", 90 * time.Minute, "90m"},
		{"a non-whole-minute duration falls back to the standard rendering", 90*time.Minute + 30*time.Second, "1h30m30s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prLandResumeTimeoutFlag(tt.inEffect); got != tt.want {
				t.Errorf("prLandResumeTimeoutFlag(%s) = %q, want %q", tt.inEffect, got, tt.want)
			}
		})
	}
}

// TestTargetMovedClassification pins which wait failures mean the target
// moved, and which update failures mean the head moved.
func TestTargetMovedClassification(t *testing.T) {
	for reason, want := range map[string]bool{
		"pull request head abc does not contain current target main at def; rebase": true,
		"target main advanced after checks passed from a to b; rebase":              true,
		"required check CI concluded failure":                                       false,
	} {
		if got := targetMovedUnderHead(reason); got != want {
			t.Errorf("targetMovedUnderHead(%q) = %v, want %v", reason, got, want)
		}
	}
	if !updateBranchHeadMoved("update: expected head sha didn't match current head ref") {
		t.Error("a compare-and-swap miss was not recognised")
	}
}
