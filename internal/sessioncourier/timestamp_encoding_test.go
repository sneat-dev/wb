package sessioncourier

import (
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func invalidCourierZone() *time.Location { return time.FixedZone("out-of-range", 24*60*60) }

func TestPayloadRejectsDecodedTimestampOutsideJSONTimezoneRange(t *testing.T) {
	t.Parallel()
	message, raw := courierTestMessage(t)
	changed := []byte(strings.Replace(string(raw), message.SentAt.Format(time.RFC3339), message.SentAt.In(invalidCourierZone()).Format(time.RFC3339), 1))
	if _, err := sessionmove.DecodeMessage(changed); err != nil {
		t.Fatalf("fixture not decodable: %v", err)
	}
	if _, err := validateMessagePayload(changed); err == nil || !strings.Contains(err.Error(), "timezone hour outside of range") {
		t.Fatalf("payload encoding = %v", err)
	}
}

func TestMessageReceiptRejectsDecodedTimestampOutsideJSONTimezoneRange(t *testing.T) {
	t.Parallel()
	message, messageRaw := courierTestMessage(t)
	receipt := courierTestMessageReceipt(message, messageRaw)
	raw, err := sessionmove.EncodeMessageReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte(strings.Replace(string(raw), receipt.RecordedAt.Format(time.RFC3339), receipt.RecordedAt.In(invalidCourierZone()).Format(time.RFC3339), 1))
	if _, err := sessionmove.DecodeMessageReceipt(changed); err != nil {
		t.Fatalf("fixture not decodable: %v", err)
	}
	if _, err := decodeMessageReceipt(changed, message, messageRaw); err == nil || !strings.Contains(err.Error(), "timezone hour outside of range") {
		t.Fatalf("receipt encoding = %v", err)
	}
}

func TestReceiverRejectsDecodedTimestampOutsideJSONTimezoneRange(t *testing.T) {
	t.Parallel()
	request, raw := courierTestRequest(t)
	changed := []byte(strings.Replace(string(raw), request.CreatedAt.Format(time.RFC3339), request.CreatedAt.In(invalidCourierZone()).Format(time.RFC3339), 1))
	if _, err := sessionmove.DecodeRequest(changed); err != nil {
		t.Fatalf("fixture not decodable: %v", err)
	}
	if _, err := validateReceiverRequest(changed, maxSSHRequestBytes); err == nil || !strings.Contains(err.Error(), "timezone hour outside of range") {
		t.Fatalf("request encoding = %v", err)
	}
	result := validCourierResult(request, raw)
	request.CreatedAt = request.CreatedAt.In(invalidCourierZone())
	if err := validateReceiverResult(result, request, raw); err == nil || !strings.Contains(err.Error(), "timezone hour outside of range") {
		t.Fatalf("delivered request encoding = %v", err)
	}
}
