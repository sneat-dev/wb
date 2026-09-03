// Package streamabsorb lands an agent's work on the stream branch locally.
//
// There are no pull requests below the stream. An agent branches from
// `stream/<name>`, works, and its change reaches the stream branch through
// `wb stream absorb` — a rebase and a squash, entirely local. Every agent pull
// request below the stream cost a push, a CI run, an API round trip and a
// review round on a branch nobody outside the stream would ever read.
//
// The work is still reviewed, and still lands as one reviewed commit. What
// disappears is the pull request that carried it — so the review has to hang on
// the CONTENT instead, which is what the ledger here is for.
//
// Implements: dependency-streams#req:agent-work-is-absorbed-locally,
// dependency-streams#req:review-pins-a-local-head,
// dependency-streams#req:the-squash-message-aggregates-the-source-commits,
// dependency-streams#req:pushes-are-justified-and-counted.
package streamabsorb

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Verdict is a review outcome.
type Verdict string

const (
	// VerdictApprove clears a patch set for absorption.
	VerdictApprove Verdict = "APPROVE"
	// VerdictApproveWithFixes records approval conditional on named fixes. It
	// does NOT clear absorption: the fixes change the content, which produces
	// a different patch set and needs its own round.
	VerdictApproveWithFixes Verdict = "APPROVE-WITH-FIXES"
	// VerdictReject refuses the patch set.
	VerdictReject Verdict = "REJECT"
)

// ParseVerdict validates a verdict from the command line.
func ParseVerdict(value string) (Verdict, error) {
	switch Verdict(strings.ToUpper(strings.TrimSpace(value))) {
	case VerdictApprove:
		return VerdictApprove, nil
	case VerdictApproveWithFixes:
		return VerdictApproveWithFixes, nil
	case VerdictReject:
		return VerdictReject, nil
	}
	return "", fmt.Errorf("unsupported verdict %q; use APPROVE, APPROVE-WITH-FIXES or REJECT", value)
}

// PatchSet identifies a body of work by what it CHANGES, not by where it sits.
//
// A review below the stream has no pull request to hang on, so it hangs on the
// content: the set of `git patch-id --stable` values over the commits the agent
// branch carries that the stream branch does not. A rebase that changes no
// content produces the same set, so an approval survives it; any content change
// produces a different set, so the approval no longer applies.
type PatchSet struct {
	// IDs are the patch identities, sorted so the fingerprint does not depend
	// on commit order — a reorder that changes no content is still the same
	// work.
	IDs []string `json:"ids"`
	// Head is the branch tip the review was taken against. It is recorded for
	// provenance and is deliberately NOT what the approval is keyed on: the
	// SHA changes on every rebase, the patch set does not.
	Head string `json:"head"`
}

// NewPatchSet builds a patch set from the commits a branch carries.
func NewPatchSet(head string, patchIDs []string) PatchSet {
	ids := append([]string(nil), patchIDs...)
	sort.Strings(ids)
	return PatchSet{IDs: ids, Head: head}
}

// Fingerprint is the stable identity of the patch set. Two reviews match when
// their fingerprints match, regardless of SHA, order or branch name.
func (set PatchSet) Fingerprint() string {
	if len(set.IDs) == 0 {
		return ""
	}
	digest := sha256.Sum256([]byte(strings.Join(set.IDs, "\n")))
	return hex.EncodeToString(digest[:])
}

// Empty reports whether there is nothing to absorb.
func (set PatchSet) Empty() bool { return len(set.IDs) == 0 }

// Record is one review verdict written to the ledger.
type Record struct {
	Stream      string    `json:"stream,omitempty"`
	Worktree    string    `json:"worktree"`
	Branch      string    `json:"branch,omitempty"`
	Verdict     Verdict   `json:"verdict"`
	Round       int       `json:"round"`
	By          string    `json:"by,omitempty"`
	Note        string    `json:"note,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	PatchSet    PatchSet  `json:"patch_set"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// Ledger stores and answers review verdicts.
//
// It is an interface so the absorb refusal is provable without a real event
// log, and so a repository outside any stream can record to the fleet log
// instead — a review still has to be recorded somewhere even when there is no
// stream to hang it on.
type Ledger interface {
	// Record writes one verdict.
	Record(record Record) error
	// Approval returns the newest record whose fingerprint matches, and
	// whether one exists at all.
	Approval(stream, fingerprint string) (Record, bool, error)
}

// Approved reports whether a record clears absorption.
//
// Only APPROVE does. APPROVE-WITH-FIXES deliberately does not: the fixes it
// asks for change the content, which produces a different patch set, and
// absorbing the unfixed set on the strength of that verdict would land exactly
// the code the reviewer asked to change.
func Approved(record Record) bool { return record.Verdict == VerdictApprove }
