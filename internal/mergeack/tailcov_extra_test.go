package mergeack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tailCovPathProofSourceProof returns one content-absorbed source proof whose
// single changed path is proved by the given path proof method and counts.
func tailCovPathProofSourceProof(method string, added, matched int) SourceProof {
	return SourceProof{
		Task: "source-0", Worktree: "/worktrees/source-0", Branch: "source-0", SHA: "source-sha",
		Method: "content_absorbed", MergeBaseSHA: "merge-base-sha", PathCount: 1,
		PathProofs: []PathProof{{Path: "spec/ledger.jsonl", Method: method, AddedLines: added, MatchedLines: matched}},
	}
}

// tailCovPersistAck persists a mutated acknowledgement for receiptPath and
// returns what Load reads back for the same receipt identity.
func tailCovPersistAck(t *testing.T, receiptPath string, mutate func(*Acknowledgement)) (Acknowledgement, error) {
	t.Helper()
	ack := newTestAcknowledgement(receiptPath)
	hash, err := FileSHA256(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	ack.ReceiptSHA256 = hash
	if mutate != nil {
		mutate(&ack)
	}
	ack.ID = ComputeID(ack)
	if err := Persist(Path(receiptPath), ack); err != nil {
		t.Fatal(err)
	}
	return Load(Path(receiptPath), testIdentity(t, receiptPath))
}

func TestTailCovComputeIDHashesPathProofsAndExcusedPaths(t *testing.T) {
	t.Parallel()
	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	base := newTestAcknowledgement(receiptPath)
	base.SourceProofs = []SourceProof{tailCovPathProofSourceProof("lines_absorbed", 2, 2)}
	base.ExcusedDerivedPaths = []string{"spec/README.md"}
	base.ID = ComputeID(base)

	if got := ComputeID(base); got != base.ID {
		t.Fatalf("ComputeID is not stable for a path-proof acknowledgement: %q vs %q", got, base.ID)
	}

	pathDrift := base
	pathDrift.SourceProofs = []SourceProof{tailCovPathProofSourceProof("lines_absorbed", 2, 2)}
	pathDrift.SourceProofs[0].PathProofs[0].Path = "spec/other.jsonl"
	if ComputeID(pathDrift) == base.ID {
		t.Fatal("ComputeID did not change when a path proof's path changed")
	}

	countDrift := base
	countDrift.SourceProofs = []SourceProof{tailCovPathProofSourceProof("lines_absorbed", 3, 3)}
	if ComputeID(countDrift) == base.ID {
		t.Fatal("ComputeID did not change when a path proof's line counts changed")
	}

	excusedDrift := base
	excusedDrift.ExcusedDerivedPaths = []string{"spec/other/README.md"}
	if ComputeID(excusedDrift) == base.ID {
		t.Fatal("ComputeID did not change when the excused derived paths changed")
	}
}

func TestTailCovSameSourcesComparesLengthAndContent(t *testing.T) {
	t.Parallel()
	source := Source{Task: "one", Worktree: "/worktrees/one", Branch: "one", SHA: "sha-one"}
	drifted := Source{Task: "two", Worktree: "/worktrees/one", Branch: "one", SHA: "sha-one"}

	if SameSources([]Source{source}, nil) {
		t.Fatal("SameSources = true for a different number of sources")
	}
	if SameSources([]Source{source, source}, []Source{source}) {
		t.Fatal("SameSources = true when the right side has fewer sources")
	}
	if SameSources([]Source{source}, []Source{drifted}) {
		t.Fatal("SameSources = true for a drifted source")
	}
	if !SameSources([]Source{source}, []Source{source}) {
		t.Fatal("SameSources = false for identical sources")
	}
}

func TestTailCovSameRejectsDivergentSourceProofEvidence(t *testing.T) {
	t.Parallel()
	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	left := newTestAcknowledgement(receiptPath)
	left.SourceProofs = []SourceProof{tailCovPathProofSourceProof("lines_absorbed", 4, 4)}
	left.ID = ComputeID(left)

	methodDrift := left
	methodDrift.SourceProofs = []SourceProof{tailCovPathProofSourceProof("lines_absorbed", 4, 4)}
	methodDrift.SourceProofs[0].Method = "ancestor"
	if Same(left, methodDrift) {
		t.Fatal("Same = true when a source proof method differs")
	}

	pathProofDrift := left
	pathProofDrift.SourceProofs = []SourceProof{tailCovPathProofSourceProof("lines_absorbed", 4, 4)}
	pathProofDrift.SourceProofs[0].PathProofs[0].Path = "spec/other.jsonl"
	if Same(left, pathProofDrift) {
		t.Fatal("Same = true when a path proof differs")
	}

	excusedDrift := left
	excusedDrift.ExcusedDerivedPaths = []string{"spec/README.md"}
	if Same(left, excusedDrift) {
		t.Fatal("Same = true when the excused derived paths differ")
	}
}

func TestTailCovFileSHA256ReportsMissingFile(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent.json")
	if _, err := FileSHA256(missing); err == nil {
		t.Fatal("FileSHA256 succeeded for a missing file")
	} else if !os.IsNotExist(err) {
		t.Fatalf("FileSHA256 error = %v, want os.IsNotExist", err)
	}
}

func TestTailCovPersistRejectsUnencodableTimestamp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ackPath := filepath.Join(dir, "ack.json")
	ack := newTestAcknowledgement(filepath.Join(dir, "receipt.json"))
	// time.Time refuses to marshal a year outside [0,9999]; Persist must
	// surface that encoder failure instead of writing a silent zero-byte file.
	ack.RecordedAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)

	if err := Persist(ackPath, ack); err == nil {
		t.Fatal("Persist succeeded for an unencodable acknowledgement")
	}
	if _, err := os.Stat(ackPath); !os.IsNotExist(err) {
		t.Fatalf("Persist left an artifact behind on encoder failure: %v", err)
	}
}

func TestTailCovPersistRejectsUncreatableDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	ack := newTestAcknowledgement(filepath.Join(dir, "receipt.json"))

	if err := Persist(filepath.Join(blocker, "nested", "ack.json"), ack); err == nil {
		t.Fatal("Persist succeeded when its parent directory could not be created")
	}
}

func TestTailCovPersistReportsUnwritableDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ackPath := filepath.Join(dir, "ack.json")
	ack := newTestAcknowledgement(filepath.Join(dir, "receipt.json"))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := Persist(ackPath, ack); err == nil {
		t.Fatal("Persist succeeded inside a read-only directory")
	}
	if _, err := os.Stat(ackPath); !os.IsNotExist(err) {
		t.Fatalf("Persist left an artifact behind in a read-only directory: %v", err)
	}
}

func TestTailCovLoadRejectsUndecodableSidecar(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	receiptPath := filepath.Join(dir, "receipt.json")
	writeTestReceipt(t, receiptPath)
	ackPath := Path(receiptPath)
	if err := os.WriteFile(ackPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(ackPath, testIdentity(t, receiptPath))
	if err == nil || !strings.Contains(err.Error(), "decode absorbed-conflict acknowledgement") {
		t.Fatalf("Load error = %v, want a decode failure", err)
	}
}

func TestTailCovLoadRejectsReceiptThatCannotBeHashed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	receiptPath := filepath.Join(dir, "receipt.json")
	ackPath := Path(receiptPath)
	// The sidecar itself decodes; the missing receipt it must be bound to is
	// what has to surface the failure.
	if err := os.WriteFile(ackPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(ackPath, testIdentity(t, receiptPath))
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("Load error = %v, want os.IsNotExist for the absent receipt", err)
	}
}

func TestTailCovLoadValidatesExcusedPathsAndProofs(t *testing.T) {
	t.Parallel()
	newReceipt := func(t *testing.T) string {
		t.Helper()
		receiptPath := filepath.Join(t.TempDir(), "receipt.json")
		writeTestReceipt(t, receiptPath)
		return receiptPath
	}

	t.Run("records each allowed excused derived path", func(t *testing.T) {
		t.Parallel()
		receiptPath := newReceipt(t)
		loaded, err := tailCovPersistAck(t, receiptPath, func(ack *Acknowledgement) {
			ack.ExcusedDerivedPaths = []string{"spec/README.md", "spec/features/thing/README.md"}
		})
		if err != nil {
			t.Fatalf("Load rejected an allowed derived path: %v", err)
		}
		if len(loaded.ExcusedDerivedPaths) != 2 || loaded.ExcusedDerivedPaths[1] != "spec/features/thing/README.md" {
			t.Fatalf("loaded excused paths = %v", loaded.ExcusedDerivedPaths)
		}
	})

	t.Run("rejects a proof whose identity drifted from its source", func(t *testing.T) {
		t.Parallel()
		receiptPath := newReceipt(t)
		_, err := tailCovPersistAck(t, receiptPath, func(ack *Acknowledgement) {
			ack.SourceProofs[0].Task = "someone-else"
		})
		if err == nil || !strings.Contains(err.Error(), "source proof identity drift") {
			t.Fatalf("Load error = %v, want source proof identity drift", err)
		}
	})

	t.Run("rejects an unknown source proof method", func(t *testing.T) {
		t.Parallel()
		receiptPath := newReceipt(t)
		_, err := tailCovPersistAck(t, receiptPath, func(ack *Acknowledgement) {
			ack.SourceProofs[0].Method = "teleported"
		})
		if err == nil || !strings.Contains(err.Error(), "unknown proof method") {
			t.Fatalf("Load error = %v, want an unknown proof method failure", err)
		}
	})

	t.Run("rejects a content absorbed proof without evidence", func(t *testing.T) {
		t.Parallel()
		receiptPath := newReceipt(t)
		_, err := tailCovPersistAck(t, receiptPath, func(ack *Acknowledgement) {
			ack.SourceProofs[0].Method = "content_absorbed"
			ack.SourceProofs[0].MergeBaseSHA = ""
			ack.SourceProofs[0].PathCount = 0
		})
		if err == nil || !strings.Contains(err.Error(), "content-absorbed proof lacks") {
			t.Fatalf("Load error = %v, want a missing content-absorbed evidence failure", err)
		}
	})

	t.Run("accepts a blob absorbed path proof", func(t *testing.T) {
		t.Parallel()
		receiptPath := newReceipt(t)
		loaded, err := tailCovPersistAck(t, receiptPath, func(ack *Acknowledgement) {
			ack.SourceProofs = []SourceProof{tailCovPathProofSourceProof("blob_absorbed", 0, 0)}
		})
		if err != nil {
			t.Fatalf("Load rejected a blob-absorbed path proof: %v", err)
		}
		if loaded.SourceProofs[0].PathProofs[0].Method != "blob_absorbed" {
			t.Fatalf("loaded path proof = %#v", loaded.SourceProofs[0].PathProofs[0])
		}
	})

	t.Run("rejects a lines absorbed proof with unmatched counts", func(t *testing.T) {
		t.Parallel()
		receiptPath := newReceipt(t)
		_, err := tailCovPersistAck(t, receiptPath, func(ack *Acknowledgement) {
			ack.SourceProofs = []SourceProof{tailCovPathProofSourceProof("lines_absorbed", 3, 2)}
		})
		if err == nil || !strings.Contains(err.Error(), "unproved lines-absorbed path") {
			t.Fatalf("Load error = %v, want an unproved lines-absorbed failure", err)
		}
	})

	t.Run("accepts a lines absorbed proof with matched counts", func(t *testing.T) {
		t.Parallel()
		receiptPath := newReceipt(t)
		loaded, err := tailCovPersistAck(t, receiptPath, func(ack *Acknowledgement) {
			ack.SourceProofs = []SourceProof{tailCovPathProofSourceProof("lines_absorbed", 5, 5)}
		})
		if err != nil {
			t.Fatalf("Load rejected a fully matched lines-absorbed proof: %v", err)
		}
		if loaded.SourceProofs[0].PathProofs[0].MatchedLines != 5 {
			t.Fatalf("loaded path proof = %#v", loaded.SourceProofs[0].PathProofs[0])
		}
	})

	t.Run("rejects an excused path proof that was never recorded", func(t *testing.T) {
		t.Parallel()
		receiptPath := newReceipt(t)
		_, err := tailCovPersistAck(t, receiptPath, func(ack *Acknowledgement) {
			ack.SourceProofs = []SourceProof{tailCovPathProofSourceProof("derived_excused", 0, 0)}
			ack.SourceProofs[0].PathProofs[0].Path = "spec/README.md"
		})
		if err == nil || !strings.Contains(err.Error(), "not in its recorded excused derived paths") {
			t.Fatalf("Load error = %v, want an unrecorded excused path failure", err)
		}
	})

	t.Run("accepts a recorded excused derived path proof", func(t *testing.T) {
		t.Parallel()
		receiptPath := newReceipt(t)
		loaded, err := tailCovPersistAck(t, receiptPath, func(ack *Acknowledgement) {
			ack.ExcusedDerivedPaths = []string{"spec/README.md"}
			ack.SourceProofs = []SourceProof{tailCovPathProofSourceProof("derived_excused", 0, 0)}
			ack.SourceProofs[0].PathProofs[0].Path = "spec/README.md"
		})
		if err != nil {
			t.Fatalf("Load rejected a recorded excused derived path: %v", err)
		}
		if loaded.SourceProofs[0].PathProofs[0].Method != "derived_excused" {
			t.Fatalf("loaded path proof = %#v", loaded.SourceProofs[0].PathProofs[0])
		}
	})

	t.Run("rejects an unknown path proof method", func(t *testing.T) {
		t.Parallel()
		receiptPath := newReceipt(t)
		_, err := tailCovPersistAck(t, receiptPath, func(ack *Acknowledgement) {
			ack.SourceProofs = []SourceProof{tailCovPathProofSourceProof("abducted", 0, 0)}
		})
		if err == nil || !strings.Contains(err.Error(), "unknown path proof method") {
			t.Fatalf("Load error = %v, want an unknown path proof method failure", err)
		}
	})
}
