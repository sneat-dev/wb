package orchestrate

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestPullRequestLandResumeGuidancePreservesUnaddressableTransientErrors(t *testing.T) {
	t.Parallel()
	for _, selector := range []string{"acme/app#not-a-number", "acme/app#", "https://github.com/acme/app/pull/invalid/files"} {
		t.Run(selector, func(t *testing.T) {
			t.Parallel()
			original := fmt.Errorf("owned observation: %w", githubobserver.ErrTransientRetriesExhausted)
			got := withPullRequestLandResumeGuidance(original, PullRequestLandOptions{Repository: "acme/app", PullRequest: selector}, PullRequestLandResult{})
			if got != original || !errors.Is(got, githubobserver.ErrTransientRetriesExhausted) {
				t.Fatalf("unaddressable selector rewrote original error: %v", got)
			}
		})
	}
}

func TestPullRequestLandResumeGuidanceKeepsPostedReviewAuthorityAndFileArguments(t *testing.T) {
	t.Parallel()
	const rawReview = "RAW_REVIEW_SECRET `unsafe` $(unsafe)"
	const reviewer = "opus@codex@owned-run"
	const reviewFile = "review's $(unsafe) `unsafe`.md"
	const postedURL = "https://github.com/acme/app/pull/7#issuecomment-42"
	for _, stage := range []string{"posted review", "file before posting", "literal before posting"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			original := fmt.Errorf("owned mutation: %w", githubobserver.ErrTransientMutationOutcomeUnknown)
			options := PullRequestLandOptions{Repository: "acme/app", PullRequest: "https://github.com/acme/app/pull/7/files", ApprovedBy: reviewer, ReviewComment: rawReview}
			result := PullRequestLandResult{}
			wantCommand := "wb pr land acme/app#7 --approved-by '" + reviewer + "' " + reviewCommentFilePlaceholder
			switch stage {
			case "posted review":
				options.ReviewCommentFile = reviewFile
				result.ReviewCommentURL = postedURL
				wantCommand = "wb pr land acme/app#7 --approved-by '" + postedURL + "'"
			case "file before posting":
				options.ReviewCommentFile = reviewFile
				wantCommand = "wb pr land acme/app#7 --approved-by '" + reviewer + "' --review-comment-file 'review'\\''s $(unsafe) `unsafe`.md'"
			}
			beforeOptions, beforeResult := options, result
			got := withPullRequestLandResumeGuidance(original, options, result)
			if got == nil || !errors.Is(got, original) || !errors.Is(got, githubobserver.ErrTransientMutationOutcomeUnknown) {
				t.Fatalf("guidance lost original error identity: %v", got)
			}
			message := got.Error()
			want := original.Error() + "; resumable: " + wantCommand
			if stage == "literal before posting" {
				want += "; the review text was never posted and is not echoed into this command (it may contain shell metacharacters) — replace " + reviewCommentFilePlaceholder + " with the real review file, or rerun with --review-comment \"<the review>\""
			}
			if message != want || strings.Contains(message, "RAW_REVIEW_SECRET") {
				t.Fatalf("guidance=%q want=%q", message, want)
			}
			if stage == "posted review" && (strings.Contains(message, reviewer) || strings.Contains(message, "--review-comment-file") || strings.Contains(message, reviewFile)) {
				t.Fatalf("posted review guidance carried obsolete reapproval material: %q", message)
			}
			if options.ApprovedBy != beforeOptions.ApprovedBy || options.ReviewComment != beforeOptions.ReviewComment || options.ReviewCommentFile != beforeOptions.ReviewCommentFile || result.ReviewCommentURL != beforeResult.ReviewCommentURL {
				t.Fatal("formatter changed caller-owned review material")
			}
		})
	}
}

func TestPullRequestLandResumeShellQuoteKeepsEachFreeTextValueLiteral(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ value, want string }{
		{"", "''"},
		{"plain", "'plain'"},
		{"a'b", "'a'\\''b'"},
		{"$(unsafe) `unsafe` $HOME; \"quoted\"", "'$(unsafe) `unsafe` $HOME; \"quoted\"'"},
		{"first\nsecond", "'first\nsecond'"},
	} {
		if got := shellSingleQuote(tc.value); got != tc.want {
			t.Errorf("quoted %q as %q, want %q", tc.value, got, tc.want)
		}
	}
}
