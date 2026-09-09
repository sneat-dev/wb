package orchestrate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// AbsorbedConflictAcknowledgementLookup names one worktree-merge receipt
// found under a projects root's reports directory whose candidate matches a
// task and worktree, together with its absorbed-conflict acknowledgement (see
// AcknowledgeAbsorbedConflict) when one currently validates against that
// exact, unchanged receipt.
type AbsorbedConflictAcknowledgementLookup struct {
	ReceiptPath     string
	Receipt         WorktreeMergeReceipt
	Acknowledged    bool
	Acknowledgement WorktreeMergeAbsorbedConflictAcknowledgement
}

// FindAbsorbedConflictAcknowledgement is a read-only lookup for other callers
// -- a status display, a diagnostic, a landing-proof consumer outside this
// package -- that need to know whether a candidate worktree already has a
// validated absorbed-conflict acknowledgement, without duplicating the
// receipt-matching and sidecar-validation logic AcknowledgeAbsorbedConflict
// and readAbsorbedConflictAcknowledgement already own. It never writes the
// receipt, the acknowledgement, or any Work Log, and it never deletes
// anything.
//
// ok is false only when no receipt under projectsRoot's worktree-merge
// reports names candidateTask/candidateWorktree as its candidate. When a
// matching receipt is found, lookup.Acknowledged reports whether its
// absorbed-conflict acknowledgement sidecar currently validates; a missing
// sidecar is reported as Acknowledged=false with a nil error, while a
// present-but-invalid (tampered, stale, or identity-mismatched) sidecar is
// reported as an error, never silently treated as absent -- a caller that
// wants "no usable acknowledgement" for either case should treat any error
// here the same way it treats Acknowledged=false.
func FindAbsorbedConflictAcknowledgement(projectsRoot, candidateTask, candidateWorktree string) (lookup AbsorbedConflictAcknowledgementLookup, ok bool, err error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return AbsorbedConflictAcknowledgementLookup{}, false, err
	}
	reportsDir := filepath.Join(home, "reports", "worktree-merge")
	entries, err := os.ReadDir(reportsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return AbsorbedConflictAcknowledgementLookup{}, false, nil
		}
		return AbsorbedConflictAcknowledgementLookup{}, false, fmt.Errorf("read worktree-merge reports %s: %w", reportsDir, err)
	}
	cleanWorktree := filepath.Clean(candidateWorktree)
	for _, dirEntry := range entries {
		name := dirEntry.Name()
		if dirEntry.IsDir() || !strings.HasSuffix(name, ".json") || strings.Contains(name, ".ack.json") {
			continue
		}
		receiptPath := filepath.Join(reportsDir, name)
		receipt, readErr := readWorktreeMergeReceipt(receiptPath)
		if readErr != nil {
			continue
		}
		if receipt.Candidate.Task != candidateTask || filepath.Clean(receipt.Candidate.Worktree) != cleanWorktree {
			continue
		}
		lookup = AbsorbedConflictAcknowledgementLookup{ReceiptPath: receiptPath, Receipt: receipt}
		ack, ackErr := readAbsorbedConflictAcknowledgement(absorbedConflictAcknowledgementPath(receiptPath), receipt)
		switch {
		case ackErr == nil:
			lookup.Acknowledged = true
			lookup.Acknowledgement = ack
		case errors.Is(ackErr, os.ErrNotExist):
			// No sidecar yet: a normal, reportable "not acknowledged" state.
		default:
			return AbsorbedConflictAcknowledgementLookup{}, false, ackErr
		}
		return lookup, true, nil
	}
	return AbsorbedConflictAcknowledgementLookup{}, false, nil
}
