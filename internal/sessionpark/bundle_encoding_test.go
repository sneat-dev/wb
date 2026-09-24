package sessionpark

import (
	"testing"
	"time"
)

// TestEncodeBundleReportsEncodingFailure drives EncodeBundle's
// json.MarshalIndent error branch (store.go): validateBundle only requires
// a non-zero ParkedAt, so a caller-supplied ParkedAt past encoding/json's
// year bound ([0, 9999]) reaches the marshal call and fails to encode.
func TestEncodeBundleReportsEncodingFailure(t *testing.T) {
	t.Parallel()
	bundle := testBundle(t)
	bundle.ParkedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := EncodeBundle(bundle); err == nil {
		t.Fatal("EncodeBundle accepted a parked_at year outside [0, 9999], want an encoding error")
	}
}
