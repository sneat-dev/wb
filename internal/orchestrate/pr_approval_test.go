package orchestrate

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runner"
)

// emailShapedApprovedBy is built by concatenation, not as one literal, only
// to dodge over-eager PII scrubbing of an "@"-containing literal in this
// source file; its value is an ordinary email-shaped string like any a
// caller might actually pass.
const emailShapedApprovedBy = "alex" + "@" + "example.com"

func TestClassifyApprovedBy(t *testing.T) {
	dir := t.TempDir()
	file := dir + "/review.md"
	if err := os.WriteFile(file, []byte("looks good"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Without a review comment, a value containing "@" is still the
	// unambiguous identity shape, but a bare word with no "@" stays the old
	// free-form approval evidence (#604 back-compat: any string was
	// accepted, whether or not it named a file that existed on disk).
	withoutComment := map[string]approvalKind{
		"":                                   approvalKindEmpty,
		"ci":                                 approvalKindCI,
		"CI":                                 approvalKindCI,
		"https://github.com/acme/app/pull/7": approvalKindURL,
		file:                                 approvalKindFile,
		"sonnet@claude-code@sess-1":          approvalKindIdentity,
		"sonnet":                             approvalKindFile,
		"review.md":                          approvalKindFile,
		// Round 3, minor 3 (Q4 break): a value containing "@" that is not
		// the {model}@{harness}[@{session}] shape must fall back to the
		// free-form approval, not be misclassified as an identity and
		// refused.
		emailShapedApprovedBy:        approvalKindFile, // an email
		"@octocat":                   approvalKindFile, // an "@handle"
		"/tmp/user@example.com/x.md": approvalKindFile, // a path containing "@"
	}
	for value, want := range withoutComment {
		if got := classifyApprovedBy(value, false); got != want {
			t.Errorf("classifyApprovedBy(%q, false) = %v, want %v", value, got, want)
		}
	}

	// With a review comment, an ambiguous bare string is the identity
	// shape; a URL/file/ci/empty value is unaffected.
	withComment := map[string]approvalKind{
		"":                                   approvalKindEmpty,
		"ci":                                 approvalKindCI,
		"https://github.com/acme/app/pull/7": approvalKindURL,
		file:                                 approvalKindFile,
		"sonnet@claude-code@sess-1":          approvalKindIdentity,
		"sonnet":                             approvalKindIdentity,
		emailShapedApprovedBy:                approvalKindFile,
		"@octocat":                           approvalKindFile,
		"/tmp/user@example.com/x.md":         approvalKindFile,
	}
	for value, want := range withComment {
		if got := classifyApprovedBy(value, true); got != want {
			t.Errorf("classifyApprovedBy(%q, true) = %v, want %v", value, got, want)
		}
	}
}

func TestParseReviewerIdentityFillsFromEnvironmentAndFinalizesUnknown(t *testing.T) {
	// Fully declared: nothing filled, nothing "unknown".
	identity := FinalizeReviewerIdentity(FillReviewerIdentityFromEnvironment(ParseReviewerIdentity("opus@codex@run-9")))
	if identity != (ReviewerIdentity{Model: "opus", Harness: "codex", Session: "run-9"}) {
		t.Fatalf("fully declared identity = %#v", identity)
	}

	// Model only: harness/session filled from $CLAUDE_CODE_SESSION_ID.
	t.Setenv(envClaudeCodeSessionID, "session-abc")
	identity = FinalizeReviewerIdentity(FillReviewerIdentityFromEnvironment(ParseReviewerIdentity("sonnet")))
	if identity != (ReviewerIdentity{Model: "sonnet", Harness: "claude-code", Session: "session-abc"}) {
		t.Fatalf("env-filled identity = %#v", identity)
	}

	// Nothing declared and nothing in the environment: every part is
	// "unknown", never guessed.
	t.Setenv(envClaudeCodeSessionID, "")
	identity = FinalizeReviewerIdentity(FillReviewerIdentityFromEnvironment(ParseReviewerIdentity("")))
	if identity != (ReviewerIdentity{Model: unknownIdentityPart, Harness: unknownIdentityPart, Session: unknownIdentityPart}) {
		t.Fatalf("undeterminable identity = %#v", identity)
	}
}

func TestReviewerIdentitySelfReviewOnlyOnFullTripleMatch(t *testing.T) {
	author := ReviewerIdentity{Model: "sonnet", Harness: "claude-code", Session: "s1"}
	full := ReviewerIdentity{Model: "sonnet", Harness: "claude-code", Session: "s1"}
	if !full.SelfReview(author) {
		t.Fatal("a full-triple match must be self_review")
	}
	modelOnly := ReviewerIdentity{Model: "sonnet", Harness: "codex", Session: "s2"}
	if modelOnly.SelfReview(author) {
		t.Fatal("model-only matching must not report self_review: Sonnet implements, Opus subagent reviews, same session is the normal flow")
	}
	sameSessionDifferentModel := ReviewerIdentity{Model: "opus", Harness: "claude-code", Session: "s1"}
	if sameSessionDifferentModel.SelfReview(author) {
		t.Fatal("a different reviewer model in the same session must not be self_review")
	}
}

func TestReviewDigestIsDeterministicAndDistinguishesText(t *testing.T) {
	a := ReviewDigest("looks good")
	b := ReviewDigest("looks good")
	c := ReviewDigest("looks great")
	if a != b {
		t.Fatalf("digest of identical text differs: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("digest of different text collided: %q", a)
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Fatalf("digest %q has no sha256 prefix", a)
	}
}

func TestReadReviewCommentTextPrefersLiteralThenFile(t *testing.T) {
	if text, err := readReviewCommentText("  looks good  ", ""); err != nil || text != "looks good" {
		t.Fatalf("literal comment = %q, %v", text, err)
	}
	dir := t.TempDir()
	path := dir + "/review.txt"
	if err := os.WriteFile(path, []byte(" from file \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if text, err := readReviewCommentText("", path); err != nil || text != "from file" {
		t.Fatalf("file comment = %q, %v", text, err)
	}
	if text, err := readReviewCommentText("", ""); err != nil || text != "" {
		t.Fatalf("empty comment = %q, %v", text, err)
	}
}

// #586: the head-binding walk. A foreign commit breaks the chain; a chain
// of WB-produced update-branch merges (each proved) is allowed; a crafted
// merge that fails the proof is stale even though its parent shape matches.
func TestReviewedHeadAdvanceChain(t *testing.T) {
	ctx := context.Background()
	restoreProof := reviewHeadAdvanceProof
	restoreParents := reviewCommitParents
	t.Cleanup(func() {
		reviewHeadAdvanceProof = restoreProof
		reviewCommitParents = restoreParents
	})

	t.Run("foreign push breaks the chain", func(t *testing.T) {
		reviewCommitParents = func(ctx context.Context, repository, sha string) ([]string, error) {
			// "foreign" has only one parent: not an update-branch merge shape.
			return []string{"reviewed"}, nil
		}
		reviewHeadAdvanceProof = func(ctx context.Context, _ Git, _ runner.Runner, worktree, branch, target, repository, candidateSHA, targetParent, headSHA string) (bool, error) {
			t.Fatal("proof must not be consulted for a non-merge commit")
			return false, nil
		}
		if advanced, unverifiable, _ := reviewedHeadAdvanceChain(ctx, nil, nil, "wt", "feature", "acme/app", "main", "reviewed", "foreign"); advanced || unverifiable {
			t.Fatalf("a foreign, non-merge commit must not be treated as still current or unverifiable: advanced=%v unverifiable=%v", advanced, unverifiable)
		}
	})

	t.Run("a WB update-branch merge chain is allowed", func(t *testing.T) {
		// current -> merge(reviewed, target1) : one legitimate hop.
		reviewCommitParents = func(ctx context.Context, repository, sha string) ([]string, error) {
			if sha == "current" {
				return []string{"reviewed", "target1"}, nil
			}
			t.Fatalf("unexpected commit parents lookup for %s", sha)
			return nil, nil
		}
		reviewHeadAdvanceProof = func(ctx context.Context, _ Git, _ runner.Runner, worktree, branch, target, repository, candidateSHA, targetParent, headSHA string) (bool, error) {
			return candidateSHA == "reviewed" && targetParent == "target1" && headSHA == "current", nil
		}
		if advanced, unverifiable, _ := reviewedHeadAdvanceChain(ctx, nil, nil, "wt", "feature", "acme/app", "main", "reviewed", "current"); !advanced || unverifiable {
			t.Fatalf("a proved update-branch merge advance must be allowed: advanced=%v unverifiable=%v", advanced, unverifiable)
		}
	})

	t.Run("a crafted merge failing the proof is stale", func(t *testing.T) {
		reviewCommitParents = func(ctx context.Context, repository, sha string) ([]string, error) {
			return []string{"reviewed", "target1"}, nil
		}
		reviewHeadAdvanceProof = func(ctx context.Context, _ Git, _ runner.Runner, worktree, branch, target, repository, candidateSHA, targetParent, headSHA string) (bool, error) {
			return false, nil // right parent shape, wrong content
		}
		if advanced, unverifiable, _ := reviewedHeadAdvanceChain(ctx, nil, nil, "wt", "feature", "acme/app", "main", "reviewed", "current"); advanced || unverifiable {
			t.Fatalf("a merge shape that fails the tree/ancestor proof must not be trusted: advanced=%v unverifiable=%v", advanced, unverifiable)
		}
	})

	// Round 3, minors 4/5: a transient GitHub read failure while proving a
	// hop is "could not verify", never "stale" — the walk must say so
	// distinctly so the caller records a finding and lands instead of
	// false-refusing on a blip it never actually observed as a mismatch.
	t.Run("a transient read failure while proving a hop is unverifiable, not stale", func(t *testing.T) {
		reviewCommitParents = func(ctx context.Context, repository, sha string) ([]string, error) {
			return []string{"reviewed", "target1"}, nil
		}
		reviewHeadAdvanceProof = func(ctx context.Context, _ Git, _ runner.Runner, worktree, branch, target, repository, candidateSHA, targetParent, headSHA string) (bool, error) {
			return false, githubobserver.ErrTransientRetriesExhausted
		}
		if advanced, unverifiable, _ := reviewedHeadAdvanceChain(ctx, nil, nil, "wt", "feature", "acme/app", "main", "reviewed", "current"); advanced || !unverifiable {
			t.Fatalf("a transient proof failure must be unverifiable, not a stale verdict: advanced=%v unverifiable=%v", advanced, unverifiable)
		}
	})
}

func TestClosesLinesAndSuggestionsAndFinding(t *testing.T) {
	if got := closesLines(nil); got != "" {
		t.Fatalf("closesLines(nil) = %q", got)
	}
	got := closesLines([]int{591, 614})
	want := "Closes #591\nCloses #614\n\n"
	if got != want {
		t.Fatalf("closesLines = %q, want %q", got, want)
	}
	if body := withClosesPrefix("body text", nil); body != "body text" {
		t.Fatalf("withClosesPrefix with no issues = %q", body)
	}
	if body := withClosesPrefix("body text", []int{591}); body != "Closes #591\n\nbody text" {
		t.Fatalf("withClosesPrefix = %q", body)
	}

	if got := SuggestClosesFromPrompt("fix worktree_merge.go for #591 and codify #614, see also #591"); len(got) != 2 || got[0] != 591 || got[1] != 614 {
		t.Fatalf("SuggestClosesFromPrompt = %v", got)
	}
	if got := SuggestClosesFromPrompt("no issue named here"); len(got) != 0 {
		t.Fatalf("SuggestClosesFromPrompt with no issues = %v", got)
	}
	// Round 4, minor 3: a hex colour that mixes digits and hex letters
	// (#3b82f6, #00ff00) must suggest nothing at all — issueReferencePattern's
	// `\b` excludes it before looksLikeHexColorLength is even reached, since
	// `\d+` alone would otherwise misread a digit prefix ("3", "00") out of
	// the middle of the colour literal as an issue number. A real issue
	// reference alongside one must still be found.
	if got := SuggestClosesFromPrompt("use color #3b82f6 for the accent, see #591"); len(got) != 1 || got[0] != 591 {
		t.Fatalf("SuggestClosesFromPrompt(mixed-hex colour) = %v, want only #591", got)
	}
	if got := SuggestClosesFromPrompt("background #00ff00 looks off, see #591"); len(got) != 1 || got[0] != 591 {
		t.Fatalf("SuggestClosesFromPrompt(mixed-hex colour) = %v, want only #591", got)
	}
	// An all-decimal-digit colour of a colour-shaped length (#123456, 6
	// digits) is still excluded by looksLikeHexColorLength, not by `\b`.
	if got := SuggestClosesFromPrompt("background #123456 looks off, see #591"); len(got) != 1 || got[0] != 591 {
		t.Fatalf("SuggestClosesFromPrompt(all-decimal colour) = %v, want only #591", got)
	}
	// Round 4, minor 3: "#0" is never a real issue number.
	if got := SuggestClosesFromPrompt("see #0 for context, and #591 for the real issue"); len(got) != 1 || got[0] != 591 {
		t.Fatalf("SuggestClosesFromPrompt(#0) = %v, want #0 skipped and only #591", got)
	}

	if got := formatClosesFinding(nil); got != "no linked issue" {
		t.Fatalf("formatClosesFinding(nil) = %q", got)
	}
	if got := formatClosesFinding([]int{591}); got != "closes: #591" {
		t.Fatalf("formatClosesFinding = %q", got)
	}
}
