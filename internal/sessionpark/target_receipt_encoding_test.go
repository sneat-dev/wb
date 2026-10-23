package sessionpark

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestTargetStoreSaveReceiptUnderLockReportsEncodingFailure drives
// TargetStore.SaveReceiptUnderLock's EncodeReceipt error branch
// (target_store.go): validateReceiptShape only requires a non-zero
// StartedAt, so a caller-supplied StartedAt past encoding/json's year bound
// ([0, 9999]) reaches publication and fails to encode -- a real, reachable
// failure with no new production seam.
func TestTargetStoreSaveReceiptUnderLockReportsEncodingFailure(t *testing.T) {
	store, admission := spCovTargetFixture(t)
	request := admission.Envelope.Request
	lock, err := store.Acquire(context.Background(), request.ResumeID, admission.Digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })

	receipt := spCovTargetReceipt(t, admission)
	receipt.StartedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, _, err := store.SaveReceiptUnderLock(lock, request, admission.Digest, receipt); err == nil ||
		!strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
		t.Fatalf("SaveReceiptUnderLock(started_at year 10000) = %v, want a %q error", err, wantYearOutOfRangeSubstring)
	}
}
