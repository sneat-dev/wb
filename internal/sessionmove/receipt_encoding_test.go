package sessionmove

import (
	"strings"
	"testing"
	"time"
)

// TestSaveReceiptReportsEncodingFailure drives saveReceiptAt's EncodeReceipt
// error branch (store.go): ValidateReceiptForRequest only requires a
// non-zero StartedAt, so a caller-supplied StartedAt past encoding/json's
// year bound ([0, 9999]) reaches the encode call and fails -- a real,
// reachable failure with no new production seam.
func TestSaveReceiptReportsEncodingFailure(t *testing.T) {
	t.Parallel()
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	store := NewStore(t.TempDir())
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}

	receipt := validReceipt(request, digest)
	receipt.StartedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); err == nil ||
		!strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
		t.Fatalf("SaveReceipt(started_at year 10000) = %v, want a %q error", err, wantYearOutOfRangeSubstring)
	}
}
