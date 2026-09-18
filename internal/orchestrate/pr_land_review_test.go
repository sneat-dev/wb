package orchestrate

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

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
	if result.ReviewedHeadSHA == "" {
		t.Fatal("a back-compat approval must still bind a reviewed head")
	}
	if fixture.readState(t, "posted-comments") != "" {
		t.Fatal("the file form must not post a PR comment")
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
