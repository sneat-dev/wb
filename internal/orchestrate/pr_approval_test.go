package orchestrate

import (
	"context"
	"os"
	"strings"
	"testing"
)

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
		reviewHeadAdvanceProof = func(ctx context.Context, worktree, target, candidateSHA, targetParent, headSHA string) bool {
			t.Fatal("proof must not be consulted for a non-merge commit")
			return false
		}
		if reviewedHeadAdvanceChain(ctx, "wt", "acme/app", "main", "reviewed", "foreign") {
			t.Fatal("a foreign, non-merge commit must not be treated as still current")
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
		reviewHeadAdvanceProof = func(ctx context.Context, worktree, target, candidateSHA, targetParent, headSHA string) bool {
			return candidateSHA == "reviewed" && targetParent == "target1" && headSHA == "current"
		}
		if !reviewedHeadAdvanceChain(ctx, "wt", "acme/app", "main", "reviewed", "current") {
			t.Fatal("a proved update-branch merge advance must be allowed")
		}
	})

	t.Run("a crafted merge failing the proof is stale", func(t *testing.T) {
		reviewCommitParents = func(ctx context.Context, repository, sha string) ([]string, error) {
			return []string{"reviewed", "target1"}, nil
		}
		reviewHeadAdvanceProof = func(ctx context.Context, worktree, target, candidateSHA, targetParent, headSHA string) bool {
			return false // right parent shape, wrong content
		}
		if reviewedHeadAdvanceChain(ctx, "wt", "acme/app", "main", "reviewed", "current") {
			t.Fatal("a merge shape that fails the tree/ancestor proof must not be trusted")
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

	if got := formatClosesFinding(nil); got != "no linked issue" {
		t.Fatalf("formatClosesFinding(nil) = %q", got)
	}
	if got := formatClosesFinding([]int{591}); got != "closes: #591" {
		t.Fatalf("formatClosesFinding = %q", got)
	}
}
