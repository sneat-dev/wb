package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// approvalKind classifies the `--approved-by` value (#604). The URL and file
// forms are unchanged from before this issue and stay back-compatible; "ci"
// and the reviewer-identity triple are new.
type approvalKind int

const (
	approvalKindEmpty approvalKind = iota
	approvalKindURL
	approvalKindFile
	approvalKindCI
	approvalKindIdentity
)

// classifyApprovedBy decides which of the `--approved-by` shapes a value is.
// The check order matters: an existing file or a URL is recognized first so
// that back-compat inputs are never reinterpreted as an identity, and the
// literal "ci" is recognized before falling back to the identity shape.
//
// hasReviewComment reports whether the caller also supplied --review-comment
// or --review-comment-file. A bare word like "sonnet" or "review.md" is
// ambiguous between the new identity form and the old free-form approval
// string that predates #604 (any string was accepted, whether or not it
// named a file that actually existed on disk). A value shaped exactly like
// `{model}@{harness}[@{session}]` (see looksLikeReviewerIdentity) is an
// unambiguous identity marker and is always the identity shape. A bare word
// with no "@" — or a value containing "@" that does not fit that shape (an
// email address, an "@handle", a path with "@" in a directory name) — is
// only the identity shape when a review comment came with it (every genuine
// model-only identity caller supplies one, and the landing path refuses an
// identity without one); otherwise it stays the old free-form approval
// string, so every pre-#604 caller keeps working exactly as before (round 3,
// minor 3: an unqualified "value contains @" test misclassified an email or
// an "@handle" as an identity and refused it).
func classifyApprovedBy(value string, hasReviewComment bool) approvalKind {
	value = strings.TrimSpace(value)
	if value == "" {
		return approvalKindEmpty
	}
	if strings.EqualFold(value, "ci") {
		return approvalKindCI
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return approvalKindURL
	}
	if info, err := os.Stat(value); err == nil && !info.IsDir() {
		return approvalKindFile
	}
	if looksLikeReviewerIdentity(value) {
		return approvalKindIdentity
	}
	if !strings.Contains(value, "@") && hasReviewComment {
		return approvalKindIdentity
	}
	return approvalKindFile
}

// knownReviewerHarnesses lists the harness names looksLikeReviewerIdentity
// recognizes without requiring the value to avoid a "." entirely — a real
// harness name is never itself dotted, so this list only needs to name the
// harnesses this fleet actually uses, not every one that could ever exist.
var knownReviewerHarnesses = map[string]bool{
	"claude-code":       true,
	"codex":             true,
	"copilot":           true,
	"gemini":            true,
	unknownIdentityPart: true,
}

// looksLikeReviewerIdentity reports whether value is unambiguously the
// `{model}@{harness}[@{session}]` shape (round 3, minor 3): at most three
// non-empty "@"-separated segments, no "/" anywhere (a real identity is
// never path-shaped), and no "." in the harness segment unless the harness
// is one this fleet recognizes (or "unknown"). That last rule is what tells
// an identity like "opus@codex" apart from an email like
// "[email protected]" — "example.com" is not a known harness — without
// having to enumerate every possible harness name.
func looksLikeReviewerIdentity(value string) bool {
	if !strings.Contains(value, "@") || strings.Contains(value, "/") {
		return false
	}
	parts := strings.Split(value, "@")
	if len(parts) > 3 {
		return false
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return false
		}
	}
	harness := parts[1]
	if strings.Contains(harness, ".") && !knownReviewerHarnesses[strings.ToLower(harness)] {
		return false
	}
	return true
}

// ReviewerIdentity is the `{model}[@{harness}[@{session}]]` triple #604
// records for a declared review. Any part the caller did not supply, and
// that the environment cannot fill, is "unknown" — never guessed.
type ReviewerIdentity struct {
	Model   string `json:"model"`
	Harness string `json:"harness,omitempty"`
	Session string `json:"session,omitempty"`
}

// unknownIdentityPart is recorded, never guessed, for whatever part of a
// reviewer identity nothing — neither the caller nor the environment —
// determined.
const unknownIdentityPart = "unknown"

// ParseReviewerIdentity splits a raw `--approved-by` identity value on "@":
// model[@harness[@session]].
func ParseReviewerIdentity(raw string) ReviewerIdentity {
	parts := strings.Split(strings.TrimSpace(raw), "@")
	identity := ReviewerIdentity{}
	if len(parts) > 0 {
		identity.Model = strings.TrimSpace(parts[0])
	}
	if len(parts) > 1 {
		identity.Harness = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		identity.Session = strings.TrimSpace(parts[2])
	}
	return identity
}

// envClaudeCodeSessionID is the environment variable a Claude Code session
// exports; its presence is what lets an omitted harness/session be filled in
// as `claude-code` plus that exact session, per #604.
const envClaudeCodeSessionID = "CLAUDE_CODE_SESSION_ID"

// FillReviewerIdentityFromEnvironment fills whatever part of identity the
// caller omitted from the invoking process's environment. It never
// overwrites a part the caller supplied, and it never guesses: a part
// nothing determines is left for FinalizeReviewerIdentity to record as
// "unknown".
func FillReviewerIdentityFromEnvironment(identity ReviewerIdentity) ReviewerIdentity {
	if strings.TrimSpace(identity.Harness) == "" {
		if session := strings.TrimSpace(os.Getenv(envClaudeCodeSessionID)); session != "" {
			identity.Harness = "claude-code"
			if strings.TrimSpace(identity.Session) == "" {
				identity.Session = session
			}
		}
	}
	if strings.TrimSpace(identity.Harness) == "claude-code" && strings.TrimSpace(identity.Session) == "" {
		if session := strings.TrimSpace(os.Getenv(envClaudeCodeSessionID)); session != "" {
			identity.Session = session
		}
	}
	return identity
}

// FinalizeReviewerIdentity records whatever part is still empty after
// FillReviewerIdentityFromEnvironment as "unknown" — the receipt always
// stores the full triple, per #604.
func FinalizeReviewerIdentity(identity ReviewerIdentity) ReviewerIdentity {
	if strings.TrimSpace(identity.Model) == "" {
		identity.Model = unknownIdentityPart
	}
	if strings.TrimSpace(identity.Harness) == "" {
		identity.Harness = unknownIdentityPart
	}
	if strings.TrimSpace(identity.Session) == "" {
		identity.Session = unknownIdentityPart
	}
	return identity
}

// String renders the canonical `model@harness@session` form recorded on the
// receipt and printed in the posted review comment's header.
func (identity ReviewerIdentity) String() string {
	return identity.Model + "@" + identity.Harness + "@" + identity.Session
}

// SelfReview is true only when every one of the three parts matches the
// other identity — model-only matching is wrong for the normal flow (Sonnet
// implements, Opus subagent reviews, in the same session), per #604.
func (identity ReviewerIdentity) SelfReview(other ReviewerIdentity) bool {
	return strings.EqualFold(identity.Model, other.Model) &&
		strings.EqualFold(identity.Harness, other.Harness) &&
		strings.EqualFold(identity.Session, other.Session)
}

// currentSessionIdentity approximates "the task's Work Log author" with the
// identity of the session invoking this command: `worktrees.CurrentIdentity`
// is WB's own existing notion of who is driving it (declared via
// WB_AGENT_RUNTIME/WB_AGENT_MODEL/WB_SESSION_ID, or a registered session). A
// durable per-task Work Log author lookup is a later phase; until it exists
// this is the closest available proxy, and it is deliberately conservative:
// an unregistered, undeclared environment renders as all-"unknown" fields,
// which never spuriously matches a declared reviewer identity.
func currentSessionIdentity() ReviewerIdentity {
	current := worktrees.CurrentIdentity()
	identity := ReviewerIdentity{
		Model:   strings.TrimSpace(current.Model),
		Harness: strings.TrimSpace(current.Runtime),
		Session: strings.TrimSpace(current.WBSessionID),
	}
	if identity.Session == "" {
		identity.Session = strings.TrimSpace(current.AgentID)
	}
	identity = FillReviewerIdentityFromEnvironment(identity)
	return FinalizeReviewerIdentity(identity)
}

// ReviewDigest hashes a review comment's exact text, so the receipt records
// which review authorized a landing without needing to keep the whole text.
func ReviewDigest(comment string) string {
	sum := sha256.Sum256([]byte(comment))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

// reviewCommentBody renders the PR comment WB posts for an identity-form
// review: a header naming the reviewer and the exact head SHA it reviewed,
// then the review text verbatim.
func reviewCommentBody(identity ReviewerIdentity, headSHA, comment string) string {
	// The Reviewed-Head line is machine-readable and exact (the full SHA, not
	// the shortened display form): #586's binding check parses it back out
	// of a file or a fetched comment body, and it must match the API's own
	// head string byte-for-byte.
	return fmt.Sprintf("**Review by %s** (head `%s`)\nReviewed-Head: %s\n\n%s\n",
		identity.String(), shortMergeRevision(headSHA), headSHA, comment)
}

type issueCommentResponse struct {
	HTMLURL string `json:"html_url"`
}

// postReviewComment posts the review text as a pull-request comment (pull
// requests share GitHub's issue-comment endpoint) and returns its URL, which
// is recorded as the review evidence exactly the way a caller-supplied
// review-comment URL already was.
func postReviewComment(ctx context.Context, repository, number string, identity ReviewerIdentity, headSHA, comment string) (string, error) {
	body := reviewCommentBody(identity, headSHA, comment)
	response := githubExecute(ctx, "", "api", "--method", "POST",
		"repos/"+repository+"/issues/"+number+"/comments",
		"-f", "body="+body)
	if response.Err != nil {
		message := strings.TrimSpace(string(response.Stderr))
		if message == "" {
			message = strings.TrimSpace(string(response.Stdout))
		}
		return "", fmt.Errorf("post review comment on %s#%s: %s", repository, number, message)
	}
	var decoded issueCommentResponse
	if err := json.Unmarshal(response.Stdout, &decoded); err != nil {
		return "", fmt.Errorf("decode posted review comment on %s#%s: %w", repository, number, err)
	}
	if strings.TrimSpace(decoded.HTMLURL) == "" {
		return "", fmt.Errorf("post review comment on %s#%s: GitHub returned no comment URL", repository, number)
	}
	return decoded.HTMLURL, nil
}

// readReviewCommentText resolves the review text for the identity form:
// --review-comment verbatim, or --review-comment-file's contents. The
// caller validates that at most one is given and that whichever is given is
// non-empty; an empty result here is refused by the caller as
// LandRefusalReviewCommentEmpty.
func readReviewCommentText(comment, commentFile string) (string, error) {
	if trimmed := strings.TrimSpace(comment); trimmed != "" {
		return trimmed, nil
	}
	if path := strings.TrimSpace(commentFile); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read --review-comment-file %s: %w", path, err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return "", nil
}
