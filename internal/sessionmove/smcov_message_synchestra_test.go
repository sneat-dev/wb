package sessionmove

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// smCovSynchMessageFixture builds an admitted handoff with an optional
// immutable Synchestra route, handoff receipt, and durable outgoing message,
// then acquires the exact execution fence production callers hold.
func smCovSynchMessageFixture(t *testing.T, withRoute, withReceipt, withMessage bool) (Store, Request, Digest, *ExecutionLock, Message, []byte) {
	t.Helper()
	store, request, digest, _ := admittedRouteRequest(t, false)
	if withRoute {
		route := Route{
			HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
			Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "hetzner-vm1"},
		}
		if _, _, err := store.SaveRoute(route); err != nil {
			t.Fatal(err)
		}
	}
	if withReceipt {
		if _, _, err := store.SaveReceipt(request.HandoffID, digest, validReceipt(request, digest)); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage(request)
	raw, err := EncodeMessage(message)
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	if withMessage {
		if _, err := store.AdmitOutgoingMessageUnderLock(lock, request.HandoffID, digest, raw, message.SentAt); err != nil {
			_ = lock.Close()
			t.Fatal(err)
		}
	}
	return store, request, digest, lock, message, raw
}

func smCovSynchIdentity(request Request, digest, messageDigest Digest, messageID string) MessageSynchestraDispatch {
	return MessageSynchestraDispatch{
		HandoffID: request.HandoffID, RequestDigest: digest, MessageID: messageID, MessageDigest: messageDigest,
		Runner: "hetzner-vm1", InvocationID: messageID, Handler: SynchestraSessionMessageHandler,
		DispatchID: "dsp_message_1",
	}
}

func smCovSynchDispatchPath(store Store, request Request, messageID string) string {
	return filepath.Join(store.Root, request.HandoffID, messageOutboxDirName, messageID, messageSynchestraDispatchFileName)
}

func TestSmCovMessageSynchestraDispatchPublishesExactReplayableIdentity(t *testing.T) {
	store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, true, true, true)
	defer func() { _ = lock.Close() }()
	identity := smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)

	stored, created, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, identity)
	if err != nil {
		t.Fatal(err)
	}
	identity.SchemaVersion = MessageSynchestraDispatchSchemaVersion
	if created || !reflect.DeepEqual(stored, identity) {
		t.Fatalf("first dispatch = %#v created=%t, want %#v", stored, created, identity)
	}

	replayed, created, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, identity)
	if err != nil || !created || !reflect.DeepEqual(replayed, identity) {
		t.Fatalf("replayed dispatch = %#v created=%t err=%v", replayed, created, err)
	}

	loaded, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, message.MessageID)
	if err != nil || !reflect.DeepEqual(loaded, identity) {
		t.Fatalf("loaded dispatch = %#v err=%v, want %#v", loaded, err, identity)
	}
	path := smCovSynchDispatchPath(store, request, message.MessageID)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("dispatch file mode: info=%v err=%v", info, err)
	}

	conflict := identity
	conflict.DispatchID = "dsp_message_other"
	if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, conflict); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("conflicting dispatch error = %v, want ErrHandoffConflict", err)
	}
	preserved, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, message.MessageID)
	if err != nil || preserved.DispatchID != identity.DispatchID {
		t.Fatalf("first dispatch identity was not preserved: %#v err=%v", preserved, err)
	}
}

func TestSmCovMessageSynchestraDispatchRequiresExactExecutionAuthority(t *testing.T) {
	store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, true, true, true)
	defer func() { _ = lock.Close() }()
	identity := smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)

	if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(nil, request.HandoffID, digest, identity); err == nil || !strings.Contains(err.Error(), "execution lock is required") {
		t.Fatalf("nil lock save error = %v, want execution lock requirement", err)
	}
	if _, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(nil, request.HandoffID, digest, message.MessageID); err == nil || !strings.Contains(err.Error(), "execution lock is required") {
		t.Fatalf("nil lock load error = %v, want execution lock requirement", err)
	}
	if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, "handoff-other", digest, identity); err == nil {
		t.Fatal("save under a lock for a different handoff was accepted")
	}
	if _, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, "handoff-other", digest, message.MessageID); err == nil {
		t.Fatal("load under a lock for a different handoff was accepted")
	}
}

func TestSmCovMessageSynchestraDispatchRequiresDurableRouteReceiptAndMessage(t *testing.T) {
	t.Run("route missing", func(t *testing.T) {
		store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, false, true, true)
		defer func() { _ = lock.Close() }()
		identity := smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)
		if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, identity); err == nil {
			t.Fatal("dispatch save without a durable route was accepted")
		}
		if _, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, message.MessageID); err == nil {
			t.Fatal("dispatch load without a durable route was accepted")
		}
	})

	t.Run("handoff receipt missing", func(t *testing.T) {
		store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, true, false, false)
		defer func() { _ = lock.Close() }()
		identity := smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)
		if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, identity); err == nil || !strings.Contains(err.Error(), "durable handoff receipt") {
			t.Fatalf("save without handoff receipt error = %v", err)
		}
		if _, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, message.MessageID); err == nil || !strings.Contains(err.Error(), "durable handoff receipt") {
			t.Fatalf("load without handoff receipt error = %v", err)
		}
	})

	t.Run("outgoing message missing", func(t *testing.T) {
		store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, true, true, false)
		defer func() { _ = lock.Close() }()
		identity := smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)
		if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, identity); err == nil {
			t.Fatal("dispatch save without a durable outbox message was accepted")
		}
		if _, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, message.MessageID); err == nil {
			t.Fatal("dispatch load without a durable outbox message was accepted")
		}
	})
}

func TestSmCovMessageSynchestraDispatchRefusesIdentityDrift(t *testing.T) {
	store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, true, true, true)
	defer func() { _ = lock.Close() }()
	messageDigest := DigestBytes(raw)
	published, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, smCovSynchIdentity(request, digest, messageDigest, message.MessageID))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*MessageSynchestraDispatch)
	}{
		{"handoff id", func(value *MessageSynchestraDispatch) { value.HandoffID = "handoff-other" }},
		{"request digest", func(value *MessageSynchestraDispatch) { value.RequestDigest = DigestBytes([]byte("other request")) }},
		{"message id", func(value *MessageSynchestraDispatch) { value.MessageID = "message-other" }},
		{"message digest", func(value *MessageSynchestraDispatch) { value.MessageDigest = DigestBytes([]byte("other message")) }},
		{"runner", func(value *MessageSynchestraDispatch) { value.Runner = "other-runner" }},
		{"invocation id", func(value *MessageSynchestraDispatch) { value.InvocationID = "other-invocation" }},
		{"handler", func(value *MessageSynchestraDispatch) { value.Handler = "wb.other.v1" }},
		{"dispatch id", func(value *MessageSynchestraDispatch) { value.DispatchID = "not a dispatch id" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity := smCovSynchIdentity(request, digest, messageDigest, message.MessageID)
			test.mutate(&identity)
			if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, identity); err == nil {
				t.Fatalf("dispatch identity with drifted %s was accepted", test.name)
			}
			loaded, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, message.MessageID)
			if err != nil {
				t.Fatalf("rejected drift must not damage durable state: %v", err)
			}
			if !reflect.DeepEqual(loaded, published) {
				t.Fatalf("durable dispatch changed after rejected %s drift: %#v", test.name, loaded)
			}
		})
	}
}

func TestSmCovMessageSynchestraDispatchReadsRejectCorruptState(t *testing.T) {
	t.Run("undecodable dispatch", func(t *testing.T) {
		store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, true, true, true)
		defer func() { _ = lock.Close() }()
		if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)); err != nil {
			t.Fatal(err)
		}
		path := smCovSynchDispatchPath(store, request, message.MessageID)
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, message.MessageID)
		if err == nil || !strings.Contains(err.Error(), "decode message Synchestra dispatch identity") {
			t.Fatalf("load of undecodable dispatch error = %v", err)
		}
	})

	t.Run("schema-valid but unbound dispatch", func(t *testing.T) {
		store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, true, true, true)
		defer func() { _ = lock.Close() }()
		if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)); err != nil {
			t.Fatal(err)
		}
		// A structurally valid file that decodes but does not bind the exact
		// outbox message and route must be refused by load as well.
		forged := smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)
		forged.SchemaVersion = MessageSynchestraDispatchSchemaVersion
		forged.MessageDigest = DigestBytes([]byte("forged message"))
		forgedRaw, err := marshalJSON(forged)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(smCovSynchDispatchPath(store, request, message.MessageID), forgedRaw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, message.MessageID); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("load of unbound dispatch error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, true, true, true)
		defer func() { _ = lock.Close() }()
		if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)); err != nil {
			t.Fatal(err)
		}
		path := smCovSynchDispatchPath(store, request, message.MessageID)
		external := filepath.Join(t.TempDir(), "dispatch.json")
		if err := os.Rename(path, external); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, path); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, message.MessageID); err == nil {
			t.Fatal("load followed a symlinked dispatch file")
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, true, true, true)
		defer func() { _ = lock.Close() }()
		if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)); err != nil {
			t.Fatal(err)
		}
		path := smCovSynchDispatchPath(store, request, message.MessageID)
		if err := os.Link(path, filepath.Join(t.TempDir(), "dispatch-alias.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, message.MessageID); err == nil {
			t.Fatal("load accepted a multiply-linked dispatch file")
		}
	})

	t.Run("corrupt handoff receipt", func(t *testing.T) {
		store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, true, true, true)
		defer func() { _ = lock.Close() }()
		identity := smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)
		if err := os.WriteFile(filepath.Join(store.Root, request.HandoffID, receiptFileName), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, identity); err == nil {
			t.Fatal("save accepted a corrupt durable handoff receipt")
		}
		if _, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, message.MessageID); err == nil {
			t.Fatal("load accepted a corrupt durable handoff receipt")
		}
	})

	t.Run("corrupt outgoing message record", func(t *testing.T) {
		store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, true, true, true)
		defer func() { _ = lock.Close() }()
		identity := smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)
		recordPath := filepath.Join(store.Root, request.HandoffID, messageOutboxDirName, message.MessageID, messageRecordFileName)
		if err := os.WriteFile(recordPath, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, identity); err == nil {
			t.Fatal("save accepted a corrupt durable outbox record")
		}
		if _, err := store.LoadOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, message.MessageID); err == nil {
			t.Fatal("load accepted a corrupt durable outbox record")
		}
	})
}
