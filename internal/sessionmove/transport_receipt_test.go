package sessionmove

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionterminal"
)

func TestExplicitHerdrMoveCannotSealALegacyReceipt(t *testing.T) {
	legacyRequest := validRequest()
	legacyRaw, err := EncodeRequest(legacyRequest)
	if err != nil {
		t.Fatal(err)
	}
	legacyReceipt := validReceipt(legacyRequest, DigestBytes(legacyRaw))
	legacyBytes, err := EncodeReceipt(legacyReceipt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeReceipt(legacyBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateReceiptForRequest(decoded, legacyRequest, DigestBytes(legacyRaw)); err != nil {
		t.Fatalf("legacy request rejected its immutable receipt: %v", err)
	}
	roundTrip, err := EncodeReceipt(decoded)
	if err != nil || !bytes.Equal(roundTrip, legacyBytes) {
		t.Fatalf("legacy canonical receipt changed: %v", err)
	}

	request := legacyRequest
	request.Transport = "herdr"
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	receipt := validReceipt(request, digest)
	if err := ValidateReceiptForRequest(receipt, request, digest); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("legacy downgrade validation = %v", err)
	}
	store := NewStore(filepath.Join(t.TempDir(), DirName))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("legacy downgrade publication = %v", err)
	}
	path := filepath.Join(store.Root, request.HandoffID, receiptFileName)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused legacy receipt was published: %v", err)
	}
	legacyDowngrade, err := EncodeReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacyDowngrade, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(request.HandoffID); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("persisted legacy downgrade load = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	receipt.TmuxName = ""
	receipt.Terminal = sessionterminal.Identity{Kind: "herdr", Socket: filepath.Join(t.TempDir(), "herdr.sock"), WorkspaceID: "workspace-owned", PaneID: "pane-owned"}
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); err != nil {
		t.Fatalf("matching Herdr receipt refused: %v", err)
	}
}
