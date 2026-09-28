package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// reviewBound reads PullRequestLandResult.ReviewBound's tri-state pointer
// (nil = no review applied, *false = applied but not bound, *true = bound)
// as a plain bool for tests that only care about the bound/not-bound case.
func reviewBound(result PullRequestLandResult) bool {
	return result.ReviewBound != nil && *result.ReviewBound
}

// jsonEscapedBody renders text the way the fixture's fake `gh` embeds it
// into a JSON string: escaped, but without the surrounding quotes, which
// the shell script adds itself.
func jsonEscapedBody(text string) string {
	encoded, _ := json.Marshal(text)
	return strings.TrimSuffix(strings.TrimPrefix(string(encoded), `"`), `"`)
}

// #604: the identity form posts the review as a PR comment naming the
// reviewer and the exact head, and records the receipt fields.
func TestLandIdentityReviewPostsCommentAndRecordsReceipt(t *testing.T) {
	fixture := newLandFixture(t, "feature/identity", "main.go")
	options := landOptions(fixture)
	options.ApprovedBy = "opus@codex@run-42"
	options.ReviewComment = "looks correct; the diff is scoped to main.go only"

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.Reviewer != "opus@codex@run-42" {
		t.Fatalf("Reviewer = %q", result.Reviewer)
	}
	if result.ReviewedHeadSHA == "" {
		t.Fatal("ReviewedHeadSHA must be recorded")
	}
	if result.ReviewDigest != ReviewDigest(options.ReviewComment) {
		t.Fatalf("ReviewDigest = %q", result.ReviewDigest)
	}
	if result.ReviewCommentURL == "" || result.ApprovedBy != result.ReviewCommentURL {
		t.Fatalf("ReviewCommentURL/ApprovedBy = %q / %q", result.ReviewCommentURL, result.ApprovedBy)
	}
	posted := fixture.readState(t, "posted-comments")
	if !strings.Contains(posted, "issues/7/comments") {
		t.Fatalf("no comment was posted: %q", posted)
	}
	if result.SelfReview {
		t.Fatal("an undeclared invoking session must not match a declared reviewer identity")
	}
}

// Round 3, B4: the identity form's comment is posted exactly once, and only
// after the preflight cleanup check passes — a landing that refuses on the
// preflight must never have posted a comment for a landing that did not
// happen.
func TestLandIdentityReviewPostsCommentOnlyOncePostPreflight(t *testing.T) {
	fixture := newLandFixture(t, "feature/identity-preflight", "main.go")
	options := landOptions(fixture)
	options.ApprovedBy = "opus@codex@run-42"
	options.ReviewComment = "looks correct"

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	posted := fixture.readState(t, "posted-comments")
	if strings.Count(posted, "issues/7/comments") != 1 {
		t.Fatalf("the comment must be posted exactly once: %q", posted)
	}
}

// Round 3, B4: a preflight refusal must never have posted the identity
// form's comment — the landing did not happen, so nothing was reviewed yet.
func TestLandIdentityReviewDoesNotPostCommentWhenPreflightRefuses(t *testing.T) {
	fixture := newLandFixture(t, "feature/identity-preflight-blocked", "main.go")
	created, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projects, Operation: "identity-preflight-blocked",
		Branch: "feature/identity-preflight-blocked", BranchChosen: true, Resume: true,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "wip.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	options := landOptions(fixture)
	options.Keep = false
	options.ApprovedBy = "opus@codex@run-42"
	options.ReviewComment = "looks correct"

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != "cleanup-blocked-dirty" {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if fixture.readState(t, "posted-comments") != "" {
		t.Fatal("no comment must be posted when the preflight refuses the landing")
	}
}

// Round 3, B4: once the identity form's comment is posted, every resume /
// sanctioned command built from here on must carry the posted comment's
// URL, and never the identity + review text again — a copy-run of it must
// not post a second comment, and the review text (which may contain "$" or
// a backtick) must never appear in a command line at all.
func TestLandChecksPendingResumeCommandCarriesPostedCommentURLNotReviewText(t *testing.T) {
	fixture := newLandFixture(t, "feature/identity-resume", "main.go")
	options := landOptions(fixture)
	// A budget no longer than one poll ends pending without any GitHub
	// checks call at all - the same technique
	// TestLandChecksPendingResumeCarriesATimeoutFloor uses. NoAutoMerge
	// keeps the printed command a resumable `wb pr land ...` rather than
	// the auto-merge-armed "GitHub lands it" alternative.
	options.CheckPollInterval = time.Minute
	options.Slice = 30 * time.Second
	options.NoAutoMerge = true
	options.ApprovedBy = "opus@codex@run-42"
	options.ReviewComment = "looks good; do not run `rm -rf $HOME` please"

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandFindings || result.RefusalCode != LandRefusalChecksPending {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.ReviewCommentURL == "" {
		t.Fatal("the comment must have been posted before the pending result")
	}
	if !strings.Contains(result.SanctionedCommand, result.ReviewCommentURL) {
		t.Fatalf("SanctionedCommand = %q, want it to carry the posted comment URL %q", result.SanctionedCommand, result.ReviewCommentURL)
	}
	if strings.Contains(result.SanctionedCommand, "looks good") || strings.Contains(result.SanctionedCommand, "rm -rf") {
		t.Fatalf("SanctionedCommand = %q, must never echo the review text", result.SanctionedCommand)
	}
	if strings.Contains(result.SanctionedCommand, "opus@codex@run-42") {
		t.Fatalf("SanctionedCommand = %q, must not carry the original identity once a comment was posted", result.SanctionedCommand)
	}
}

// #604: the identity form needs a non-empty review comment.
func TestLandIdentityReviewEmptyCommentRefused(t *testing.T) {
	fixture := newLandFixture(t, "feature/empty-review", "main.go")
	options := landOptions(fixture)
	options.ApprovedBy = "opus@codex@run-42"

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalReviewCommentEmpty {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if fixture.readState(t, "posted-comments") != "" {
		t.Fatal("no comment must be posted when the review text is empty")
	}
}

// #604: a partly given identity is filled from the environment
// ($CLAUDE_CODE_SESSION_ID gives harness claude-code and the session).
func TestLandIdentityReviewFillsIdentityFromEnvironment(t *testing.T) {
	fixture := newLandFixture(t, "feature/env-fill", "main.go")
	t.Setenv(envClaudeCodeSessionID, "session-xyz")
	options := landOptions(fixture)
	options.ApprovedBy = "sonnet"
	options.ReviewComment = "looks good"

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.Reviewer != "sonnet@claude-code@session-xyz" {
		t.Fatalf("Reviewer = %q, want env-filled harness+session", result.Reviewer)
	}
}

// #604: whatever nothing determines is recorded as "unknown", never guessed.
func TestLandIdentityReviewUndeterminablePartsAreUnknown(t *testing.T) {
	fixture := newLandFixture(t, "feature/undetermined", "main.go")
	t.Setenv(envClaudeCodeSessionID, "")
	options := landOptions(fixture)
	options.ApprovedBy = "sonnet"
	options.ReviewComment = "looks good"

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.Reviewer != "sonnet@unknown@unknown" {
		t.Fatalf("Reviewer = %q, want undeterminable parts recorded as unknown", result.Reviewer)
	}
}

// #604: self_review is true only when the reviewer identity fully matches
// the invoking session's own declared identity.
func TestLandIdentityReviewSelfReviewOnlyOnFullMatch(t *testing.T) {
	fixture := newLandFixture(t, "feature/self-review", "main.go")
	t.Setenv(worktrees.EnvAgentRuntime, "codex")
	t.Setenv(worktrees.EnvAgentModel, "opus")
	t.Setenv(worktrees.EnvSessionID, "run-42")
	options := landOptions(fixture)
	options.ApprovedBy = "opus@codex@run-42"
	options.ReviewComment = "self-approved, matches this session"

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !result.SelfReview {
		t.Fatal("a full-triple match against the invoking session must be self_review")
	}
}

// Back-compatibility: URL and file forms still work exactly as before #604.
func TestLandBackCompatFileApprovalStillWorks(t *testing.T) {
	fixture := newLandFixture(t, "feature/file-approval", "main.go")
	dir := t.TempDir()
	path := dir + "/review.md"
	if err := os.WriteFile(path, []byte("looks good"), 0o644); err != nil {
		t.Fatal(err)
	}
	options := landOptions(fixture)
	options.ApprovedBy = path

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.ApprovedBy != path {
		t.Fatalf("ApprovedBy = %q, want the file path preserved verbatim", result.ApprovedBy)
	}
	if fixture.readState(t, "posted-comments") != "" {
		t.Fatal("the file form must not post a PR comment")
	}
}

// #586 (founder-decided 2026-09-18: warn, still land): a file review that
// names the head it reviewed via a "Reviewed-Head: <sha>" line lands when
// the current head is exactly that one.
func TestLandFileReviewWithMatchingReviewedHeadLands(t *testing.T) {
	fixture := newLandFixture(t, "feature/file-matching-head", "main.go")
	dir := t.TempDir()
	path := dir + "/review.md"
	contents := "looks good\n\nReviewed-Head: " + fixture.headSHA + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	options := landOptions(fixture)
	options.ApprovedBy = path

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !reviewBound(result) {
		t.Fatal("a review naming its head must be recorded as bound")
	}
	if result.ReviewedHeadSHA != fixture.headSHA {
		t.Fatalf("ReviewedHeadSHA = %q, want %q", result.ReviewedHeadSHA, fixture.headSHA)
	}
	if result.Evidence["review"] != "" {
		t.Fatalf("a bound review must not carry the unbound finding: %q", result.Evidence["review"])
	}
}

// #586: a file review naming a head, followed by a foreign push (a
// SEPARATE, later `wb pr land` invocation observes the new head), refuses
// with review-stale rather than landing an unreviewed change.
func TestLandFileReviewStaleAfterForeignPushInASeparateInvocation(t *testing.T) {
	fixture := newLandFixture(t, "feature/file-foreign-push", "main.go")
	dir := t.TempDir()
	path := dir + "/review.md"
	contents := "looks good\n\nReviewed-Head: " + fixture.headSHA + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	// A foreign push lands a new commit on the PR branch after the review
	// was written, in the remote the fixture's fake `gh` reads from.
	foreignHead := fixture.pushForeignCommit(t, "feature/file-foreign-push")

	options := landOptions(fixture)
	options.ApprovedBy = path
	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalReviewStale {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.HeadSHA != foreignHead {
		t.Fatalf("HeadSHA = %q, want the foreign-pushed head %q", result.HeadSHA, foreignHead)
	}
	if fixture.readState(t, "merged") != "false" {
		t.Fatal("a review-stale refusal must not have merged anything")
	}
	// Round 3, B3: a review-stale refusal must be reached BEFORE auto-merge
	// is armed — armed then refused would still leave GitHub free to merge
	// the un-reviewed head on green without WB.
	if fixtureHasMarker(fixture, "auto-merge") {
		t.Fatal("a review-stale refusal must be reached before auto-merge is armed, not after")
	}
}

// Round 3, B3: the same review-stale case as above, asserted explicitly
// against the required test name/shape: a named review that is already
// stale relative to the observed head must refuse before ever arming
// auto-merge — never arm, then discover staleness, then refuse.
func TestLandReviewStaleRefusedBeforeAutoMergeIsArmed(t *testing.T) {
	fixture := newLandFixture(t, "feature/stale-before-arm", "main.go")
	dir := t.TempDir()
	path := dir + "/review.md"
	contents := "looks good\n\nReviewed-Head: " + fixture.headSHA + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	foreignHead := fixture.pushForeignCommit(t, "feature/stale-before-arm")

	options := landOptions(fixture)
	options.ApprovedBy = path
	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalReviewStale {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.HeadSHA != foreignHead {
		t.Fatalf("HeadSHA = %q, want the foreign-pushed head %q", result.HeadSHA, foreignHead)
	}
	if fixtureHasMarker(fixture, "auto-merge") {
		t.Fatal("auto-merge must never be armed for a head a review-stale refusal blocked before arming")
	}
	if result.AutoMergeArmed {
		t.Fatal("AutoMergeArmed must be false: arming never happened")
	}
}

// TestLandFileReviewSurvivesOnlyAWBUpdateBranchMerge moved to
// pr_land_review_e2e_test.go (spec/plans/coverage-to-100 task-17): its own
// doc comment already said the review-stale proof "needs a real local
// checkout... to run git merge-tree/ancestor checks against", and that
// proof now resolves through PullRequestLandOptions' git seam (pr_land.go),
// which task-24's runtime guard blocks outside the e2e tier. Moving it,
// rather than calling runnertest.AllowRealProcess in the default tier,
// keeps internal/quality/testdata/unit_tier.pending's cross-PR total from
// rising: this file carries zero pending entries today.

// #586: a file review with no "Reviewed-Head:" line still lands, with the
// informational review-unbound finding — never a refusal.
func TestLandFileReviewWithNoReviewedHeadLandsUnbound(t *testing.T) {
	fixture := newLandFixture(t, "feature/file-unbound", "main.go")
	dir := t.TempDir()
	path := dir + "/review.md"
	if err := os.WriteFile(path, []byte("looks good, no head named"), 0o644); err != nil {
		t.Fatal(err)
	}
	options := landOptions(fixture)
	options.ApprovedBy = path

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if reviewBound(result) {
		t.Fatal("a review naming no head must not be recorded as bound")
	}
	if !strings.Contains(result.Evidence["review"], "review-unbound") {
		t.Fatalf("Evidence[review] = %q, want the review-unbound finding", result.Evidence["review"])
	}
}

// #586: a URL review pointing at a GitHub PR/issue comment is fetched
// through the gh fake and its Reviewed-Head line is parsed the same way.
func TestLandURLReviewParsesReviewedHeadFromFetchedComment(t *testing.T) {
	fixture := newLandFixture(t, "feature/url-comment", "main.go")
	fixture.writeState(t, "comment-body", jsonEscapedBody("looks good\n\nReviewed-Head: "+fixture.headSHA+"\n"))

	options := landOptions(fixture)
	options.ApprovedBy = "https://github.com/acme/app/pull/7#issuecomment-1"
	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !reviewBound(result) || result.ReviewedHeadSHA != fixture.headSHA {
		t.Fatalf("ReviewBound = %v, ReviewedHeadSHA = %q, want bound to %q", reviewBound(result), result.ReviewedHeadSHA, fixture.headSHA)
	}
}

// Round 3, minor 2: a comment URL naming a different repository or a
// different pull/issue than the one being landed must be refused, never
// silently fetched and trusted.
func TestLandURLReviewFromADifferentRepositoryIsRefused(t *testing.T) {
	fixture := newLandFixture(t, "feature/url-cross-repo", "main.go")
	fixture.writeState(t, "comment-body", jsonEscapedBody("looks good\n\nReviewed-Head: "+fixture.headSHA+"\n"))

	options := landOptions(fixture)
	options.ApprovedBy = "https://github.com/other/repo/pull/7#issuecomment-1"
	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalReviewCommentCrossRepo {
		t.Fatalf("outcome = %s (%s): %s, want %s", result.Outcome, result.RefusalCode, result.Reason, LandRefusalReviewCommentCrossRepo)
	}
}

// Round 3, minor 1: a "Reviewed-Head:" line whose value is not a full
// 40-character SHA is a parse error, refused with a clear reason — never
// silently treated as a permanently-unmatchable reviewed head that can
// never again equal a real current head.
func TestLandFileReviewWithShortReviewedHeadGivesParseError(t *testing.T) {
	fixture := newLandFixture(t, "feature/short-sha", "main.go")
	dir := t.TempDir()
	path := dir + "/review.md"
	contents := "looks good\n\nReviewed-Head: " + fixture.headSHA[:8] + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	options := landOptions(fixture)
	options.ApprovedBy = path

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalReviewHeadMalformed {
		t.Fatalf("outcome = %s (%s): %s, want %s", result.Outcome, result.RefusalCode, result.Reason, LandRefusalReviewHeadMalformed)
	}
}

// #604: the identity form's posted comment carries the exact machine-readable
// "Reviewed-Head: <full sha>" line.
func TestLandIdentityReviewCommentContainsExactReviewedHeadLine(t *testing.T) {
	fixture := newLandFixture(t, "feature/identity-reviewed-head", "main.go")
	options := landOptions(fixture)
	options.ApprovedBy = "opus@codex@run-1"
	options.ReviewComment = "looks good"

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	posted := fixture.readState(t, "posted-comments")
	if !strings.Contains(posted, "Reviewed-Head: "+result.ReviewedHeadSHA) {
		t.Fatalf("posted comment = %q, missing the exact Reviewed-Head line for %q", posted, result.ReviewedHeadSHA)
	}
}

// The mechanical classifier is decided from the diff regardless of
// --approved-by: a mechanical bump given an identity approval is still
// mechanical, and the identity/comment machinery never runs for it.
func TestLandMechanicalClassifierStillRunsWithApprovedBy(t *testing.T) {
	fixture := newLandFixture(t, "bump/with-approval", "go.mod", "go.sum")
	options := landOptions(fixture)
	options.ApprovedBy = "opus@codex@run-1"
	options.ReviewComment = "unnecessary but supplied anyway"

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess || !result.Mechanical {
		t.Fatalf("outcome = %s (%s), mechanical = %v", result.Outcome, result.RefusalCode, result.Mechanical)
	}
	if result.Reviewer != "" || result.ReviewCommentURL != "" {
		t.Fatalf("a mechanical landing must not post a review comment: reviewer=%q url=%q", result.Reviewer, result.ReviewCommentURL)
	}
	if fixture.readState(t, "posted-comments") != "" {
		t.Fatal("a mechanical bump must not post a PR comment even when --approved-by is given")
	}
}

// #615: `wb pr land` reports the linked issues GitHub's own
// closingIssuesReferences names, and the informational "no linked issue"
// finding when there are none.
func TestLandReportsClosesFinding(t *testing.T) {
	fixture := newLandFixture(t, "feature/closes", "go.mod", "go.sum")
	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s: %s", result.Outcome, result.Reason)
	}
	if len(result.Closes) != 0 {
		t.Fatalf("Closes = %v, want none", result.Closes)
	}
	if result.Evidence["closes"] != "no linked issue" {
		t.Fatalf("Evidence[closes] = %q, want the informational finding", result.Evidence["closes"])
	}
}

func TestLandReportsClosesFindingWithLinkedIssues(t *testing.T) {
	fixture := newLandFixture(t, "feature/closes-linked", "go.mod", "go.sum")
	fixture.writeState(t, "closes", `[{"number":591},{"number":614}]`)
	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s: %s", result.Outcome, result.Reason)
	}
	if len(result.Closes) != 2 || result.Closes[0] != 591 || result.Closes[1] != 614 {
		t.Fatalf("Closes = %v", result.Closes)
	}
	if result.Evidence["closes"] != "closes: #591, #614" {
		t.Fatalf("Evidence[closes] = %q", result.Evidence["closes"])
	}
}
