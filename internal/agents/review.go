package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// reviewFileName is the append-only sidecar holding acceptance decisions. It is
// deliberately not part of run.json: the owner writes run.json while validation
// runs, and a reviewer must never race that write.
const reviewFileName = "review.json"

// ReviewEntry is one acceptance decision.
type ReviewEntry struct {
	Status    string    `json:"status"`
	Evaluator string    `json:"evaluator"`
	Note      string    `json:"note,omitempty"`
	At        time.Time `json:"at"`
}

// ReviewRecord is the review axis of one execution. Status is `pending` until a
// reviewer appends an entry, and always reflects the most recent decision.
type ReviewRecord struct {
	Status    string        `json:"status"`
	Evaluator string        `json:"evaluator,omitempty"`
	Note      string        `json:"note,omitempty"`
	At        time.Time     `json:"at,omitempty"`
	History   []ReviewEntry `json:"history,omitempty"`
}

type reviewDocument struct {
	SchemaVersion int           `json:"schema_version"`
	AgentID       string        `json:"agent_id"`
	Entries       []ReviewEntry `json:"entries"`
}

// ReviewPath is the sidecar path for one execution.
func (store Store) ReviewPath(agentID string) string {
	return filepath.Join(store.Dir(agentID), reviewFileName)
}

// LoadReview reads the review sidecar. An absent sidecar is not an error: it
// means the execution has not been reviewed.
func (store Store) LoadReview(agentID string) (ReviewRecord, error) {
	if err := validateAgentID(agentID); err != nil {
		return ReviewRecord{}, err
	}
	raw, err := os.ReadFile(store.ReviewPath(agentID))
	if err != nil {
		if os.IsNotExist(err) {
			return ReviewRecord{Status: ReviewPending}, nil
		}
		return ReviewRecord{}, fmt.Errorf("read review for %s: %w", agentID, err)
	}
	var document reviewDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return ReviewRecord{}, fmt.Errorf("parse review for %s: %w", agentID, err)
	}
	if document.SchemaVersion != schemaVersion {
		return ReviewRecord{}, fmt.Errorf("review for %s has schema_version %d; this wb supports %d", agentID, document.SchemaVersion, schemaVersion)
	}
	record := ReviewRecord{Status: ReviewPending}
	for _, entry := range document.Entries {
		record = ReviewRecord{Status: entry.Status, Evaluator: entry.Evaluator, Note: entry.Note, At: entry.At}
	}
	record.History = document.Entries
	return record, nil
}

// AppendReview adds one decision and returns the resulting review record. The
// caller has already established that the execution is terminal, so nothing
// here can race an owner's save.
func (store Store) AppendReview(agentID string, entry ReviewEntry) (ReviewRecord, error) {
	if err := validateReviewStatus(entry.Status); err != nil {
		return ReviewRecord{}, requestErrorf("%v", err)
	}
	if entry.At.IsZero() {
		return ReviewRecord{}, fmt.Errorf("review entry requires a timestamp")
	}
	// A review only ever attaches to a real execution; without this check a
	// typo would create a sidecar directory instead of reporting a missing run.
	if _, err := store.Load(agentID); err != nil {
		return ReviewRecord{}, err
	}
	existing, err := store.LoadReview(agentID)
	if err != nil {
		return ReviewRecord{}, err
	}
	document := reviewDocument{SchemaVersion: schemaVersion, AgentID: agentID, Entries: append(existing.History, entry)}
	directory, err := os.Open(store.Dir(agentID))
	if err != nil {
		return ReviewRecord{}, fmt.Errorf("open review directory for %s: %w", agentID, err)
	}
	defer func() { _ = directory.Close() }()
	if err := filewrite.WriteJSONAtomicAt(directory, reviewFileName, document, 0o600); err != nil {
		return ReviewRecord{}, fmt.Errorf("write review for %s: %w", agentID, err)
	}
	return store.LoadReview(agentID)
}

// Latest projects the current decision without the history.
func (record ReviewRecord) Latest() ReviewRecord {
	return ReviewRecord{Status: record.Status, Evaluator: record.Evaluator, Note: record.Note, At: record.At}
}

// Reviewed reports whether a human decided something about this run.
func (record ReviewRecord) Reviewed() bool {
	switch record.Status {
	case ReviewAccepted, ReviewRejected, ReviewNeedsChange:
		return true
	default:
		return false
	}
}

func validateReviewStatus(status string) error {
	switch status {
	case ReviewAccepted, ReviewRejected, ReviewNeedsChange:
		return nil
	default:
		return fmt.Errorf("review status %q must be %q, %q or %q", status, ReviewAccepted, ReviewRejected, ReviewNeedsChange)
	}
}
