package streamabsorb

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
)

// EventLedger stores review verdicts in an append-only event log.
//
// The stream's own log is used where there is a stream; a repository outside
// every stream records to the fleet log instead, because a review still has to
// be recorded somewhere even when there is no stream to hang it on.
//
// An append-only log is the right shape: a verdict is a historical fact, and
// the newest record for a fingerprint is the current answer. Nothing is ever
// rewritten, so a later round never erases the one before it.
type EventLedger struct {
	Log *streams.FileEventLog
	Now func() time.Time
}

const (
	ledgerVerb        = "review record"
	ledgerPhase       = "verdict"
	evidenceVerdict   = "verdict"
	evidenceRound     = "round"
	evidenceBy        = "by"
	evidenceWorktree  = "worktree"
	evidenceBranch    = "branch"
	evidenceHead      = "head"
	evidencePatchSet  = "patch_set"
	evidenceFingerpri = "fingerprint"
)

// Record implements Ledger.
func (ledger EventLedger) Record(record Record) error {
	if ledger.Log == nil {
		return fmt.Errorf("no ledger is available to record the verdict")
	}
	if record.RecordedAt.IsZero() {
		record.RecordedAt = ledger.now()
	}
	return ledger.Log.Append(streams.Event{
		Stream:     record.Stream,
		Verb:       ledgerVerb,
		Phase:      ledgerPhase,
		Outcome:    strings.ToLower(string(record.Verdict)),
		Timestamp:  record.RecordedAt,
		Detail:     record.Note,
		Repository: record.Branch,
		Evidence: map[string]string{
			evidenceVerdict:   string(record.Verdict),
			evidenceRound:     strconv.Itoa(record.Round),
			evidenceBy:        record.By,
			evidenceWorktree:  record.Worktree,
			evidenceBranch:    record.Branch,
			evidenceHead:      record.PatchSet.Head,
			evidencePatchSet:  strings.Join(record.PatchSet.IDs, ","),
			evidenceFingerpri: record.Fingerprint,
		},
	})
}

// Approval implements Ledger, returning the NEWEST record for a fingerprint.
//
// Newest wins because a later round supersedes an earlier one: an APPROVE
// followed by a REJECT for the same content must not still absorb.
func (ledger EventLedger) Approval(_ string, fingerprint string) (Record, bool, error) {
	if ledger.Log == nil || fingerprint == "" {
		return Record{}, false, nil
	}
	events, err := streams.ReadEvents(ledger.Log.Path)
	if err != nil {
		return Record{}, false, err
	}
	var newest Record
	found := false
	for _, event := range events {
		if event.Verb != ledgerVerb || event.Evidence[evidenceFingerpri] != fingerprint {
			continue
		}
		round, _ := strconv.Atoi(event.Evidence[evidenceRound])
		candidate := Record{
			Stream:      event.Stream,
			Worktree:    event.Evidence[evidenceWorktree],
			Branch:      event.Evidence[evidenceBranch],
			Verdict:     Verdict(event.Evidence[evidenceVerdict]),
			Round:       round,
			By:          event.Evidence[evidenceBy],
			Note:        event.Detail,
			Fingerprint: fingerprint,
			PatchSet: PatchSet{
				Head: event.Evidence[evidenceHead],
				IDs:  splitIDs(event.Evidence[evidencePatchSet]),
			},
			RecordedAt: event.Timestamp,
		}
		if !found || !candidate.RecordedAt.Before(newest.RecordedAt) {
			newest, found = candidate, true
		}
	}
	return newest, found, nil
}

func splitIDs(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func (ledger EventLedger) now() time.Time {
	if ledger.Now == nil {
		return time.Now().UTC()
	}
	return ledger.Now().UTC()
}
