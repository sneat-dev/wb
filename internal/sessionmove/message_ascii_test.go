package sessionmove

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMessageSchemaTwoIsOneASCIILineWithExactUnicodeBody(t *testing.T) {
	t.Parallel()
	message := validMessage(validRequest())
	message.Body = "line one\r\nline two\n café 中文 👩‍💻 😀 \u2028\u2029 \x00\x01\t $(`literal`)"
	raw, err := EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range raw {
		if value > 127 || value == '\n' || value == '\r' {
			t.Fatalf("wire payload contains physical multiline/non-ASCII byte: %x", value)
		}
	}
	if len(raw) == 0 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		t.Fatal("canonical object has trailing input or wrong envelope")
	}
	if !bytes.Contains(raw, []byte(`\ud83d\udc69\u200d\ud83d\udcbb`)) {
		t.Fatal("ZWJ emoji did not use exact surrogate/ASCII encoding")
	}
	decoded, err := DecodeMessage(raw)
	if err != nil || decoded != message {
		t.Fatalf("semantic Unicode/newline roundtrip changed: %#v %v", decoded, err)
	}
	again, err := EncodeMessage(decoded)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatalf("canonical bytes/digest changed on replay: %v", err)
	}
	if err := RequireActionableMessage(decoded); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyMessageCanonicalBytesStayImmutableAndReadOnly(t *testing.T) {
	t.Parallel()
	message := validMessage(validRequest())
	message.SchemaVersion = LegacyMessageSchemaVersion
	message.Body = "café\nsecond line"
	message.MessageID = "message-legacy"
	message.HandoffID = "handoff-legacy"
	message.SenderWBSessionID = "wbs-source"
	message.RecipientWBSessionID = "wbs-target"
	message.ReplyToWBSessionID = "wbs-source"
	message.SentAt = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	expected := []byte("{\n  \"schema_version\": 1,\n  \"message_id\": \"message-legacy\",\n  \"handoff_id\": \"handoff-legacy\",\n  \"sender_wb_session_id\": \"wbs-source\",\n  \"recipient_wb_session_id\": \"wbs-target\",\n  \"reply_to_wb_session_id\": \"wbs-source\",\n  \"kind\": \"text\",\n  \"body\": \"café\\nsecond line\",\n  \"sent_at\": \"2026-08-25T12:00:00Z\"\n}\n")
	raw, err := EncodeMessage(message)
	if err != nil || !bytes.Equal(raw, expected) {
		t.Fatalf("schema-1 bytes changed:\n%s\n%v", raw, err)
	}
	decoded, err := DecodeMessage(expected)
	if err != nil || decoded != message {
		t.Fatalf("legacy reader changed: %#v %v", decoded, err)
	}
	if err := RequireActionableMessage(decoded); err == nil || !strings.Contains(err.Error(), "new message ID") {
		t.Fatalf("legacy active delivery not refused actionably: %v", err)
	}
}

func TestASCIIMessageExpansionRetainsSemanticBodyLimit(t *testing.T) {
	t.Parallel()
	message := validMessage(validRequest())
	message.Body = "x" + strings.Repeat("\x01", MaxMessageBodyBytes-1)
	raw, err := EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= MaxMessageBodyBytes+(16<<10) || len(raw) > MaxMessagePayloadBytes {
		t.Fatalf("expanded payload was not bounded correctly: %d", len(raw))
	}
	decoded, err := DecodeMessage(raw)
	if err != nil || decoded.Body != message.Body {
		t.Fatalf("maximum semantic body changed: %v", err)
	}
	message.Body += "x"
	if _, err := EncodeMessage(message); err == nil {
		t.Fatal("semantic body limit expanded with wire limit")
	}
	message = validMessage(validRequest())
	message.MessageID = strings.Repeat("a", 16<<10)
	if _, err := EncodeMessage(message); err == nil {
		t.Fatal("unbounded identity envelope admitted")
	}
}

func TestMessageRejectsMalformedUTF8BeforeDurableCanonicalization(t *testing.T) {
	t.Parallel()
	raw, err := EncodeMessage(validMessage(validRequest()))
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte("Please"), []byte{0xff}, 1)
	if _, err := DecodeMessage(raw); err == nil {
		t.Fatal("malformed UTF-8 silently replaced before admission")
	}
	message := validMessage(validRequest())
	message.SchemaVersion = 9
	if _, err := EncodeMessage(message); err == nil {
		t.Fatal("unknown schema accepted")
	}
	message = validMessage(validRequest())
	message.SentAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := EncodeMessage(message); err == nil {
		t.Fatal("unencodable typed timestamp accepted")
	}
}
