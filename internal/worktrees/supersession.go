package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// SupersessionReceipt is the trusted-reviewer evidence required to retire a
// clean branch whose intent was split across replacement changes. It is an
// operator-supplied receipt, never an inference from CI, a closed PR, or
// patch/tree similarity.
type SupersessionReceipt struct {
	Version           int                       `json:"version"`
	Repository        string                    `json:"repository"`
	Task              string                    `json:"task"`
	Branch            string                    `json:"branch"`
	OriginalHead      string                    `json:"original_head"`
	Target            string                    `json:"target"`
	TargetHead        string                    `json:"target_head"`
	Replacements      []SupersessionReplacement `json:"replacements"`
	Residuals         []SupersessionResidual    `json:"residuals"`
	ResidualsComplete bool                      `json:"residuals_complete"`
	Approval          SupersessionApproval      `json:"approval"`
}

// SupersessionReplacement identifies the reviewed change that replaced an
// intended slice. A replacement may be a merged PR or an exact commit.
type SupersessionReplacement struct {
	Kind string `json:"kind"` // pr or commit
	Ref  string `json:"ref"`
	SHA  string `json:"sha,omitempty"`
}

// SupersessionResidual classifies one commit reachable from the original
// branch and outside the exact target. Every such commit must appear exactly
// once, including commits whose intended slice was replaced elsewhere.
type SupersessionResidual struct {
	Commit         string   `json:"commit"`
	Classification string   `json:"classification"` // replaced, obsolete, regressive, or cosmetic
	Reason         string   `json:"reason"`
	ReplacementRef string   `json:"replacement_ref,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	Reviewed       bool     `json:"reviewed"`
}

// SupersessionApproval is the explicit trusted-reviewer boundary. Trusted is
// intentionally a field in the immutable receipt: WB never guesses who is
// trusted from a PR, commit author, CI result, or local identity.
type SupersessionApproval struct {
	Actor      string    `json:"actor"`
	Trusted    bool      `json:"trusted"`
	Decision   string    `json:"decision"`
	ReceiptID  string    `json:"receipt_id"`
	ApprovedAt time.Time `json:"approved_at"`
}

var supersessionClassifications = map[string]bool{
	"replaced":   true,
	"obsolete":   true,
	"regressive": true,
	"cosmetic":   true,
}

// supersessionReceiptForEntry loads and independently verifies one receipt
// against the current exact source and fetched target identities.
func supersessionReceiptForEntry(ctx context.Context, path string, entry ListResult) (*SupersessionReceipt, string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Sprintf("read supersession receipt %s: %v", path, err), nil
	}
	var receipt SupersessionReceipt
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return nil, fmt.Sprintf("decode supersession receipt %s: %v", path, err), nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Sprintf("supersession receipt %s contains trailing JSON", path), nil
	}
	if rejection := validateSupersessionReceipt(ctx, receipt, entry); rejection != "" {
		return nil, rejection, nil
	}
	return &receipt, "", nil
}

func applySupersessionReceipt(ctx context.Context, path string, entry *ListResult) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	receipt, rejection, err := supersessionReceiptForEntry(ctx, path, *entry)
	if err != nil {
		return err
	}
	if rejection != "" {
		entry.SupersessionRejection = rejection
		entry.SupersededAtOrigin = false
		return nil
	}
	entry.SupersededAtOrigin = true
	entry.SupersessionReceipt = path
	entry.SupersessionReviewer = receipt.Approval.Actor
	entry.SupersessionReceiptID = receipt.Approval.ReceiptID
	entry.SupersessionRejection = ""
	entry.supersessionReceipt = receipt
	return nil
}

func validateSupersessionReceipt(ctx context.Context, receipt SupersessionReceipt, entry ListResult) string {
	if receipt.Version != 1 {
		return fmt.Sprintf("supersession receipt version %d is unsupported", receipt.Version)
	}
	if receipt.Repository != entry.Repository || receipt.Task != entry.Task || receipt.Branch != entry.Branch {
		return "supersession receipt source identity does not match the live worktree"
	}
	if receipt.OriginalHead != entry.HeadSHA {
		return fmt.Sprintf("supersession receipt original head %s does not match live head %s", receipt.OriginalHead, entry.HeadSHA)
	}
	if receipt.Target != entry.Base {
		return fmt.Sprintf("supersession receipt target %q does not match requested target %q", receipt.Target, entry.Base)
	}
	if receipt.TargetHead == "" || receipt.TargetHead != entry.RemoteTargetSHA {
		return fmt.Sprintf("supersession receipt target head %s does not match exact fetched origin/%s head %s", receipt.TargetHead, entry.Base, entry.RemoteTargetSHA)
	}
	if len(receipt.Replacements) == 0 {
		return "supersession receipt has no replacement PRs or commits"
	}
	for index, replacement := range receipt.Replacements {
		kind := strings.ToLower(strings.TrimSpace(replacement.Kind))
		if kind != "pr" && kind != "commit" {
			return fmt.Sprintf("replacement %d has unsupported kind %q", index+1, replacement.Kind)
		}
		if strings.TrimSpace(replacement.Ref) == "" && strings.TrimSpace(replacement.SHA) == "" {
			return fmt.Sprintf("replacement %d has no PR or commit reference", index+1)
		}
		if kind == "commit" && !isGitObjectID(replacement.SHA) {
			return fmt.Sprintf("replacement %d commit has no valid landed SHA", index+1)
		}
		if !isGitObjectID(replacement.SHA) {
			return fmt.Sprintf("replacement %d has no valid landed commit SHA", index+1)
		}
		landed, err := isAncestor(ctx, entry.CanonicalDir, replacement.SHA, entry.RemoteTargetSHA)
		if err != nil {
			return fmt.Sprintf("verify replacement %d landed in target: %v", index+1, err)
		}
		if !landed {
			return fmt.Sprintf("replacement %d commit %s is not contained in the exact target", index+1, replacement.SHA)
		}
	}
	if !receipt.ResidualsComplete {
		return "supersession receipt does not declare a complete residual inventory"
	}
	if len(receipt.Residuals) == 0 {
		return "supersession receipt has no classified residuals"
	}
	if receipt.Approval.Actor == "" || !receipt.Approval.Trusted || strings.ToLower(receipt.Approval.Decision) != "approved" || receipt.Approval.ReceiptID == "" || receipt.Approval.ApprovedAt.IsZero() {
		return "supersession receipt has no complete trusted-reviewer approval"
	}

	commitsOutput, err := git(ctx, entry.CanonicalDir, "rev-list", "--reverse", "--end-of-options", entry.RemoteTargetSHA+".."+entry.HeadSHA)
	if err != nil {
		return fmt.Sprintf("enumerate original branch commits: %v", err)
	}
	original := strings.Fields(commitsOutput)
	if len(original) == 0 {
		return "supersession receipt names a branch with no residual commits outside the target"
	}
	originalSet := make(map[string]bool, len(original))
	for _, commit := range original {
		originalSet[commit] = true
	}
	seen := make(map[string]bool, len(receipt.Residuals))
	for index, residual := range receipt.Residuals {
		if !isGitObjectID(residual.Commit) {
			return fmt.Sprintf("residual %d has invalid commit %q", index+1, residual.Commit)
		}
		if !originalSet[residual.Commit] {
			return fmt.Sprintf("residual %s is not a commit in the original branch", residual.Commit)
		}
		if seen[residual.Commit] {
			return fmt.Sprintf("residual %s is classified more than once", residual.Commit)
		}
		seen[residual.Commit] = true
		classification := strings.ToLower(strings.TrimSpace(residual.Classification))
		if !supersessionClassifications[classification] {
			return fmt.Sprintf("residual %s is unclassified (classification %q)", residual.Commit, residual.Classification)
		}
		if strings.TrimSpace(residual.Reason) == "" {
			return fmt.Sprintf("residual %s has no reviewer reason", residual.Commit)
		}
		if !residual.Reviewed {
			return fmt.Sprintf("residual %s is unreviewed", residual.Commit)
		}
		if classification == "replaced" && strings.TrimSpace(residual.ReplacementRef) == "" {
			return fmt.Sprintf("replaced residual %s has no replacement reference", residual.Commit)
		}
	}
	missing := make([]string, 0)
	for _, commit := range original {
		if !seen[commit] {
			missing = append(missing, commit)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Sprintf("supersession receipt has unclassified residual commit(s): %s", strings.Join(missing, ", "))
	}
	return ""
}

func sameSupersessionReceipt(left, right *SupersessionReceipt) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
