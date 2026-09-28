package worktrees

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The removed-worktree path has no live checkout to consult. Every file in its
// immutable evidence chain must therefore be checked before returning a base.
//
//nolint:paralleltest // The removed-terminal fixture sets process-wide WB state variables.
func TestRemovedTerminalEvidenceRejectsCorruptFilesWithoutCheckout(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(t *testing.T, home, claimID string)
		want string
	}{
		{"unsafe run entry", func(t *testing.T, home, _ string) {
			writeTerminalEvidenceFile(t, filepath.Join(home, "worklogs", "removed-task", "runs", "bad run"), []byte("foreign"))
		}, "unsafe terminal Work Log run"},
		{"unsafe claim entry", func(t *testing.T, home, _ string) {
			writeTerminalEvidenceFile(t, filepath.Join(home, "worklogs", "removed-task", "runs", "removed-run", "claims", "0bad claim.json"), []byte("foreign"))
		}, "unsafe terminal Work Log claim entry"},
		{"malformed claim", func(t *testing.T, home, id string) {
			writeTerminalEvidenceFile(t, terminalClaimPath(home, id), []byte("{"))
		}, "read immutable terminal Work Log claim"},
		{"invalid immutable identity", func(t *testing.T, home, id string) {
			editTerminalEvidenceJSON(t, terminalClaimPath(home, id), "base_sha", "invalid")
		}, "immutable Work Log claim"},
		{"missing terminal", func(t *testing.T, home, id string) {
			if err := os.Remove(terminalRecordPath(home, id)); err != nil {
				t.Fatal(err)
			}
		}, "read removed terminal Work Log"},
		{"terminal changed after seal", func(t *testing.T, home, id string) {
			editTerminalEvidenceJSON(t, terminalRecordPath(home, id), "worktree_disposition", "parked")
		}, "does not exactly corroborate"},
		{"missing public receipt", func(t *testing.T, home, id string) {
			if err := os.Remove(terminalOutboxPath(home, id)); err != nil {
				t.Fatal(err)
			}
		}, "read immutable terminal outbox"},
		{"public receipt changed", func(t *testing.T, home, id string) {
			editTerminalEvidenceJSON(t, terminalOutboxPath(home, id), "disposition", "parked")
		}, "outbox does not corroborate"},
	} {
		//nolint:paralleltest // Each fixture changes process-wide WB state variables.
		t.Run(tc.name, func(t *testing.T) {
			home, _, expectation, claimID := wtLogCovRemovedTerminalHome(t)
			tc.edit(t, home, claimID)
			if base, err := validateRemovedTerminalWorkLog(home, expectation); err == nil || !strings.Contains(err.Error(), tc.want) || base != "" {
				t.Fatalf("corrupt terminal evidence returned base %q, error %v; want %q", base, err, tc.want)
			}
		})
	}
}

func terminalClaimPath(home, id string) string {
	return filepath.Join(home, "worklogs", "removed-task", "runs", "removed-run", "claims", id+".json")
}

func terminalRecordPath(home, id string) string {
	return filepath.Join(home, "worklogs", "removed-task", "runs", "removed-run", "terminals", id+".json")
}

func terminalOutboxPath(home, id string) string {
	return filepath.Join(home, "worklogs", "removed-task", "outbox", "removed-run-"+id+"-sealed.json")
}

func writeTerminalEvidenceFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func editTerminalEvidenceJSON(t *testing.T, path, field string, value any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatal(err)
	}
	record[field] = value
	content, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	writeTerminalEvidenceFile(t, path, content)
}

//nolint:paralleltest // The removed-terminal fixture sets process-wide WB state variables.
func TestExecutionIdentityCorrectionRejectsBrokenPrivateEvidence(t *testing.T) {
	model := "unknown"
	for _, tc := range []struct {
		name string
		edit func(t *testing.T, home, id string)
		want string
	}{
		{"missing claim", func(t *testing.T, home, id string) {
			if err := os.Remove(terminalClaimPath(home, id)); err != nil {
				t.Fatal(err)
			}
		}, "read immutable claim"},
		{"mismatched claim identity", func(t *testing.T, home, id string) {
			editTerminalEvidenceJSON(t, terminalClaimPath(home, id), "run_id", "other-run")
		}, "claim does not match"},
		{"malformed correction history", func(t *testing.T, home, id string) {
			corrections := filepath.Join(home, "worklogs", "removed-task", "runs", "removed-run", "corrections", id)
			if err := os.MkdirAll(corrections, 0o700); err != nil {
				t.Fatal(err)
			}
			writeTerminalEvidenceFile(t, filepath.Join(corrections, "bad entry"), []byte("foreign"))
		}, "malformed execution-identity correction filename"},
		{"existing event has different evidence", func(t *testing.T, home, id string) {
			corrections := filepath.Join(home, "worklogs", "removed-task", "runs", "removed-run", "corrections", id)
			if err := os.MkdirAll(corrections, 0o700); err != nil {
				t.Fatal(err)
			}
			event := workLogIdentityCorrection{Version: 1, Type: "worktree.execution_identity_corrected", CorrectionID: "review-event",
				ClaimID: id, Sequence: 1, At: time.Now().UTC(), Actor: "other-reviewer", Reason: "different reason", Model: &model}
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			writeTerminalEvidenceFile(t, filepath.Join(corrections, "review-event.json"), encoded)
		}, "different immutable evidence"},
	} {
		//nolint:paralleltest // Each fixture changes process-wide WB state variables.
		t.Run(tc.name, func(t *testing.T) {
			home, projects, _, id := wtLogCovRemovedTerminalHome(t)
			tc.edit(t, home, id)
			_, err := CorrectExecutionIdentity(CorrectExecutionIdentityOptions{ProjectsRoot: projects,
				EffortID: "removed-task", RunID: "removed-run", ClaimID: id, EventID: "review-event",
				Actor: "reviewer", Reason: "actual runtime", Model: &model})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("broken correction evidence returned %v; want %q", err, tc.want)
			}
		})
	}
}
