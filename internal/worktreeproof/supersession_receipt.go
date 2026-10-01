package worktreeproof

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	// OriginalPR identifies the source pull request when this is a dependency
	// consolidation. A PR-scoped receipt opts into exact dependency proof;
	// generic worktree supersessions leave it empty.
	OriginalPR               string                        `json:"original_pr,omitempty"`
	OriginalPRNumber         int                           `json:"original_pr_number,omitempty"`
	OriginalPRRepository     string                        `json:"original_pr_repository,omitempty"`
	OriginalPRHead           string                        `json:"original_pr_head,omitempty"`
	DependencyDeltasComplete bool                          `json:"dependency_deltas_complete,omitempty"`
	DependencyDeltas         []SupersessionDependencyDelta `json:"dependency_deltas,omitempty"`
}

// SupersessionDependencyDelta is immutable, per-source-PR evidence used before
// an old dependency PR may be called superseded. The selector is deliberately
// explicit: nx and @nx/* are different direct package identities.
type SupersessionDependencyDelta struct {
	SourcePR         string `json:"source_pr"`
	SourceHead       string `json:"source_head"`
	Consumer         string `json:"consumer"`
	Ecosystem        string `json:"ecosystem"`
	Package          string `json:"package"`
	Manifest         string `json:"manifest"`
	Selector         string `json:"selector"`
	Before           string `json:"before"`
	RequestedAfter   string `json:"requested_after"`
	CandidateAfter   string `json:"candidate_after"`
	Lockfile         string `json:"lockfile,omitempty"`
	LockfileSelector string `json:"lockfile_selector,omitempty"`
	LockfileVersion  string `json:"lockfile_version,omitempty"`
	Reviewed         bool   `json:"reviewed"`
}

// DependencyAuditJSON and DependencyAuditMarkdown are deterministic per-PR
// renderings. They sort a copy so receipt bytes remain immutable.
func (receipt SupersessionReceipt) DependencyAuditJSON() ([]byte, error) {
	deltas := SortedDependencyDeltas(receipt.DependencyDeltas)
	payload := struct {
		OriginalPR       string                        `json:"original_pr"`
		OriginalPRNumber int                           `json:"original_pr_number"`
		OriginalPRRepo   string                        `json:"original_pr_repository"`
		OriginalPRHead   string                        `json:"original_pr_head"`
		OriginalHead     string                        `json:"original_head"`
		TargetHead       string                        `json:"target_head"`
		Complete         bool                          `json:"dependency_deltas_complete"`
		DependencyDeltas []SupersessionDependencyDelta `json:"dependency_deltas"`
	}{receipt.OriginalPR, receipt.OriginalPRNumber, receipt.OriginalPRRepository, receipt.OriginalPRHead, receipt.OriginalHead, receipt.TargetHead, receipt.DependencyDeltasComplete, deltas}
	return json.MarshalIndent(payload, "", "  ")
}

func (receipt SupersessionReceipt) DependencyAuditMarkdown() string {
	deltas := SortedDependencyDeltas(receipt.DependencyDeltas)
	var output strings.Builder
	fmt.Fprintf(&output, "# Dependency supersession audit: %s\n\n", receipt.OriginalPR)
	fmt.Fprintf(&output, "- Source PR: `%s` (#%d, `%s`)\n- Source head: `%s`\n- Candidate target: `%s`\n- Complete: `%t`\n\n", receipt.OriginalPR, receipt.OriginalPRNumber, receipt.OriginalPRRepository, receipt.OriginalHead, receipt.TargetHead, receipt.DependencyDeltasComplete)
	output.WriteString("| Consumer | Package | Manifest selector | Before | Requested after | Candidate after | Lockfile | Lockfile version | Source head | Reviewed |\n")
	output.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, delta := range deltas {
		fmt.Fprintf(&output, "| `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | %t |\n",
			delta.Consumer, delta.Package, delta.Manifest+":"+delta.Selector, delta.Before, delta.RequestedAfter, delta.CandidateAfter,
			delta.Lockfile, delta.LockfileVersion, delta.SourceHead, delta.Reviewed)
	}
	return output.String()
}

func SortedDependencyDeltas(deltas []SupersessionDependencyDelta) []SupersessionDependencyDelta {
	sorted := append([]SupersessionDependencyDelta(nil), deltas...)
	sort.Slice(sorted, func(i, j int) bool {
		left, right := sorted[i], sorted[j]
		for _, pair := range [][2]string{{left.SourcePR, right.SourcePR}, {left.Consumer, right.Consumer}, {left.Manifest, right.Manifest}, {left.Selector, right.Selector}, {left.Package, right.Package}, {left.Before, right.Before}} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return left.RequestedAfter < right.RequestedAfter
	})
	return sorted
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

func SameSupersessionReceipt(left, right *SupersessionReceipt) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
