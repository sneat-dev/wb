package retiredcandidateack

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadBindsExactReceiptAndCandidateOnlyIdentity(t *testing.T) {
	dir := t.TempDir()
	receipt := filepath.Join(dir, "merge.json")
	if err := os.WriteFile(receipt, []byte("immutable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := Source{Task: "candidate", Worktree: "/candidate", Branch: "wb/integration/old/x", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	id := ReceiptIdentity{Path: receipt, ID: "receipt", Phase: "prepare", Status: "conflict", Lane: "lane", Repository: "acme/app", Target: "old", TargetSHA: candidate.SHA, Candidate: candidate, Sources: []Source{{Task: "source", Worktree: "/source", Branch: "feature/source", SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}
	hash, err := FileSHA256(receipt)
	if err != nil {
		t.Fatal(err)
	}
	ack := Acknowledgement{SchemaVersion: SchemaVersion, Status: Status, ReceiptPath: receipt, ReceiptSHA256: hash, ReceiptID: id.ID, ReceiptPhase: id.Phase, ReceiptStatus: id.Status, Lane: id.Lane, Repository: id.Repository, Target: id.Target, TargetSHA: id.TargetSHA, Candidate: candidate, Sources: id.Sources, DefaultBranch: "main", DefaultSHA: "cccccccccccccccccccccccccccccccccccccccc", Actor: "reviewer", Reason: "test", RecordedAt: time.Date(2026, time.September, 22, 0, 0, 0, 0, time.UTC)}
	ack.ID = ComputeID(ack)
	if err := Persist(Path(receipt), ack); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(Path(receipt), id); err != nil {
		t.Fatal(err)
	}
	wrongIdentity := id
	wrongIdentity.ID = "other-receipt"
	if _, err := Load(Path(receipt), wrongIdentity); err == nil {
		t.Fatal("sidecar accepted another receipt identity")
	}
	wrongIdentity = id
	wrongIdentity.Sources = append([]Source(nil), id.Sources...)
	wrongIdentity.Sources[0].SHA = "another-source-sha"
	if _, err := Load(Path(receipt), wrongIdentity); err == nil {
		t.Fatal("sidecar accepted another source identity")
	}
	if err := Persist(Path(receipt), ack); !errors.Is(err, os.ErrExist) {
		t.Fatalf("append-only sidecar was overwritten: %v", err)
	}
	ack.Actor, ack.Reason = "", ""
	ack.ID = ComputeID(ack)
	encoded, err := json.Marshal(ack)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(receipt), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(Path(receipt), id); err == nil {
		t.Fatal("acknowledgement without an audit actor and reason was accepted")
	}
	ack.Actor, ack.Reason, ack.RecordedAt = "reviewer", "test", time.Time{}
	ack.ID = ComputeID(ack)
	encoded, err = json.Marshal(ack)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(receipt), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(Path(receipt), id); err == nil {
		t.Fatal("acknowledgement without a recorded audit time was accepted")
	}
	if err := os.WriteFile(receipt, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(Path(receipt), id); err == nil {
		t.Fatal("tampered receipt was accepted")
	}
}

func TestLoadAndHashRejectMissingOrMalformedFiles(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, err := FileSHA256(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing receipt hash: %v", err)
	}
	if _, err := Load(missing, ReceiptIdentity{Path: missing}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing sidecar load: %v", err)
	}
	if err := os.WriteFile(missing, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(missing, ReceiptIdentity{Path: missing}); err == nil {
		t.Fatal("malformed sidecar was accepted")
	}
}

func TestDistinctCandidateRejectsSourceConfusion(t *testing.T) {
	c := Source{Task: "candidate", Worktree: "/candidate", Branch: "branch", SHA: "a"}
	if DistinctCandidate(c, []Source{{Task: "source", Worktree: "/candidate", Branch: "other", SHA: "b"}}) {
		t.Fatal("candidate/source worktree collision accepted")
	}
}
