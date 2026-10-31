package sessionpark

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionterminal"
)

func TestExplicitHerdrParkReceiptRejectsLegacyDowngradeAndPreservesHistoricalBytes(t *testing.T) {
	request := BuildRemoteRequest(remoteTestBundle(t), "target", "", time.Unix(100, 0).UTC())
	envelope := Envelope{SchemaVersion: EnvelopeSchemaVersion, Kind: EnvelopeKind, Request: request}
	raw, err := EncodeEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	digest := digestEnvelope(raw)
	herdr := validRemoteReceipt(t, RemoteAdmission{Envelope: envelope, Raw: raw, Digest: digest})
	if err := ValidateReceipt(herdr, request, digest); err != nil {
		t.Fatalf("matching Herdr receipt refused: %v", err)
	}
	legacy := herdr
	legacy.Terminal = sessionterminal.Identity{}
	legacy.TmuxName = "wb-session-" + request.SuccessorWBSessionID
	if err := ValidateReceipt(legacy, request, digest); err == nil || !strings.Contains(err.Error(), "explicit Herdr request") {
		t.Fatalf("legacy downgrade accepted: %v", err)
	}
	request.Transport = ""
	envelope.Request = request
	raw, err = EncodeEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	digest = digestEnvelope(raw)
	legacy = validRemoteReceipt(t, RemoteAdmission{Envelope: envelope, Raw: raw, Digest: digest})
	legacyRaw, err := EncodeReceipt(legacy)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeReceipt(legacyRaw)
	if err != nil {
		t.Fatalf("historical receipt decoder changed: %v", err)
	}
	if err := ValidateReceipt(decoded, request, digest); err != nil {
		t.Fatalf("historical omitted-transport receipt refused: %v", err)
	}
	roundTrip, err := EncodeReceipt(decoded)
	if err != nil || !bytes.Equal(legacyRaw, roundTrip) {
		t.Fatalf("historical canonical receipt changed: %v", err)
	}
}
