package mergeack

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestAcknowledgement(receiptPath string) Acknowledgement {
	return Acknowledgement{
		SchemaVersion: SchemaVersion, Status: Status,
		ReceiptPath: receiptPath, AcknowledgementPath: receiptPath + FileSuffix,
		ReceiptID: "receipt-1", ReceiptSHA256: "deadbeef", ReceiptStatus: "conflict", Lane: "lane-1",
		Repository: "acme/app", Target: "main", ReceiptTargetSHA: "target-sha", CurrentTargetSHA: "current-sha",
		CandidateTask: "task-1", CandidateWorktree: "/worktrees/task-1", CandidateBranch: "wb/integration/main/task-1", CandidateSHA: "candidate-sha",
		Sources: []Source{{Task: "source-0", Worktree: "/worktrees/source-0", Branch: "source-0", SHA: "source-sha"}},
		SourceProofs: []SourceProof{
			{Task: "source-0", Worktree: "/worktrees/source-0", Branch: "source-0", SHA: "source-sha", Method: "ancestor"},
		},
		Actor: "reviewer", Reason: "audited absorbed conflict", RecordedAt: time.Date(2026, 9, 7, 15, 10, 32, 0, time.UTC),
	}
}

func writeTestReceipt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"receipt":"fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testIdentity(t *testing.T, receiptPath string) ReceiptIdentity {
	t.Helper()
	return ReceiptIdentity{
		Path: receiptPath, ID: "receipt-1", Status: "conflict", Lane: "lane-1",
		Repository: "acme/app", Target: "main", TargetSHA: "target-sha",
		Candidate: Source{Task: "task-1", Worktree: "/worktrees/task-1", Branch: "wb/integration/main/task-1", SHA: "candidate-sha"},
		Sources:   []Source{{Task: "source-0", Worktree: "/worktrees/source-0", Branch: "source-0", SHA: "source-sha"}},
	}
}

func TestComputeIDIsStableAndSensitiveToEveryRecordedField(t *testing.T) {
	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	base := newTestAcknowledgement(receiptPath)
	base.ID = ComputeID(base)

	repeat := ComputeID(base)
	if repeat != base.ID {
		t.Fatalf("ComputeID is not stable: %q vs %q", repeat, base.ID)
	}

	// Actor and Reason are deliberately included in the ID, unlike Same: a
	// tamper that only rewrites who acknowledged it (or why) must still
	// invalidate the ID.
	tamperedActor := base
	tamperedActor.Actor = "attacker"
	if ComputeID(tamperedActor) == base.ID {
		t.Fatal("ComputeID did not change when Actor changed")
	}

	tamperedReason := base
	tamperedReason.Reason = "a different story"
	if ComputeID(tamperedReason) == base.ID {
		t.Fatal("ComputeID did not change when Reason changed")
	}

	tamperedSource := base
	tamperedSource.SourceProofs = append([]SourceProof(nil), base.SourceProofs...)
	tamperedSource.SourceProofs[0].Method = "content_absorbed"
	if ComputeID(tamperedSource) == base.ID {
		t.Fatal("ComputeID did not change when a source proof method changed")
	}

	// RecordedAt carries no evidentiary weight and is excluded.
	sameEvidence := base
	sameEvidence.RecordedAt = base.RecordedAt.Add(time.Hour)
	if ComputeID(sameEvidence) != base.ID {
		t.Fatal("ComputeID changed when only RecordedAt changed")
	}
}

func TestSameIgnoresActorReasonIDAndRecordedAtButNotEvidence(t *testing.T) {
	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	left := newTestAcknowledgement(receiptPath)
	left.ID = ComputeID(left)

	right := left
	right.Actor = "someone-else"
	right.Reason = "a different reason"
	right.RecordedAt = left.RecordedAt.Add(24 * time.Hour)
	right.ID = ComputeID(right)
	if !Same(left, right) {
		t.Fatal("Same = false for acknowledgements differing only in actor/reason/id/recorded_at")
	}

	differentEvidence := left
	differentEvidence.CurrentTargetSHA = "different-current-sha"
	if Same(left, differentEvidence) {
		t.Fatal("Same = true for acknowledgements with different evidence")
	}
}

func TestIsDerivedPathAllowed(t *testing.T) {
	cases := map[string]bool{
		"spec/README.md":              true,
		"spec/features/foo/README.md": true,
		"README.md":                   false,
		"spec/features/foo/NOTES.md":  false,
		"docs/spec/README.md":         false,
		"spec/README.md.bak":          false,
	}
	for path, want := range cases {
		if got := IsDerivedPathAllowed(path); got != want {
			t.Errorf("IsDerivedPathAllowed(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestPersistAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	receiptPath := filepath.Join(dir, "receipt.json")
	writeTestReceipt(t, receiptPath)

	ack := newTestAcknowledgement(receiptPath)
	hash, err := FileSHA256(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	ack.ReceiptSHA256 = hash
	ack.ID = ComputeID(ack)

	ackPath := Path(receiptPath)
	if ackPath != receiptPath+FileSuffix {
		t.Fatalf("Path(%q) = %q, want suffix %q", receiptPath, ackPath, FileSuffix)
	}
	if err := Persist(ackPath, ack); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(ackPath, testIdentity(t, receiptPath))
	if err != nil {
		t.Fatalf("Load failed on a freshly persisted acknowledgement: %v", err)
	}
	if !Same(loaded, ack) {
		t.Fatalf("loaded acknowledgement does not match what was persisted: %+v vs %+v", loaded, ack)
	}
	if loaded.ID != ack.ID {
		t.Fatalf("loaded ID = %q, want %q", loaded.ID, ack.ID)
	}
}

func TestLoadRejectsReceiptEditedAfterAcknowledgement(t *testing.T) {
	dir := t.TempDir()
	receiptPath := filepath.Join(dir, "receipt.json")
	writeTestReceipt(t, receiptPath)

	ack := newTestAcknowledgement(receiptPath)
	hash, err := FileSHA256(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	ack.ReceiptSHA256 = hash
	ack.ID = ComputeID(ack)
	if err := Persist(Path(receiptPath), ack); err != nil {
		t.Fatal(err)
	}

	// Edit the receipt after the acknowledgement was recorded.
	if err := os.WriteFile(receiptPath, []byte(`{"receipt":"edited"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(Path(receiptPath), testIdentity(t, receiptPath)); err == nil {
		t.Fatal("Load succeeded despite the receipt being edited since the acknowledgement was recorded")
	}
}

func TestLoadRejectsTamperedIDWithoutInvalidatingHash(t *testing.T) {
	dir := t.TempDir()
	receiptPath := filepath.Join(dir, "receipt.json")
	writeTestReceipt(t, receiptPath)

	ack := newTestAcknowledgement(receiptPath)
	hash, err := FileSHA256(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	ack.ReceiptSHA256 = hash
	ack.ID = ComputeID(ack)
	// Corrupt only the ID, leaving every other field (including the receipt
	// hash) genuinely valid: a hand-edit that forgot to recompute ID.
	ack.ID = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := Persist(Path(receiptPath), ack); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(Path(receiptPath), testIdentity(t, receiptPath)); err == nil {
		t.Fatal("Load succeeded despite a tampered ID")
	}
}

func TestLoadRejectsMissingSidecar(t *testing.T) {
	dir := t.TempDir()
	receiptPath := filepath.Join(dir, "receipt.json")
	writeTestReceipt(t, receiptPath)

	_, err := Load(Path(receiptPath), testIdentity(t, receiptPath))
	if err == nil {
		t.Fatal("Load succeeded for a missing sidecar")
	}
	if !os.IsNotExist(err) {
		t.Fatalf("Load error = %v, want os.IsNotExist", err)
	}
}

func TestLoadRejectsEmptiedSourceProofs(t *testing.T) {
	dir := t.TempDir()
	receiptPath := filepath.Join(dir, "receipt.json")
	writeTestReceipt(t, receiptPath)

	ack := newTestAcknowledgement(receiptPath)
	hash, err := FileSHA256(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	ack.ReceiptSHA256 = hash
	// Fabricate away the proof evidence while trying to keep the ID valid:
	// this must still be rejected because SourceProofs must have one entry
	// per receipt.Sources.
	ack.SourceProofs = nil
	ack.ID = ComputeID(ack)
	if err := Persist(Path(receiptPath), ack); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(Path(receiptPath), testIdentity(t, receiptPath)); err == nil {
		t.Fatal("Load succeeded despite emptied source proofs")
	}
}

func TestLoadRejectsExcusedPathOutsideAllowedShape(t *testing.T) {
	dir := t.TempDir()
	receiptPath := filepath.Join(dir, "receipt.json")
	writeTestReceipt(t, receiptPath)

	ack := newTestAcknowledgement(receiptPath)
	hash, err := FileSHA256(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	ack.ReceiptSHA256 = hash
	ack.ExcusedDerivedPaths = []string{"docs/README.md"}
	ack.ID = ComputeID(ack)
	if err := Persist(Path(receiptPath), ack); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(Path(receiptPath), testIdentity(t, receiptPath)); err == nil {
		t.Fatal("Load succeeded despite an excused path outside the allowed derived-index shape")
	}
}
