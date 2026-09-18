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
