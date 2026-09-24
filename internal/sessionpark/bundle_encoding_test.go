package sessionpark

import (
	"strings"
	"testing"
	"time"
)

// wantYearOutOfRangeSubstring is the substring encoding/json's
// time.Time.MarshalJSON puts in its error when a time.Time's year falls
// outside [0, 9999].
const wantYearOutOfRangeSubstring = "year outside of range"

// TestEncodeBundleReportsEncodingFailure drives EncodeBundle's
// json.MarshalIndent error branch (store.go): validateBundle only requires
// a non-zero ParkedAt, so a caller-supplied ParkedAt past encoding/json's
// year bound ([0, 9999]) reaches the marshal call and fails to encode.
func TestEncodeBundleReportsEncodingFailure(t *testing.T) {
	t.Parallel()
	bundle := testBundle(t)
	bundle.ParkedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := EncodeBundle(bundle); err == nil || !strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
		t.Fatalf("EncodeBundle(parked_at year 10000) = %v, want a %q error", err, wantYearOutOfRangeSubstring)
	}
}
