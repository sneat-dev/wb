package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

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
	if !result.ReviewBound {
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
}

// #586: a file review naming a head, followed by ONLY a WB-produced
// update-branch merge (the target advances and WB brings the candidate up
// to date), still lands: the advance is proved, not a foreign change.
func TestLandFileReviewSurvivesOnlyAWBUpdateBranchMerge(t *testing.T) {
	// "feature" (no slash) matches the fixture's fake update-branch script,
	// which republishes the merge onto a hardcoded "refs/heads/feature"
	// when no "head-ref" state override is written (see pr_land_test.go's
	// update-branch case) — the same branch name
	// TestLandUpdatesABehindCandidateInsteadOfRefusing and friends use.
	fixture := newLandFixture(t, "feature", "main.go")
	// The review-stale proof needs a real local checkout of the branch to
	// run `git merge-tree`/ancestor checks against — the same requirement
	// --keep-commits already has (locateBranchCheckout). A real linked
	// worktree, not just the bare canonical push newLandFixture leaves on
	// main, is what makes it findable.
	if _, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projects, Operation: "file-update-branch",
		Branch: "feature", BranchChosen: true, Resume: true,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := dir + "/review.md"
	contents := "looks good\n\nReviewed-Head: " + fixture.headSHA + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	advanceLandTarget(t, fixture)

	options := landOptions(fixture)
	options.ApprovedBy = path
	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !fixtureHasMarker(fixture, "update-branch") {
		t.Fatal("expected the candidate to have been brought up to date via update-branch")
	}
	if !result.ReviewBound {
		t.Fatal("the review must still be recorded as bound")
	}
}

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
	if result.ReviewBound {
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
	if !result.ReviewBound || result.ReviewedHeadSHA != fixture.headSHA {
		t.Fatalf("ReviewBound = %v, ReviewedHeadSHA = %q, want bound to %q", result.ReviewBound, result.ReviewedHeadSHA, fixture.headSHA)
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
