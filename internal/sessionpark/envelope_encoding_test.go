package sessionpark

import (
	"strings"
	"testing"
	"time"
)

// TestEncodeEnvelopeReportsEncodingFailure drives EncodeEnvelope's
// json.MarshalIndent error branch (protocol.go): validateRequest only
// requires a non-zero CreatedAt, so a caller-supplied CreatedAt past
// encoding/json's year bound ([0, 9999]) reaches the marshal call and fails
// -- a real, reachable failure with no new production seam, since
// BuildRemoteRequest takes `now` directly as a parameter.
func TestEncodeEnvelopeReportsEncodingFailure(t *testing.T) {
	t.Parallel()
	request := BuildRemoteRequest(remoteTestBundle(t), "target", "codex", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
	envelope := Envelope{SchemaVersion: EnvelopeSchemaVersion, Kind: EnvelopeKind, Request: request}
	if _, err := EncodeEnvelope(envelope); err == nil || !strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
		t.Fatalf("EncodeEnvelope(created_at year 10000) = %v, want a %q error", err, wantYearOutOfRangeSubstring)
	}
}
