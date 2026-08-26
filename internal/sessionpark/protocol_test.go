package sessionpark

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestParkResumeProtocolStrictBoundsAndUnsafeRemoteRedaction(t *testing.T) {
	request := BuildRemoteRequest(remoteTestBundle(t), "target", "codex", time.Unix(100, 0).UTC())
	envelope := Envelope{SchemaVersion: EnvelopeSchemaVersion, Kind: EnvelopeKind, Request: request}
	raw, err := EncodeEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEnvelope(raw)
	if err != nil || decoded.Request.ResumeID != request.ResumeID {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}

	t.Run("unknown field", func(t *testing.T) {
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		value["unexpected"] = true
		unknown, _ := json.Marshal(value)
		if _, err := DecodeEnvelope(unknown); err == nil {
			t.Fatal("unknown envelope field accepted")
		}
	})
	t.Run("trailing JSON", func(t *testing.T) {
		if _, err := DecodeEnvelope(append(bytes.Clone(raw), []byte("{}")...)); err == nil {
			t.Fatal("trailing JSON accepted")
		}
	})
	t.Run("zero members", func(t *testing.T) {
		candidate := envelope
		candidate.Request.Members = nil
		if _, err := EncodeEnvelope(candidate); err == nil || !strings.Contains(err.Error(), "requires between 1") {
			t.Fatalf("zero-member remote error = %v", err)
		}
	})
	t.Run("member count", func(t *testing.T) {
		candidate := envelope
		candidate.Request.Members = make([]RemoteMember, MaxMembers+1)
		if _, err := EncodeEnvelope(candidate); err == nil {
			t.Fatal("oversize member count accepted")
		}
	})
	t.Run("field bound", func(t *testing.T) {
		candidate := envelope
		candidate.Request.Members = append([]RemoteMember(nil), request.Members...)
		candidate.Request.Members[0].Branch = strings.Repeat("x", MaxFieldBytes+1)
		if _, err := EncodeEnvelope(candidate); err == nil {
			t.Fatal("oversize member field accepted")
		}
	})
	t.Run("credential redaction", func(t *testing.T) {
		secret := "top-secret-token"
		candidate := envelope
		candidate.Request.Members = append([]RemoteMember(nil), request.Members...)
		candidate.Request.Members[0].RepositoryRemote = "https://user:" + secret + "@github.com/acme/app.git"
		_, err := EncodeEnvelope(candidate)
		if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "user:") {
			t.Fatalf("unsafe remote error = %v", err)
		}
	})
}

func TestParkResumeReceiptBindsEveryExactMember(t *testing.T) {
	request := BuildRemoteRequest(remoteTestBundle(t), "target", "", time.Unix(100, 0).UTC())
	raw, err := EncodeEnvelope(Envelope{SchemaVersion: EnvelopeSchemaVersion, Kind: EnvelopeKind, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	admission := RemoteAdmission{Envelope: Envelope{SchemaVersion: EnvelopeSchemaVersion, Kind: EnvelopeKind, Request: request}, Raw: raw, Digest: digestEnvelope(raw)}
	receipt := validRemoteReceipt(t, admission)
	if err := ValidateReceipt(receipt, request, admission.Digest); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Receipt){
		func(value *Receipt) { value.Members[0].Repository = "acme/other" },
		func(value *Receipt) { value.Members[0].Commit = strings.Repeat("d", 40) },
		func(value *Receipt) { value.Members[0].Pin += "-other" },
		func(value *Receipt) {
			value.Members[0].TargetWorkLogReference = "worklog:effort/run/" + strings.Repeat("e", 64)
		},
	} {
		candidate := receipt
		candidate.Members = append([]ReceiptMember(nil), receipt.Members...)
		mutate(&candidate)
		if err := ValidateReceipt(candidate, request, admission.Digest); err == nil {
			t.Fatalf("mutated receipt accepted: %#v", candidate.Members[0])
		}
	}
}

func digestEnvelope(raw []byte) sessionmove.Digest { return sessionmove.DigestBytes(raw) }
