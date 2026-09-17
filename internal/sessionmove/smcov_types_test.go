package sessionmove

import (
	"encoding/hex"
	"math"
	"strings"
	"testing"
	"time"
)

func TestSmCovTypesNewHandoffIDIsOpaqueAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 3 {
		id, err := NewHandoffID()
		if err != nil {
			t.Fatal(err)
		}
		encoded := strings.TrimPrefix(id, "handoff-")
		if !strings.HasPrefix(id, "handoff-") || len(encoded) != 32 {
			t.Fatalf("handoff id %q is not handoff-<32 hex characters>", id)
		}
		if _, err := hex.DecodeString(encoded); err != nil {
			t.Fatalf("handoff id %q does not carry hex entropy: %v", id, err)
		}
		if encoded != strings.ToLower(encoded) {
			t.Fatalf("handoff id %q is not lowercase", id)
		}
		if seen[id] {
			t.Fatalf("handoff id %q was generated twice", id)
		}
		seen[id] = true
	}
}

func TestSmCovTypesDigestValidateRejectsNonCanonicalSpelling(t *testing.T) {
	digest := DigestBytes([]byte("payload"))
	if err := digest.validate(); err != nil {
		t.Fatalf("canonical digest %q was rejected: %v", digest, err)
	}
	if !digest.Matches([]byte("payload")) {
		t.Fatalf("canonical digest %q did not match its bytes", digest)
	}
	for _, value := range []Digest{
		Digest("sha256:" + strings.Repeat("A", 64)),
		Digest("sha256:" + strings.Repeat("g", 64)),
		Digest("md5:" + strings.Repeat("a", 64)),
		Digest(strings.Repeat("a", 64)),
		Digest("sha256:" + strings.Repeat("a", 63)),
	} {
		if err := value.validate(); err == nil {
			t.Fatalf("digest %q was accepted", value)
		}
	}
}

func TestSmCovTypesExternalHandoffClaimIDRejectsBadIdentity(t *testing.T) {
	digest := DigestBytes([]byte("request bytes"))
	first, err := ExternalHandoffClaimID(digest, "wbs-successor")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || first != strings.ToLower(first) {
		t.Fatalf("claim id %q is not 64 lowercase hex characters", first)
	}
	second, err := ExternalHandoffClaimID(digest, "wbs-successor")
	if err != nil || second != first {
		t.Fatalf("claim id is not deterministic: %q vs %q err=%v", first, second, err)
	}
	if _, err := ExternalHandoffClaimID("not-a-digest", "wbs-successor"); err == nil {
		t.Fatal("claim id accepted an invalid request digest")
	}
	if _, err := ExternalHandoffClaimID(digest, "not a session id"); err == nil {
		t.Fatal("claim id accepted an invalid successor session id")
	}
}

func TestSmCovTypesExpectedTargetWorkLogReferenceRejectsBadInput(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	reference, err := ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		t.Fatal(err)
	}
	if reference.EffortID != "effort-123" || reference.RunID != "run-456" || reference.ClaimID == "" {
		t.Fatalf("derived reference = %#v", reference)
	}
	older := request
	older.SchemaVersion = 0
	if _, err := ExpectedTargetWorkLogReference(older, digest); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("older request schema error = %v", err)
	}
	if _, err := ExpectedTargetWorkLogReference(request, "not-a-digest"); err == nil {
		t.Fatal("target reference accepted an invalid request digest")
	}
}

func TestSmCovTypesRequestValidationRejectsEachInvalidField(t *testing.T) {
	base := validRequest()
	largeContent := strings.Repeat("x", MaxHandoverContentBytes+1)
	tests := []struct {
		name   string
		mutate func(*Request)
		want   string
	}{
		{"older schema", func(value *Request) { value.SchemaVersion = 0 }, "unsupported"},
		{"handoff id", func(value *Request) { value.HandoffID = "bad id" }, "handoff_id"},
		{"successor id", func(value *Request) { value.SuccessorWBSessionID = "bad id" }, "successor_wb_session_id"},
		{"predecessor id", func(value *Request) { value.PredecessorWBSessionID = "bad id" }, "predecessor_wb_session_id"},
		{"source machine", func(value *Request) { value.SourceMachine = "bad id" }, "source_machine"},
		{"target machine", func(value *Request) { value.TargetMachine = "bad id" }, "target_machine"},
		{"blank repository remote", func(value *Request) { value.RepositoryRemote = "  " }, "repository_remote"},
		{"multi-line repository remote", func(value *Request) { value.RepositoryRemote = "git\nremote" }, "repository_remote"},
		{"blank branch", func(value *Request) { value.Branch = "\t" }, "branch"},
		{"multi-line branch", func(value *Request) { value.Branch = "a\rb" }, "branch"},
		{"short source work commit", func(value *Request) { value.SourceWorkCommit = "abc" }, "source_work_commit"},
		{"non-hex bundle commit", func(value *Request) { value.BundleCommit = strings.Repeat("z", 40) }, "bundle_commit"},
		{"bad handover digest", func(value *Request) { value.HandoverDigest = "bogus" }, "handover_digest"},
		{"empty legacy handover path", func(value *Request) { value.HandoverPath = "" }, "handover_path"},
		{"absolute legacy handover path", func(value *Request) { value.HandoverPath = "/etc/handover.md" }, "handover_path"},
		{"unclean legacy handover path", func(value *Request) { value.HandoverPath = "a/./b.md" }, "handover_path"},
		{"trailing-slash legacy handover path", func(value *Request) { value.HandoverPath = "a/b/" }, "handover_path"},
		{"blank source runtime", func(value *Request) { value.SourceRuntime = "   " }, "source_runtime"},
		{"multi-line requested harness", func(value *Request) { value.RequestedHarness = "a\nb" }, "single-line"},
		{"multi-line requested model", func(value *Request) { value.RequestedModel = "a\rb" }, "single-line"},
		{"zero created at", func(value *Request) { value.CreatedAt = time.Time{} }, "created_at"},
		{"path alongside inline content", func(value *Request) {
			value.HandoverContent = "handover document"
			value.HandoverDigest = DigestBytes([]byte(value.HandoverContent))
		}, "handover_path must be empty"},
		{"oversized inline content", func(value *Request) {
			value.HandoverPath = ""
			value.HandoverContent = largeContent
			value.HandoverDigest = DigestBytes([]byte(largeContent))
		}, "handover_content exceeds"},
		{"inline content digest mismatch", func(value *Request) {
			value.HandoverPath = ""
			value.HandoverContent = "handover document"
			value.HandoverDigest = DigestBytes([]byte("other document"))
		}, "handover_content does not match"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			_, err := EncodeRequest(value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("EncodeRequest error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSmCovTypesReceiptValidationRejectsEachInvalidField(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	base := validReceipt(request, digest)
	tests := []struct {
		name   string
		mutate func(*Receipt)
		want   string
	}{
		{"older schema", func(value *Receipt) { value.SchemaVersion = 0 }, "unsupported"},
		{"handoff id", func(value *Receipt) { value.HandoffID = "bad id" }, "handoff_id"},
		{"successor id", func(value *Receipt) { value.SuccessorWBSessionID = "bad id" }, "successor_wb_session_id"},
		{"predecessor id", func(value *Receipt) { value.PredecessorWBSessionID = "bad id" }, "predecessor_wb_session_id"},
		{"target machine", func(value *Receipt) { value.TargetMachine = "bad id" }, "target_machine"},
		{"tmux name", func(value *Receipt) { value.TmuxName = "bad id" }, "tmux_name"},
		{"request digest", func(value *Receipt) { value.RequestDigest = "bogus" }, "request_digest"},
		{"blank runtime", func(value *Receipt) { value.Runtime = "  " }, "runtime"},
		{"short pinned commit", func(value *Receipt) { value.PinnedCommit = "abc" }, "pinned_commit"},
		{"zero started at", func(value *Receipt) { value.StartedAt = time.Time{} }, "started_at"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			_, err := EncodeReceipt(value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("EncodeReceipt error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSmCovTypesMessageValidationRejectsEachInvalidField(t *testing.T) {
	base := validMessage(validRequest())
	tests := []struct {
		name   string
		mutate func(*Message)
		want   string
	}{
		{"message id", func(value *Message) { value.MessageID = "bad id" }, "message_id"},
		{"unsupported kind", func(value *Message) { value.Kind = "broadcast" }, "kind"},
		{"blank text body", func(value *Message) { value.Body = "   " }, "body is required"},
		{"zero sent at", func(value *Message) { value.SentAt = time.Time{} }, "sent_at"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			_, err := EncodeMessage(value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("EncodeMessage error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSmCovTypesDecodersRejectMalformedWireValues(t *testing.T) {
	if _, err := DecodeRequest([]byte("{")); err == nil || !strings.Contains(err.Error(), "parse session move request") {
		t.Fatalf("DecodeRequest error = %v", err)
	}
	if _, err := DecodeReceipt([]byte("{")); err == nil || !strings.Contains(err.Error(), "parse session move receipt") {
		t.Fatalf("DecodeReceipt error = %v", err)
	}
	if _, err := DecodeMessage([]byte("{")); err == nil || !strings.Contains(err.Error(), "parse session message") {
		t.Fatalf("DecodeMessage error = %v", err)
	}
	if _, err := DecodeMessageReceipt([]byte("{")); err == nil || !strings.Contains(err.Error(), "parse session message receipt") {
		t.Fatalf("DecodeMessageReceipt parse error = %v", err)
	}
	if _, err := DecodeMessageReceipt([]byte(`{"schema_version": 0}`)); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("DecodeMessageReceipt validation error = %v", err)
	}
}

func TestSmCovTypesJSONHelpersRejectTrailingAndUnsupportedValues(t *testing.T) {
	var target map[string]any
	if err := decodeJSON([]byte(`{} {}`), &target); err == nil || !strings.Contains(err.Error(), "unexpected trailing JSON value") {
		t.Fatalf("decoding trailing JSON error = %v", err)
	}
	if err := decodeJSON([]byte(`{} {`), &target); err == nil {
		t.Fatal("decoding truncated trailing JSON succeeded")
	}
	if err := decodeJSON([]byte(`{`), &target); err == nil {
		t.Fatal("decoding truncated JSON succeeded")
	}
	if _, err := marshalJSON(math.NaN()); err == nil {
		t.Fatal("marshalling NaN succeeded")
	}
}
