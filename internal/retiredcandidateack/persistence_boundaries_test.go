package retiredcandidateack

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadRejectsMissingReceiptAfterReadingAcknowledgement(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "ack.json")
	if err := Persist(path, Acknowledgement{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, ReceiptIdentity{Path: filepath.Join(t.TempDir(), "missing-receipt.json")}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing receipt: %v", err)
	}
}

func TestPersistRejectsInvalidTimeWithoutCreatingArtifact(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "ack.json")
	ack := Acknowledgement{ID: "already-assigned", RecordedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := Persist(path, ack); err == nil || !strings.Contains(err.Error(), "year outside of range") {
		t.Fatalf("invalid audit timestamp: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid acknowledgement artifact exists: %v", err)
	}
}

func TestPersistRejectsFileAsParentDirectory(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Persist(filepath.Join(parent, "ack.json"), Acknowledgement{}); err == nil {
		t.Fatal("accepted file as parent")
	}
	if data, err := os.ReadFile(parent); err != nil || string(data) != "preserve" {
		t.Fatalf("parent changed: %q, %v", data, err)
	}
}
