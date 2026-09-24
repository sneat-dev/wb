package sessionmove

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file raised statement coverage of store.go. It forges durable state only
// under t.TempDir() and asserts the observable behaviour of the store (returned
// values, errors, and on-disk state) rather than merely executing lines.

// smCovStore returns a Store rooted at a fresh temporary directory.
func smCovStore(t *testing.T) Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), DirName))
}

func smCovEventTime(seconds int) time.Time {
	return time.Date(2026, 8, 25, 10, 0, seconds, 0, time.UTC)
}

func smCovWriteFile(t *testing.T, path string, raw []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func smCovMkdirAll(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, mode); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func smCovOpenDirectory(t *testing.T, path string) *os.File {
	t.Helper()
	directory, err := os.Open(path)
	if err != nil {
		t.Fatalf("open directory %s: %v", path, err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return directory
}

func smCovOpenRegularFile(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open file %s: %v", path, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func smCovAdmit(t *testing.T, store Store, request Request) ([]byte, Digest) {
	t.Helper()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	digest := DigestBytes(raw)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	return raw, digest
}

func smCovAcquireLock(t *testing.T, store Store, handoffID string, digest Digest) *ExecutionLock {
	t.Helper()
	lock, err := store.AcquireExecutionLock(context.Background(), handoffID, digest)
	if err != nil {
		t.Fatalf("AcquireExecutionLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return lock
}

func smCovEventRaw(t *testing.T, event HandoffEvent) []byte {
	t.Helper()
	raw, err := marshalJSON(event)
	if err != nil {
		t.Fatalf("marshalJSON: %v", err)
	}
	return raw
}

func smCovUnder(store Store, handoffID string, names ...string) string {
	return filepath.Join(append([]string{store.Root, handoffID}, names...)...)
}

func TestSmCovStoreHappyLifecycleCoversEveryExportedPath(t *testing.T) {
	store := smCovStore(t)
	request := validRequestWithInlineHandover("inline handover document\n")
	raw, digest := smCovAdmit(t, store, request)

	if store.Root == "" || PrivateHandoverPath(store.Root, request.HandoffID) != smCovUnder(store, request.HandoffID, HandoverFileName) {
		t.Fatalf("PrivateHandoverPath = %q", PrivateHandoverPath(store.Root, request.HandoffID))
	}

	// A repeated Admit with the exact bytes is a replay without a receipt.
	replayed, err := store.Admit(raw, digest)
	if err != nil || !replayed.Replay || replayed.Request != request || replayed.Digest != digest || replayed.Receipt != nil {
		t.Fatalf("replayed Admit = (%#v, %v)", replayed, err)
	}

	// A fresh handoff with no events directory loads as an empty projection.
	empty, err := store.Load(request.HandoffID)
	if err != nil || empty.Events == nil || len(empty.Events) != 0 || empty.Receipt != nil || empty.Digest != digest {
		t.Fatalf("initial Load = (%#v, %v)", empty, err)
	}

	lock := smCovAcquireLock(t, store, request.HandoffID, digest)

	path, err := store.EnsureHandoverUnderLock(lock, request.HandoffID, digest)
	if err != nil || path != PrivateHandoverPath(store.Root, request.HandoffID) || !filepath.IsAbs(path) {
		t.Fatalf("EnsureHandoverUnderLock = (%q, %v)", path, err)
	}
	content, err := store.ReadHandover(request.HandoffID)
	if err != nil || string(content) != request.HandoverContent {
		t.Fatalf("ReadHandover = (%q, %v)", content, err)
	}

	readmitted, err := store.ReadmitUnderLock(lock, request.HandoffID, digest, raw)
	if err != nil || !readmitted.Replay || readmitted.Request != request || readmitted.Receipt != nil {
		t.Fatalf("ReadmitUnderLock = (%#v, %v)", readmitted, err)
	}

	receipt := validReceipt(request, digest)
	written, receiptReplay, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, receipt)
	if err != nil || receiptReplay || written != receipt {
		t.Fatalf("SaveReceiptUnderLock = (%#v, replay=%t, err=%v)", written, receiptReplay, err)
	}
	if again, replay, err := store.SaveReceipt(request.HandoffID, digest, receipt); err != nil || !replay || again != receipt {
		t.Fatalf("replayed SaveReceipt = (%#v, replay=%t, err=%v)", again, replay, err)
	}

	admitted, err := store.Admit(raw, digest)
	if err != nil || !admitted.Replay || admitted.Receipt == nil || *admitted.Receipt != receipt {
		t.Fatalf("Admit with durable receipt = (%#v, %v)", admitted, err)
	}
	readmitted, err = store.ReadmitUnderLock(lock, request.HandoffID, digest, raw)
	if err != nil || readmitted.Receipt == nil || *readmitted.Receipt != receipt {
		t.Fatalf("ReadmitUnderLock with durable receipt = (%#v, %v)", readmitted, err)
	}

	first, err := store.AppendEventUnderLock(lock, request.HandoffID, digest, HandoffEvent{Phase: PhaseReceived, At: smCovEventTime(1)})
	if err != nil || first.Sequence != 1 || first.SchemaVersion != EventSchemaVersion || first.HandoffID != request.HandoffID || first.RequestDigest != digest {
		t.Fatalf("AppendEventUnderLock = (%#v, %v)", first, err)
	}
	second, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseWorktreeReady, At: smCovEventTime(2), Diagnostic: "  trimmed  "})
	if err != nil || second.Sequence != 2 || second.Diagnostic != "trimmed" {
		t.Fatalf("AppendEvent = (%#v, %v)", second, err)
	}
	completed, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseCompleted, At: smCovEventTime(3)})
	if err != nil || completed.Sequence != 3 {
		t.Fatalf("completed AppendEvent = (%#v, %v)", completed, err)
	}

	state, err := store.Load(request.HandoffID)
	if err != nil || state.Request != request || state.Digest != digest || state.Receipt == nil || *state.Receipt != receipt {
		t.Fatalf("Load = (%#v, %v)", state, err)
	}
	if len(state.Events) != 3 || state.Events[0] != first || state.Events[1] != second || state.Events[2] != completed {
		t.Fatalf("Load events = %#v", state.Events)
	}
	locked, err := store.LoadUnderLock(lock, request.HandoffID, digest)
	if err != nil || len(locked.Events) != 3 || locked.Receipt == nil || *locked.Receipt != receipt {
		t.Fatalf("LoadUnderLock = (%#v, %v)", locked, err)
	}

	// An explicitly created empty events directory also loads as an empty slice.
	emptyStore := smCovStore(t)
	emptyRequest := validRequest()
	_, emptyDigest := smCovAdmit(t, emptyStore, emptyRequest)
	smCovMkdirAll(t, smCovUnder(emptyStore, emptyRequest.HandoffID, eventsDirName), 0o700)
	emptyState, err := emptyStore.Load(emptyRequest.HandoffID)
	if err != nil || emptyState.Events == nil || len(emptyState.Events) != 0 || emptyState.Digest != emptyDigest {
		t.Fatalf("empty events directory Load = (%#v, %v)", emptyState, err)
	}
}

func TestSmCovStoreEnsureHandoverRefusesMissingAuthority(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	request := validRequestWithInlineHandover("inline\n")
	_, digest := smCovAdmit(t, store, request)

	if _, err := store.EnsureHandoverUnderLock(nil, request.HandoffID, digest); err == nil || !strings.Contains(err.Error(), "retain") {
		t.Fatalf("EnsureHandoverUnderLock(nil) error = %v, want retain authority failure", err)
	}

	other := validRequest()
	other.HandoffID = "handoff-smcov-ensure-other"
	_, otherDigest := smCovAdmit(t, store, other)
	lock := smCovAcquireLock(t, store, other.HandoffID, otherDigest)
	if _, err := store.EnsureHandoverUnderLock(lock, request.HandoffID, digest); err == nil || !strings.Contains(err.Error(), "retain") {
		t.Fatalf("EnsureHandoverUnderLock(mismatched lock) error = %v, want retain authority failure", err)
	}

	legacy := validRequest()
	legacy.HandoffID = "handoff-smcov-legacy"
	_, legacyDigest := smCovAdmit(t, store, legacy)
	legacyLock := smCovAcquireLock(t, store, legacy.HandoffID, legacyDigest)
	if _, err := store.EnsureHandoverUnderLock(legacyLock, legacy.HandoffID, legacyDigest); err == nil || !strings.Contains(err.Error(), "no inline handover content") {
		t.Fatalf("EnsureHandoverUnderLock(legacy) error = %v, want missing inline content", err)
	}
}

func TestSmCovStoreReadHandoverRefusesMissingAndInvalidIDs(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	smCovMkdirAll(t, store.Root, 0o700)

	if _, err := store.ReadHandover("handoff-smcov-absent"); err == nil || !strings.Contains(err.Error(), "open handoff") {
		t.Fatalf("ReadHandover(missing) error = %v, want open failure", err)
	}
	if _, err := store.ReadHandover("bad id"); err == nil || !strings.Contains(err.Error(), "handoff_id") {
		t.Fatalf("ReadHandover(invalid id) error = %v, want validation failure", err)
	}
	if _, err := NewStore("").ReadHandover("handoff-123"); err == nil || !strings.Contains(err.Error(), "root is required") {
		t.Fatalf("ReadHandover(empty root) error = %v, want root failure", err)
	}
}

func TestSmCovStoreAdmitRefusals(t *testing.T) {
	t.Parallel()
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	store := smCovStore(t)

	if _, err := store.Admit(raw, Digest("not-a-digest")); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Admit(invalid digest spelling) error = %v, want ErrDigestMismatch", err)
	}
	if _, err := store.Admit(raw, DigestBytes([]byte("different bytes"))); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Admit(mismatched digest) error = %v, want ErrDigestMismatch", err)
	}

	garbage := []byte("{\"handoff_id\": ")
	if _, err := store.Admit(garbage, DigestBytes(garbage)); err == nil || !strings.Contains(err.Error(), "parse session move request") {
		t.Fatalf("Admit(garbage) error = %v, want decode failure", err)
	}

	for _, root := range []string{"", "   "} {
		if _, err := NewStore(root).Admit(raw, digest); err == nil || !strings.Contains(err.Error(), "root is required") {
			t.Fatalf("Admit(root=%q) error = %v, want root failure", root, err)
		}
	}

	// request.json is a directory: Linkat reports EEXIST and the hardened reader
	// then refuses the non-regular durable entry.
	directoryStore := smCovStore(t)
	smCovMkdirAll(t, smCovUnder(directoryStore, request.HandoffID, requestFileName), 0o700)
	if _, err := directoryStore.Admit(raw, digest); err == nil || !strings.Contains(err.Error(), "handoff request") {
		t.Fatalf("Admit(request.json is a directory) error = %v", err)
	}

	// A handoff ID longer than NAME_MAX cannot be created.
	long := validRequest()
	long.HandoffID = strings.Repeat("a", 300)
	longRaw, err := EncodeRequest(long)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := smCovStore(t).Admit(longRaw, DigestBytes(longRaw)); err == nil || !strings.Contains(err.Error(), "create handoff directory") {
		t.Fatalf("Admit(over-long handoff id) error = %v, want mkdirat failure", err)
	}

	// A different request for an already-admitted handoff ID loses to the first
	// winner because the durable request bytes diverge from the presented ones.
	conflictingStore := smCovStore(t)
	smCovAdmit(t, conflictingStore, request)
	conflicting := request
	conflicting.BundleCommit = strings.Repeat("c", 40)
	conflictingRaw, err := EncodeRequest(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conflictingStore.Admit(conflictingRaw, DigestBytes(conflictingRaw)); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("Admit(conflicting bytes) error = %v, want ErrHandoffConflict", err)
	}
	state, err := conflictingStore.Load(request.HandoffID)
	if err != nil || state.Request != request || state.Digest != digest {
		t.Fatalf("first winner changed after conflicting Admit: (%#v, %v)", state, err)
	}
}

func TestSmCovStoreAdmitReplayRefusesCorruptDurableReceipt(t *testing.T) {
	store := smCovStore(t)
	request := validRequest()
	raw, digest := smCovAdmit(t, store, request)
	receipt := validReceipt(request, digest)
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); err != nil {
		t.Fatal(err)
	}
	smCovWriteFile(t, smCovUnder(store, request.HandoffID, receiptFileName), []byte("{ not json"), 0o600)
	if _, err := store.Admit(raw, digest); err == nil || !strings.Contains(err.Error(), "decode durable handoff receipt") {
		t.Fatalf("Admit replay error = %v, want durable receipt decode failure", err)
	}
}

func TestSmCovStoreReadmitUnderLockRefusals(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	request := validRequest()
	raw, digest := smCovAdmit(t, store, request)

	if _, err := store.ReadmitUnderLock(nil, request.HandoffID, Digest("bogus"), raw); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("ReadmitUnderLock(invalid digest) error = %v, want ErrDigestMismatch", err)
	}
	if _, err := store.ReadmitUnderLock(nil, request.HandoffID, DigestBytes([]byte("other")), raw); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("ReadmitUnderLock(mismatched digest) error = %v, want ErrDigestMismatch", err)
	}
	unparsable := []byte("{\"handoff_id\": \"handoff-123\"}")
	if _, err := store.ReadmitUnderLock(nil, request.HandoffID, DigestBytes(unparsable), unparsable); err == nil || !strings.Contains(err.Error(), "session move request") {
		t.Fatalf("ReadmitUnderLock(unparsable request) error = %v", err)
	}
	if _, err := store.ReadmitUnderLock(nil, request.HandoffID, digest, raw); err == nil || !strings.Contains(err.Error(), "execution lock is required") {
		t.Fatalf("ReadmitUnderLock(nil lock) error = %v", err)
	}

	other := validRequest()
	other.HandoffID = "handoff-smcov-readmit-other"
	_, otherDigest := smCovAdmit(t, store, other)
	otherLock := smCovAcquireLock(t, store, other.HandoffID, otherDigest)
	if _, err := store.ReadmitUnderLock(otherLock, request.HandoffID, digest, raw); err == nil || !strings.Contains(err.Error(), "re-admit under exact execution authority") {
		t.Fatalf("ReadmitUnderLock(mismatched lock) error = %v", err)
	}

	smCovWriteFile(t, smCovUnder(store, request.HandoffID, receiptFileName), []byte("{ not json"), 0o600)
	ownLock := smCovAcquireLock(t, store, request.HandoffID, digest)
	if _, err := store.ReadmitUnderLock(ownLock, request.HandoffID, digest, raw); err == nil || !strings.Contains(err.Error(), "decode durable handoff receipt") {
		t.Fatalf("ReadmitUnderLock(corrupt receipt) error = %v", err)
	}
}

func TestSmCovStoreSaveReceiptAndAppendEventRefuseMissingState(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	smCovMkdirAll(t, store.Root, 0o700)
	request := validRequest()
	digest := DigestBytes([]byte("some exact bytes"))
	receipt := Receipt{}
	event := HandoffEvent{Phase: PhaseReceived, At: smCovEventTime(1)}

	if _, _, err := store.SaveReceipt("handoff-smcov-absent", digest, receipt); err == nil || !strings.Contains(err.Error(), "open handoff") {
		t.Fatalf("SaveReceipt(missing) error = %v", err)
	}
	if _, _, err := store.SaveReceipt("bad id", digest, receipt); err == nil || !strings.Contains(err.Error(), "handoff_id") {
		t.Fatalf("SaveReceipt(invalid id) error = %v", err)
	}
	if _, err := store.AppendEvent("handoff-smcov-absent", digest, event); err == nil || !strings.Contains(err.Error(), "open handoff") {
		t.Fatalf("AppendEvent(missing) error = %v", err)
	}
	if _, err := store.AppendEvent("bad id", digest, event); err == nil || !strings.Contains(err.Error(), "handoff_id") {
		t.Fatalf("AppendEvent(invalid id) error = %v", err)
	}
	if _, err := store.AppendEventUnderLock(nil, request.HandoffID, digest, event); err == nil || !strings.Contains(err.Error(), "exact admitted execution authority") {
		t.Fatalf("AppendEventUnderLock(nil lock) error = %v", err)
	}
	if _, _, err := store.SaveReceiptUnderLock(nil, request.HandoffID, digest, receipt); err == nil || !strings.Contains(err.Error(), "exact admitted execution authority") {
		t.Fatalf("SaveReceiptUnderLock(nil lock) error = %v", err)
	}
}

func TestSmCovStoreSaveReceiptAndAppendEventRefuseCorruptRequest(t *testing.T) {
	store := smCovStore(t)
	request := validRequest()
	_, digest := smCovAdmit(t, store, request)
	smCovWriteFile(t, smCovUnder(store, request.HandoffID, requestFileName), []byte("{ not json"), 0o600)
	receipt := validReceipt(request, digest)
	event := HandoffEvent{Phase: PhaseReceived, At: smCovEventTime(1)}

	if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); err == nil || !strings.Contains(err.Error(), "decode durable handoff request") {
		t.Fatalf("SaveReceipt(corrupt request) error = %v", err)
	}
	if _, err := store.AppendEvent(request.HandoffID, digest, event); err == nil || !strings.Contains(err.Error(), "decode durable handoff request") {
		t.Fatalf("AppendEvent(corrupt request) error = %v", err)
	}
}

func TestSmCovStoreSaveReceiptAndAppendEventRefuseWrongDigest(t *testing.T) {
	store := smCovStore(t)
	request := validRequest()
	_, digest := smCovAdmit(t, store, request)
	wrong := DigestBytes([]byte("some other exact request bytes"))
	receipt := validReceipt(request, digest)
	event := HandoffEvent{Phase: PhaseReceived, At: smCovEventTime(1)}

	if _, _, err := store.SaveReceipt(request.HandoffID, wrong, receipt); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("SaveReceipt(wrong digest) error = %v, want ErrHandoffConflict", err)
	}
	if _, err := store.AppendEvent(request.HandoffID, wrong, event); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("AppendEvent(wrong digest) error = %v, want ErrHandoffConflict", err)
	}
	if _, err := store.Load(request.HandoffID); err != nil {
		t.Fatalf("conflict mutated durable state: %v", err)
	}
}

func TestSmCovStoreSaveReceiptAtRejectsInvalidAndConflictingReceipts(t *testing.T) {
	store := smCovStore(t)
	request := validRequest()
	_, digest := smCovAdmit(t, store, request)
	receiptPath := smCovUnder(store, request.HandoffID, receiptFileName)

	invalid := validReceipt(request, digest)
	invalid.TmuxName = "bad tmux name"
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, invalid); err == nil || !strings.Contains(err.Error(), "tmux_name") {
		t.Fatalf("SaveReceipt(invalid receipt) error = %v", err)
	}
	if _, err := os.Stat(receiptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid receipt was persisted: %v", err)
	}

	oversized := validReceipt(request, digest)
	oversized.NativeHarnessID = strings.Repeat("a", maxReceiptBytes)
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("SaveReceipt(oversized receipt) error = %v", err)
	}

	first := validReceipt(request, digest)
	written, replay, err := store.SaveReceipt(request.HandoffID, digest, first)
	if err != nil || replay || written != first {
		t.Fatalf("first SaveReceipt = (%#v, replay=%t, err=%v)", written, replay, err)
	}
	firstRaw, err := EncodeReceipt(first)
	if err != nil {
		t.Fatal(err)
	}

	identical, replay, err := store.SaveReceipt(request.HandoffID, digest, first)
	if err != nil || !replay || identical != first {
		t.Fatalf("identical SaveReceipt = (%#v, replay=%t, err=%v)", identical, replay, err)
	}

	// Structurally different but individually valid: only the first identity
	// wins, every later one is a conflict.
	second := first
	second.AttemptID = "000002-" + strings.Repeat("b", 32)
	second.AttemptIndex = 2
	second.PID = 4321
	second.StartedAt = first.StartedAt.Add(time.Minute)
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, second); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("second SaveReceipt error = %v, want ErrHandoffConflict", err)
	}
	onDisk, err := os.ReadFile(receiptPath)
	if err != nil || !bytes.Equal(onDisk, firstRaw) {
		t.Fatalf("first receipt was replaced: %q, err=%v", onDisk, err)
	}

	decoded, err := DecodeReceipt(onDisk)
	if err != nil || decoded != first {
		t.Fatalf("durable receipt = (%#v, %v)", decoded, err)
	}
}

func TestSmCovStoreAppendEventRejectsInvalidPhaseAndTime(t *testing.T) {
	store := smCovStore(t)
	request := validRequest()
	_, digest := smCovAdmit(t, store, request)

	if _, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: Phase("bogus"), At: smCovEventTime(1)}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("AppendEvent(unsupported phase) error = %v", err)
	}
	if _, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseReceived}); err == nil || !strings.Contains(err.Error(), "time is required") {
		t.Fatalf("AppendEvent(zero time) error = %v", err)
	}
	if _, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseCompleted, At: smCovEventTime(1)}); err == nil || !strings.Contains(err.Error(), "durable receipt") {
		t.Fatalf("AppendEvent(completed without receipt) error = %v", err)
	}
	if _, err := store.AppendEventUnderLock(nil, request.HandoffID, digest, HandoffEvent{Phase: Phase("bogus"), At: smCovEventTime(1)}); err == nil || !strings.Contains(err.Error(), "exact admitted execution authority") {
		t.Fatalf("AppendEventUnderLock(nil lock) error = %v", err)
	}

	if _, _, err := store.SaveReceipt(request.HandoffID, digest, validReceipt(request, digest)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseCompleted, At: smCovEventTime(2)}); err != nil {
		t.Fatalf("receipt-backed completed AppendEvent: %v", err)
	}
	if _, err := store.AppendEvent(request.HandoffID, DigestBytes([]byte("a different exact request")), HandoffEvent{Phase: PhaseCompleted, At: smCovEventTime(3)}); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("second completed AppendEvent error = %v, want ErrHandoffConflict", err)
	}
}

func TestSmCovStoreAppendEventRefusesEventsPathThatIsNotADirectory(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	request := validRequest()
	_, digest := smCovAdmit(t, store, request)
	smCovWriteFile(t, smCovUnder(store, request.HandoffID, eventsDirName), []byte("not a directory"), 0o700)

	if _, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseReceived, At: smCovEventTime(1)}); err == nil || !strings.Contains(err.Error(), "events directory") {
		t.Fatalf("AppendEvent(events is a file) error = %v", err)
	}
	if _, err := store.Load(request.HandoffID); err == nil || !strings.Contains(err.Error(), "events directory") {
		t.Fatalf("Load(events is a file) error = %v", err)
	}
}

func TestSmCovStoreAppendEventRejectsBadEventNames(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		file string
		want string
	}{
		{"invalid", "abc.json", "invalid handoff event name"},
		{"noncanonical", "1.json", "noncanonical handoff event name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := smCovStore(t)
			request := validRequest()
			_, digest := smCovAdmit(t, store, request)
			eventsPath := smCovUnder(store, request.HandoffID, eventsDirName)
			smCovMkdirAll(t, eventsPath, 0o700)
			smCovWriteFile(t, filepath.Join(eventsPath, test.file), []byte("{}"), 0o600)

			if _, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseReceived, At: smCovEventTime(1)}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("AppendEvent error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSmCovStoreAppendEventSkipsNonEventDirectoryEntries(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	request := validRequest()
	_, digest := smCovAdmit(t, store, request)
	eventsPath := smCovUnder(store, request.HandoffID, eventsDirName)
	smCovMkdirAll(t, eventsPath, 0o700)
	smCovMkdirAll(t, filepath.Join(eventsPath, "subdir"), 0o700)
	smCovWriteFile(t, filepath.Join(eventsPath, "notes.txt"), []byte("ignored"), 0o600)

	event, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseOffered, At: smCovEventTime(1)})
	if err != nil || event.Sequence != 1 {
		t.Fatalf("AppendEvent = (%#v, %v), want sequence 1", event, err)
	}
	state, err := store.Load(request.HandoffID)
	if err != nil || len(state.Events) != 1 || state.Events[0] != event {
		t.Fatalf("Load = (%#v, %v)", state, err)
	}
}

func TestSmCovStoreAppendEventGivesUpAfterConcurrentSequenceCollisions(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	request := validRequest()
	_, digest := smCovAdmit(t, store, request)
	eventsPath := smCovUnder(store, request.HandoffID, eventsDirName)
	smCovMkdirAll(t, eventsPath, 0o700)
	// A non-event directory whose name is the next canonical event name is
	// skipped by the sequence scan but still makes Linkat report EEXIST, so the
	// immutable publication never wins.
	for sequence := uint64(1); sequence <= 3; sequence++ {
		smCovWriteFile(t, filepath.Join(eventsPath, eventFileName(sequence)), []byte("{}"), 0o600)
	}
	smCovMkdirAll(t, filepath.Join(eventsPath, eventFileName(4)), 0o700)

	if _, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseReceived, At: smCovEventTime(1)}); err == nil || !strings.Contains(err.Error(), "too many concurrent writers") {
		t.Fatalf("AppendEvent(colliding sequences) error = %v", err)
	}
	entries, err := os.ReadDir(eventsPath)
	if err != nil || len(entries) != 4 {
		t.Fatalf("collision retries published files: %d entries, err=%v", len(entries), err)
	}
}

func TestSmCovStoreAppendEventRejectsOversizedDiagnostic(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	request := validRequest()
	_, digest := smCovAdmit(t, store, request)

	if _, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{
		Phase: PhaseFailed, At: smCovEventTime(1), Diagnostic: strings.Repeat("x", maxEventBytes+1),
	}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("AppendEvent(oversized diagnostic) error = %v", err)
	}
	files, err := filepath.Glob(filepath.Join(smCovUnder(store, request.HandoffID, eventsDirName), "*.json"))
	if err != nil || len(files) != 0 {
		t.Fatalf("oversized event was published: %v, err=%v", files, err)
	}
}

func TestSmCovStoreLoadRefusals(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	smCovMkdirAll(t, store.Root, 0o700)
	request := validRequest()

	if _, err := store.Load("handoff-smcov-absent"); err == nil || !strings.Contains(err.Error(), "open handoff") {
		t.Fatalf("Load(missing) error = %v", err)
	}
	if _, err := store.Load("bad id"); err == nil || !strings.Contains(err.Error(), "handoff_id") {
		t.Fatalf("Load(invalid id) error = %v", err)
	}
	if _, err := NewStore("").Load("handoff-123"); err == nil || !strings.Contains(err.Error(), "root is required") {
		t.Fatalf("Load(empty root) error = %v", err)
	}
	if _, err := NewStore(filepath.Join(t.TempDir(), "absent-root")).Load("handoff-123"); err == nil || !strings.Contains(err.Error(), "open handoff store root") {
		t.Fatalf("Load(missing root) error = %v", err)
	}

	_, digest := smCovAdmit(t, store, request)
	if _, err := store.LoadUnderLock(nil, request.HandoffID, digest); err == nil || !strings.Contains(err.Error(), "execution lock is required") {
		t.Fatalf("LoadUnderLock(nil lock) error = %v", err)
	}
	other := validRequest()
	other.HandoffID = "handoff-smcov-loadlock-other"
	_, otherDigest := smCovAdmit(t, store, other)
	otherLock := smCovAcquireLock(t, store, other.HandoffID, otherDigest)
	if _, err := store.LoadUnderLock(otherLock, request.HandoffID, digest); err == nil || !strings.Contains(err.Error(), "execution lock is for handoff") {
		t.Fatalf("LoadUnderLock(mismatched lock) error = %v", err)
	}
}

func TestSmCovStoreLoadRefusesCompletedEventWithoutReceipt(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	request := validRequest()
	_, digest := smCovAdmit(t, store, request)
	eventsPath := smCovUnder(store, request.HandoffID, eventsDirName)
	smCovMkdirAll(t, eventsPath, 0o700)
	forged := HandoffEvent{
		SchemaVersion: EventSchemaVersion,
		Sequence:      1,
		HandoffID:     request.HandoffID,
		RequestDigest: digest,
		Phase:         PhaseCompleted,
		At:            smCovEventTime(5),
	}
	smCovWriteFile(t, filepath.Join(eventsPath, eventFileName(1)), smCovEventRaw(t, forged), 0o600)

	if _, err := store.Load(request.HandoffID); !errors.Is(err, ErrHandoffConflict) || !strings.Contains(err.Error(), "without an exact durable receipt") {
		t.Fatalf("Load(completed without receipt) error = %v", err)
	}
	lock := smCovAcquireLock(t, store, request.HandoffID, digest)
	if _, err := store.LoadUnderLock(lock, request.HandoffID, digest); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("LoadUnderLock(completed without receipt) error = %v", err)
	}
}

func TestSmCovStoreHandoffDirValidation(t *testing.T) {
	t.Parallel()
	for _, root := range []string{"", "   "} {
		if _, err := NewStore(root).handoffDir("handoff-123"); err == nil || !strings.Contains(err.Error(), "root is required") {
			t.Fatalf("handoffDir(root=%q) error = %v", root, err)
		}
	}
	root := t.TempDir()
	if _, err := NewStore(root).handoffDir("bad id"); err == nil || !strings.Contains(err.Error(), "handoff_id") {
		t.Fatalf("handoffDir(invalid id) error = %v", err)
	}
	directory, err := NewStore(root).handoffDir("handoff-123")
	if err != nil || directory != filepath.Join(root, "handoff-123") {
		t.Fatalf("handoffDir = (%q, %v)", directory, err)
	}
}

func TestSmCovStoreOpenHandoffAndRootRefusals(t *testing.T) {
	t.Parallel()
	if _, err := openHandoffAtRoot(nil, "handoff-123"); err == nil || !strings.Contains(err.Error(), "exact Store root is required") {
		t.Fatalf("openHandoffAtRoot(nil) error = %v", err)
	}
	root := smCovOpenDirectory(t, t.TempDir())
	if _, err := openHandoffAtRoot(root, "bad id"); err == nil || !strings.Contains(err.Error(), "handoff_id") {
		t.Fatalf("openHandoffAtRoot(invalid id) error = %v", err)
	}
	if _, err := openHandoffAtRoot(root, "handoff-smcov-absent"); err == nil || !strings.Contains(err.Error(), "open handoff directory") {
		t.Fatalf("openHandoffAtRoot(missing) error = %v", err)
	}

	for _, input := range []string{"", "   ", "/tmp/smcov-trailing "} {
		if _, err := NewStore(input).openRoot(true); err == nil || !strings.Contains(err.Error(), "root is required") {
			t.Fatalf("openRoot(%q) error = %v", input, err)
		}
	}

	filePath := filepath.Join(t.TempDir(), "regular")
	smCovWriteFile(t, filePath, []byte("x"), 0o600)
	if _, err := NewStore(filepath.Join(filePath, "handoffs")).openRoot(true); err == nil || !strings.Contains(err.Error(), "create handoff store root") {
		t.Fatalf("openRoot(parent is a file) error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := NewStore(missing).openRoot(false); err == nil || !strings.Contains(err.Error(), "open handoff store root") {
		t.Fatalf("openRoot(missing, create=false) error = %v", err)
	}
	if _, err := NewStore(missing).openRoot(true); err != nil {
		t.Fatalf("openRoot(create=true) error = %v", err)
	}
	if info, err := os.Stat(missing); err != nil || !info.IsDir() {
		t.Fatalf("openRoot(create=true) did not create the root: %v", err)
	}
}

func TestSmCovStoreOpenHandoffRefusesRegularFileInPlaceOfDirectory(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	smCovMkdirAll(t, store.Root, 0o700)
	smCovWriteFile(t, smCovUnder(store, request.HandoffID), []byte("a file"), 0o600)

	if _, err := store.Admit(raw, digest); err == nil || !strings.Contains(err.Error(), "open handoff directory") {
		t.Fatalf("Admit(handoff path is a file) error = %v", err)
	}
	if _, err := store.Load(request.HandoffID); err == nil || !strings.Contains(err.Error(), "open handoff directory") {
		t.Fatalf("Load(handoff path is a file) error = %v", err)
	}
}

func TestSmCovStoreRetainHandoffUnderLockRefusals(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	request := validRequest()
	_, digest := smCovAdmit(t, store, request)

	if _, _, err := store.retainHandoffUnderLock(nil, request.HandoffID, digest); err == nil || !strings.Contains(err.Error(), "execution lock is required") {
		t.Fatalf("retainHandoffUnderLock(nil) error = %v", err)
	}

	other := validRequest()
	other.HandoffID = "handoff-smcov-retain-other"
	_, otherDigest := smCovAdmit(t, store, other)
	otherLock := smCovAcquireLock(t, store, other.HandoffID, otherDigest)
	if _, _, err := store.retainHandoffUnderLock(otherLock, request.HandoffID, digest); err == nil || !strings.Contains(err.Error(), "execution lock is for handoff") {
		t.Fatalf("retainHandoffUnderLock(mismatched lock) error = %v", err)
	}

	// The retained descriptor still authenticates the request inode, but the
	// hardened durable reader refuses the widened mode.
	ownLock := smCovAcquireLock(t, store, request.HandoffID, digest)
	if err := os.Chmod(smCovUnder(store, request.HandoffID, requestFileName), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.retainHandoffUnderLock(ownLock, request.HandoffID, digest); err == nil || !strings.Contains(err.Error(), "mode 0600") {
		t.Fatalf("retainHandoffUnderLock(widened mode) error = %v", err)
	}

	// Replacing request.json with a fresh inode breaks the descriptor-based
	// execution authority even though the bytes are identical.
	swappedStore := smCovStore(t)
	swappedRequest := validRequest()
	swappedRaw, swappedDigest := smCovAdmit(t, swappedStore, swappedRequest)
	swappedLock := smCovAcquireLock(t, swappedStore, swappedRequest.HandoffID, swappedDigest)
	replacement := smCovUnder(swappedStore, swappedRequest.HandoffID, "replacement.json")
	smCovWriteFile(t, replacement, swappedRaw, 0o600)
	if err := os.Rename(replacement, smCovUnder(swappedStore, swappedRequest.HandoffID, requestFileName)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := swappedStore.retainHandoffUnderLock(swappedLock, swappedRequest.HandoffID, swappedDigest); err == nil || !strings.Contains(err.Error(), "does not retain the exact admitted handoff directory") {
		t.Fatalf("retainHandoffUnderLock(swapped request inode) error = %v", err)
	}
}

func TestSmCovStoreDurableArtifactCorruptionRefusals(t *testing.T) {
	at := smCovEventTime(1)

	t.Run("append completed with corrupt receipt", func(t *testing.T) {
		t.Parallel()
		store := smCovStore(t)
		request := validRequest()
		_, digest := smCovAdmit(t, store, request)
		smCovWriteFile(t, smCovUnder(store, request.HandoffID, receiptFileName), []byte("{ not json"), 0o600)
		if _, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseCompleted, At: at}); err == nil || !strings.Contains(err.Error(), "decode durable handoff receipt") {
			t.Fatalf("AppendEvent(completed, corrupt receipt) error = %v", err)
		}
	})

	t.Run("load with corrupt request", func(t *testing.T) {
		t.Parallel()
		store := smCovStore(t)
		request := validRequest()
		smCovAdmit(t, store, request)
		smCovWriteFile(t, smCovUnder(store, request.HandoffID, requestFileName), []byte("{ not json"), 0o600)
		if _, err := store.Load(request.HandoffID); err == nil || !strings.Contains(err.Error(), "decode durable handoff request") {
			t.Fatalf("Load(corrupt request) error = %v", err)
		}
	})

	t.Run("load with corrupt receipt", func(t *testing.T) {
		store := smCovStore(t)
		request := validRequest()
		_, digest := smCovAdmit(t, store, request)
		if _, _, err := store.SaveReceipt(request.HandoffID, digest, validReceipt(request, digest)); err != nil {
			t.Fatal(err)
		}
		smCovWriteFile(t, smCovUnder(store, request.HandoffID, receiptFileName), []byte("{ not json"), 0o600)
		if _, err := store.Load(request.HandoffID); err == nil || !strings.Contains(err.Error(), "decode durable handoff receipt") {
			t.Fatalf("Load(corrupt receipt) error = %v", err)
		}
	})

	t.Run("load receipt with widened mode", func(t *testing.T) {
		store := smCovStore(t)
		request := validRequest()
		_, digest := smCovAdmit(t, store, request)
		raw, err := EncodeReceipt(validReceipt(request, digest))
		if err != nil {
			t.Fatal(err)
		}
		smCovWriteFile(t, smCovUnder(store, request.HandoffID, receiptFileName), raw, 0o644)
		handoff := smCovOpenDirectory(t, smCovUnder(store, request.HandoffID))
		if _, _, err := loadReceiptAt(handoff, request, digest); err == nil || !strings.Contains(err.Error(), "mode 0600") {
			t.Fatalf("loadReceiptAt(widened mode) error = %v", err)
		}
		if _, err := store.Load(request.HandoffID); err == nil || !strings.Contains(err.Error(), "mode 0600") {
			t.Fatalf("Load(widened receipt mode) error = %v", err)
		}
	})

	t.Run("save receipt over a directory", func(t *testing.T) {
		store := smCovStore(t)
		request := validRequest()
		_, digest := smCovAdmit(t, store, request)
		// A directory named receipt.json wins the no-replace publication, and
		// the durable reader then refuses the non-regular winner.
		smCovMkdirAll(t, smCovUnder(store, request.HandoffID, receiptFileName), 0o700)
		if _, _, err := store.SaveReceipt(request.HandoffID, digest, validReceipt(request, digest)); err == nil || !strings.Contains(err.Error(), "receipt") {
			t.Fatalf("SaveReceipt(receipt.json is a directory) error = %v", err)
		}
	})
}

func TestSmCovStoreLoadRequestAtRefusals(t *testing.T) {
	t.Parallel()
	store := smCovStore(t)
	request := validRequest()
	smCovAdmit(t, store, request)
	handoff := smCovOpenDirectory(t, smCovUnder(store, request.HandoffID))

	if _, _, _, err := loadRequestAt(handoff, request.HandoffID); err != nil {
		t.Fatalf("baseline loadRequestAt: %v", err)
	}

	smCovWriteFile(t, smCovUnder(store, request.HandoffID, requestFileName), []byte("{ not json"), 0o600)
	if _, _, _, err := loadRequestAt(handoff, request.HandoffID); err == nil || !strings.Contains(err.Error(), "decode durable handoff request") {
		t.Fatalf("loadRequestAt(corrupt) error = %v", err)
	}

	// A well-formed request for a different handoff ID inside this directory.
	moved := validRequest()
	moved.HandoffID = "handoff-smcov-moved"
	movedRaw, err := EncodeRequest(moved)
	if err != nil {
		t.Fatal(err)
	}
	otherStore := smCovStore(t)
	smCovAdmit(t, otherStore, request)
	smCovWriteFile(t, smCovUnder(otherStore, request.HandoffID, requestFileName), movedRaw, 0o600)
	otherHandoff := smCovOpenDirectory(t, smCovUnder(otherStore, request.HandoffID))
	if _, _, _, err := loadRequestAt(otherHandoff, request.HandoffID); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("loadRequestAt(moved request) error = %v, want ErrHandoffConflict", err)
	}
}

func TestSmCovStoreLoadReceiptAtRefusals(t *testing.T) {
	store := smCovStore(t)
	request := validRequest()
	_, digest := smCovAdmit(t, store, request)
	handoff := smCovOpenDirectory(t, smCovUnder(store, request.HandoffID))
	receiptPath := smCovUnder(store, request.HandoffID, receiptFileName)

	missing, raw, err := loadReceiptAt(handoff, request, digest)
	if err != nil || missing != nil || raw != nil {
		t.Fatalf("loadReceiptAt(missing) = (%#v, %q, %v)", missing, raw, err)
	}

	smCovWriteFile(t, receiptPath, []byte("{ not json"), 0o600)
	if _, _, err := loadReceiptAt(handoff, request, digest); err == nil || !strings.Contains(err.Error(), "decode durable handoff receipt") {
		t.Fatalf("loadReceiptAt(corrupt) error = %v", err)
	}

	unbound := validReceipt(request, digest)
	unbound.SuccessorWBSessionID = "wbs-other"
	unbound.TmuxName = "wb-session-wbs-other"
	unboundRaw, err := EncodeReceipt(unbound)
	if err != nil {
		t.Fatal(err)
	}
	smCovWriteFile(t, receiptPath, unboundRaw, 0o600)
	if _, _, err := loadReceiptAt(handoff, request, digest); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("loadReceiptAt(unbound receipt) error = %v, want ErrHandoffConflict", err)
	}
}

func TestSmCovStoreValidateReceiptForRequestFieldMismatches(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	base := validReceipt(request, digest)
	if err := ValidateReceiptForRequest(base, request, digest); err != nil {
		t.Fatalf("baseline ValidateReceiptForRequest: %v", err)
	}

	invalid := base
	invalid.SchemaVersion = ReceiptSchemaVersion + 1
	if err := ValidateReceiptForRequest(invalid, request, digest); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("ValidateReceiptForRequest(invalid receipt) error = %v", err)
	}

	badRequest := request
	badRequest.WorkLogReference = "worklog:effort-123/run-456/not-a-claim"
	if err := ValidateReceiptForRequest(base, badRequest, digest); err == nil || !strings.Contains(err.Error(), "derive target Work Log reference") {
		t.Fatalf("ValidateReceiptForRequest(bad request reference) error = %v", err)
	}

	for name, mutate := range map[string]func(*Receipt){
		"handoff_id":                func(receipt *Receipt) { receipt.HandoffID = "handoff-other" },
		"request_digest":            func(receipt *Receipt) { receipt.RequestDigest = DigestBytes([]byte("another request")) },
		"successor_wb_session_id":   func(receipt *Receipt) { receipt.SuccessorWBSessionID = "wbs-other" },
		"predecessor_wb_session_id": func(receipt *Receipt) { receipt.PredecessorWBSessionID = "wbs-other" },
		"target_machine":            func(receipt *Receipt) { receipt.TargetMachine = "other-machine" },
		"pinned_commit":             func(receipt *Receipt) { receipt.PinnedCommit = strings.Repeat("c", 40) },
		"tmux_name":                 func(receipt *Receipt) { receipt.TmuxName = "wb-session-other" },
		"runtime":                   func(receipt *Receipt) { receipt.Runtime = "claude-code" },
		"model":                     func(receipt *Receipt) { receipt.Model = "different-model" },
		"target_work_log_reference": func(receipt *Receipt) {
			receipt.TargetWorkLogReference = "worklog:effort-123/run-456/" + strings.Repeat("d", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := base
			mutate(&candidate)
			if err := ValidateReceiptForRequest(candidate, request, digest); !errors.Is(err, ErrHandoffConflict) {
				t.Fatalf("ValidateReceiptForRequest(%s) error = %v, want ErrHandoffConflict", name, err)
			}
		})
	}
}

func TestSmCovStoreValidateReceiptForRequestHarnessPolicy(t *testing.T) {
	request := validRequest()
	request.SourceModel = "gpt-5"
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	sameRuntime := validReceipt(request, digest)
	if sameRuntime.Runtime != request.SourceRuntime || sameRuntime.Model != request.SourceModel {
		t.Fatalf("same-runtime receipt = %#v, want source model carried through", sameRuntime)
	}
	if err := ValidateReceiptForRequest(sameRuntime, request, digest); err != nil {
		t.Fatalf("ValidateReceiptForRequest(same runtime): %v", err)
	}

	// A requested harness overrides the source runtime and clears the expected
	// model, because the target model is a fresh harness decision.
	request.RequestedHarness = "claude-code"
	raw, err = EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest = DigestBytes(raw)
	crossHarness := validReceipt(request, digest)
	if crossHarness.Runtime != "claude-code" || crossHarness.Model != "" {
		t.Fatalf("cross-harness receipt = %#v, want runtime claude-code and empty model", crossHarness)
	}
	if err := ValidateReceiptForRequest(crossHarness, request, digest); err != nil {
		t.Fatalf("ValidateReceiptForRequest(requested harness): %v", err)
	}

	withSourceModel := crossHarness
	withSourceModel.Model = request.SourceModel
	if err := ValidateReceiptForRequest(withSourceModel, request, digest); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("ValidateReceiptForRequest(source model under requested harness) error = %v, want ErrHandoffConflict", err)
	}
	wrongRuntime := crossHarness
	wrongRuntime.Runtime = request.SourceRuntime
	if err := ValidateReceiptForRequest(wrongRuntime, request, digest); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("ValidateReceiptForRequest(source runtime under requested harness) error = %v, want ErrHandoffConflict", err)
	}
}

func TestSmCovStoreOpenEventsAtRefusals(t *testing.T) {
	t.Parallel()
	if _, err := openEventsAt(nil, true); err == nil || !strings.Contains(err.Error(), "authority is required") {
		t.Fatalf("openEventsAt(nil, create) error = %v", err)
	}
	if _, err := openEventsAt(nil, false); err == nil || !strings.Contains(err.Error(), "authority is required") {
		t.Fatalf("openEventsAt(nil, read) error = %v", err)
	}

	store := smCovStore(t)
	request := validRequest()
	smCovAdmit(t, store, request)
	handoff := smCovOpenDirectory(t, smCovUnder(store, request.HandoffID))
	if _, err := openEventsAt(handoff, false); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("openEventsAt(missing events) error = %v, want not-exist", err)
	}

	eventsPath := smCovUnder(store, request.HandoffID, eventsDirName)
	smCovMkdirAll(t, eventsPath, 0o755)
	if err := os.Chmod(eventsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openEventsAt(handoff, false); err == nil || !strings.Contains(err.Error(), "not mode 0700") {
		t.Fatalf("openEventsAt(mode 0755) error = %v", err)
	}
	if _, err := store.Load(request.HandoffID); err == nil || !strings.Contains(err.Error(), "not mode 0700") {
		t.Fatalf("Load(mode 0755 events) error = %v", err)
	}

	fileStore := smCovStore(t)
	fileRequest := validRequest()
	smCovAdmit(t, fileStore, fileRequest)
	smCovWriteFile(t, smCovUnder(fileStore, fileRequest.HandoffID, eventsDirName), []byte("regular"), 0o700)
	fileHandoff := smCovOpenDirectory(t, smCovUnder(fileStore, fileRequest.HandoffID))
	if _, err := openEventsAt(fileHandoff, true); err == nil || !strings.Contains(err.Error(), "events directory") {
		t.Fatalf("openEventsAt(events is a file, create) error = %v", err)
	}
	if _, err := openEventsAt(fileHandoff, false); err == nil || !strings.Contains(err.Error(), "events directory") {
		t.Fatalf("openEventsAt(events is a file, read) error = %v", err)
	}
}

func TestSmCovStoreLoadEventsAtRefusals(t *testing.T) {
	t.Parallel()
	request := validRequest()
	at := smCovEventTime(7)
	base := HandoffEvent{
		SchemaVersion: EventSchemaVersion,
		Sequence:      1,
		HandoffID:     request.HandoffID,
		Phase:         PhaseReceived,
		At:            at,
	}
	tests := []struct {
		name  string
		file  string
		raw   func(digest Digest) []byte
		mode  os.FileMode
		extra func(t *testing.T, eventsPath string)
		want  string
	}{
		{
			name: "unsafe mode",
			file: eventFileName(1),
			raw: func(digest Digest) []byte {
				event := base
				event.RequestDigest = digest
				return smCovEventRaw(t, event)
			},
			mode: 0o644,
			want: "mode 0600",
		},
		{
			name: "unparsable",
			file: eventFileName(1),
			raw:  func(Digest) []byte { return []byte("{ not json") },
			mode: 0o600,
			want: "parse handoff event",
		},
		{
			name: "wrong sequence",
			file: eventFileName(1),
			raw: func(digest Digest) []byte {
				event := base
				event.RequestDigest = digest
				event.Sequence = 2
				return smCovEventRaw(t, event)
			},
			mode: 0o600,
			want: "inconsistent with aggregate",
		},
		{
			name: "wrong handoff",
			file: eventFileName(1),
			raw: func(digest Digest) []byte {
				event := base
				event.RequestDigest = digest
				event.HandoffID = "handoff-somewhere-else"
				return smCovEventRaw(t, event)
			},
			mode: 0o600,
			want: "inconsistent with aggregate",
		},
		{
			name: "wrong digest",
			file: eventFileName(1),
			raw: func(Digest) []byte {
				event := base
				event.RequestDigest = DigestBytes([]byte("an unrelated request"))
				return smCovEventRaw(t, event)
			},
			mode: 0o600,
			want: "inconsistent with aggregate",
		},
		{
			name: "unsupported phase",
			file: eventFileName(1),
			raw: func(digest Digest) []byte {
				event := base
				event.RequestDigest = digest
				event.Phase = Phase("bogus")
				return smCovEventRaw(t, event)
			},
			mode: 0o600,
			want: "inconsistent with aggregate",
		},
		{
			name: "zero time",
			file: eventFileName(1),
			raw: func(digest Digest) []byte {
				event := base
				event.RequestDigest = digest
				event.At = time.Time{}
				return smCovEventRaw(t, event)
			},
			mode: 0o600,
			want: "inconsistent with aggregate",
		},
		{
			name: "noncanonical file name",
			file: eventFileName(2),
			raw: func(digest Digest) []byte {
				event := base
				event.RequestDigest = digest
				event.Sequence = 2
				return smCovEventRaw(t, event)
			},
			mode: 0o600,
			want: "inconsistent with aggregate",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := smCovStore(t)
			_, digest := smCovAdmit(t, store, request)
			eventsPath := smCovUnder(store, request.HandoffID, eventsDirName)
			smCovMkdirAll(t, eventsPath, 0o700)
			smCovWriteFile(t, filepath.Join(eventsPath, test.file), test.raw(digest), test.mode)
			if test.extra != nil {
				test.extra(t, eventsPath)
			}
			if _, err := store.Load(request.HandoffID); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load error = %v, want %q", err, test.want)
			}
		})
	}

	// A consistent hand-built event is accepted, proving the refusals above are
	// about the forged defects and not about hand-built state in general.
	store := smCovStore(t)
	_, digest := smCovAdmit(t, store, request)
	eventsPath := smCovUnder(store, request.HandoffID, eventsDirName)
	smCovMkdirAll(t, eventsPath, 0o700)
	consistent := base
	consistent.RequestDigest = digest
	smCovWriteFile(t, filepath.Join(eventsPath, eventFileName(1)), smCovEventRaw(t, consistent), 0o600)
	state, err := store.Load(request.HandoffID)
	if err != nil || len(state.Events) != 1 || state.Events[0] != consistent {
		t.Fatalf("Load(consistent hand-built event) = (%#v, %v)", state, err)
	}
}

func TestSmCovStoreReadEventEntriesRejectsRegularFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "regular.txt")
	smCovWriteFile(t, path, []byte("x"), 0o600)
	if _, err := readEventEntries(smCovOpenRegularFile(t, path)); err == nil || !strings.Contains(err.Error(), "read handoff events") {
		t.Fatalf("readEventEntries(regular file) error = %v", err)
	}
}

func TestSmCovStoreNextEventSequenceAt(t *testing.T) {
	t.Parallel()
	regularPath := filepath.Join(t.TempDir(), "regular.txt")
	smCovWriteFile(t, regularPath, []byte("x"), 0o600)
	if _, err := nextEventSequenceAt(smCovOpenRegularFile(t, regularPath)); err == nil || !strings.Contains(err.Error(), "read handoff events") {
		t.Fatalf("nextEventSequenceAt(regular file) error = %v", err)
	}

	for _, test := range []struct {
		name string
		file string
		want string
	}{
		{"invalid name", "abc.json", "invalid handoff event name"},
		{"noncanonical name", "1.json", "noncanonical handoff event name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			smCovWriteFile(t, filepath.Join(directory, test.file), []byte("{}"), 0o600)
			if _, err := nextEventSequenceAt(smCovOpenDirectory(t, directory)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("nextEventSequenceAt error = %v, want %q", err, test.want)
			}
		})
	}

	directory := t.TempDir()
	smCovMkdirAll(t, filepath.Join(directory, "subdir"), 0o700)
	smCovWriteFile(t, filepath.Join(directory, "notes.txt"), []byte("ignored"), 0o600)
	for _, sequence := range []uint64{1, 7, 3} {
		smCovWriteFile(t, filepath.Join(directory, eventFileName(sequence)), []byte("{}"), 0o600)
	}
	next, err := nextEventSequenceAt(smCovOpenDirectory(t, directory))
	if err != nil || next != 8 {
		t.Fatalf("nextEventSequenceAt = (%d, %v), want 8", next, err)
	}
}

func TestSmCovStoreValidPhase(t *testing.T) {
	t.Parallel()
	for _, phase := range []Phase{PhaseOffered, PhaseReceived, PhaseWorktreeReady, PhaseSuccessorStarted, PhaseCompleted, PhaseFailed, PhaseCancelled} {
		if !validPhase(phase) {
			t.Errorf("validPhase(%q) = false, want true", phase)
		}
	}
	for _, phase := range []Phase{"", "unknown", Phase("Offered"), Phase("COMPLETED")} {
		if validPhase(phase) {
			t.Errorf("validPhase(%q) = true, want false", phase)
		}
	}
}

func TestSmCovStoreReadImmutableAtRefusals(t *testing.T) {
	t.Parallel()
	if _, err := readImmutableAt(nil, "artifact", 1024, "smcov artifact"); err == nil || !strings.Contains(err.Error(), "directory authority is required") {
		t.Fatalf("readImmutableAt(nil) error = %v", err)
	}

	directory := t.TempDir()
	smCovWriteFile(t, filepath.Join(directory, "ok"), []byte("payload"), 0o600)
	smCovWriteFile(t, filepath.Join(directory, "unsafe"), []byte("payload"), 0o644)
	smCovWriteFile(t, filepath.Join(directory, "hard"), []byte("payload"), 0o600)
	if err := os.Link(filepath.Join(directory, "hard"), filepath.Join(directory, "hard2")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "ok"), filepath.Join(directory, "link")); err != nil {
		t.Fatal(err)
	}
	smCovMkdirAll(t, filepath.Join(directory, "targetdir"), 0o700)
	smCovWriteFile(t, filepath.Join(directory, "oversized"), []byte(strings.Repeat("b", 64)), 0o600)

	authority := smCovOpenDirectory(t, directory)
	raw, err := readImmutableAt(authority, "ok", 1024, "smcov artifact")
	if err != nil || string(raw) != "payload" {
		t.Fatalf("readImmutableAt(ok) = (%q, %v)", raw, err)
	}
	if _, err := readImmutableAt(authority, "absent", 1024, "smcov artifact"); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("readImmutableAt(absent) error = %v, want not-exist", err)
	}
	if _, err := readImmutableAt(authority, "hard", 1024, "smcov artifact"); err == nil || !strings.Contains(err.Error(), "single-link") {
		t.Fatalf("readImmutableAt(hard link) error = %v", err)
	}
	if _, err := readImmutableAt(authority, "link", 1024, "smcov artifact"); err == nil || !strings.Contains(err.Error(), "repair interrupted smcov artifact publication") {
		t.Fatalf("readImmutableAt(symlink) error = %v", err)
	}
	if _, err := readImmutableAt(authority, "unsafe", 1024, "smcov artifact"); err == nil || !strings.Contains(err.Error(), "mode 0600") {
		t.Fatalf("readImmutableAt(mode 0644) error = %v", err)
	}
	if _, err := readImmutableAt(authority, "targetdir", 1024, "smcov artifact"); err == nil || !strings.Contains(err.Error(), "single-link") {
		t.Fatalf("readImmutableAt(directory) error = %v", err)
	}
	if _, err := readImmutableAt(authority, "oversized", 8, "smcov artifact"); err == nil || !strings.Contains(err.Error(), "is not one single-link bounded regular mode 0600 file") {
		t.Fatalf("readImmutableAt(beyond limit) error = %v", err)
	}

	regular := smCovOpenRegularFile(t, filepath.Join(directory, "ok"))
	if _, err := readImmutableAt(regular, "ok", 1024, "smcov artifact"); err == nil || !strings.Contains(err.Error(), "repair interrupted smcov artifact publication") {
		t.Fatalf("readImmutableAt(regular file authority) error = %v", err)
	}
}

func TestSmCovStorePublishImmutableAtRefusalsAndFirstWinner(t *testing.T) {
	t.Parallel()
	if _, err := publishImmutableAt(nil, "artifact", []byte("payload"), 0o600); err == nil || !strings.Contains(err.Error(), "directory authority is required") {
		t.Fatalf("publishImmutableAt(nil) error = %v", err)
	}

	directory := t.TempDir()
	authority := smCovOpenDirectory(t, directory)
	for _, name := range []string{"a/b", ".", "..", "../escape"} {
		if _, err := publishImmutableAt(authority, name, []byte("payload"), 0o600); err == nil || !strings.Contains(err.Error(), "one base name") {
			t.Fatalf("publishImmutableAt(name=%q) error = %v", name, err)
		}
	}

	regularPath := filepath.Join(t.TempDir(), "regular")
	smCovWriteFile(t, regularPath, []byte("payload"), 0o600)
	if _, err := publishImmutableAt(smCovOpenRegularFile(t, regularPath), "artifact", []byte("payload"), 0o600); err == nil || !strings.Contains(err.Error(), "immutable temporary file") {
		t.Fatalf("publishImmutableAt(regular file authority) error = %v", err)
	}

	created, err := publishImmutableAt(authority, "artifact", []byte("first winner"), 0o600)
	if err != nil || !created {
		t.Fatalf("first publishImmutableAt = (%t, %v)", created, err)
	}
	again, err := publishImmutableAt(authority, "artifact", []byte("second loser"), 0o600)
	if err != nil || again {
		t.Fatalf("second publishImmutableAt = (%t, %v)", again, err)
	}
	onDisk, err := os.ReadFile(filepath.Join(directory, "artifact"))
	if err != nil || string(onDisk) != "first winner" {
		t.Fatalf("published content = %q, err=%v", onDisk, err)
	}
	info, err := os.Stat(filepath.Join(directory, "artifact"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("published mode = %v, err=%v", info.Mode(), err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".pending-") {
			t.Fatalf("temporary publication name leaked: %s", entry.Name())
		}
	}
}

func TestSmCovStoreIsPendingPublicationName(t *testing.T) {
	t.Parallel()
	if !isPendingPublicationName(".pending-" + strings.Repeat("0", 32)) {
		t.Fatal("isPendingPublicationName refused a canonical pending name")
	}
	if !isPendingPublicationName(".pending-" + "0123456789abcdef0123456789abcdef") {
		t.Fatal("isPendingPublicationName refused a canonical pending name")
	}
	for _, name := range []string{
		"",
		".pending-",
		".pending-abc",
		".pending-" + strings.Repeat("0", 31),
		".pending-" + strings.Repeat("0", 33),
		".pending-" + strings.Repeat("z", 32),
		".pending-" + strings.Repeat("A", 32),
		"pending-" + strings.Repeat("0", 32),
		"x.pending-" + strings.Repeat("0", 32),
	} {
		if isPendingPublicationName(name) {
			t.Errorf("isPendingPublicationName(%q) = true, want false", name)
		}
	}
}

func TestSmCovStoreRepairPendingLinkAtReachableBranches(t *testing.T) {
	t.Parallel()
	pendingName := ".pending-" + strings.Repeat("0", 32)
	unrelatedName := ".pending-" + strings.Repeat("1", 32)

	// An interrupted publication: a hard link to the final name whose second
	// link is a pending name. It is repaired, and an unrelated pending file with
	// a different inode is left alone.
	directory := t.TempDir()
	target := filepath.Join(directory, "artifact")
	smCovWriteFile(t, target, []byte("payload"), 0o600)
	if err := os.Link(target, filepath.Join(directory, pendingName)); err != nil {
		t.Fatal(err)
	}
	smCovWriteFile(t, filepath.Join(directory, unrelatedName), []byte("other"), 0o600)

	raw, err := readImmutableAt(smCovOpenDirectory(t, directory), "artifact", 1024, "smcov artifact")
	if err != nil || string(raw) != "payload" {
		t.Fatalf("readImmutableAt(interrupted) = (%q, %v)", raw, err)
	}
	if _, err := os.Stat(filepath.Join(directory, pendingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending link remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, unrelatedName)); err != nil {
		t.Fatalf("unrelated pending file was removed: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("repaired artifact stat = (%v, %v)", info, err)
	}

	// Only an unrelated pending file: nothing to repair, and it is untouched.
	second := t.TempDir()
	smCovWriteFile(t, filepath.Join(second, "artifact"), []byte("payload"), 0o600)
	smCovWriteFile(t, filepath.Join(second, unrelatedName), []byte("other"), 0o600)
	if _, err := readImmutableAt(smCovOpenDirectory(t, second), "artifact", 1024, "smcov artifact"); err != nil {
		t.Fatalf("readImmutableAt(unrelated pending only) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(second, unrelatedName)); err != nil {
		t.Fatalf("unrelated pending file was removed: %v", err)
	}

	// A second hard link that is not a pending name survives repair, so the
	// repaired final file is still multi-link and the store refuses it.
	third := t.TempDir()
	thirdTarget := filepath.Join(third, "artifact")
	smCovWriteFile(t, thirdTarget, []byte("payload"), 0o600)
	if err := os.Link(thirdTarget, filepath.Join(third, pendingName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(thirdTarget, filepath.Join(third, "sibling")); err != nil {
		t.Fatal(err)
	}
	if _, err := readImmutableAt(smCovOpenDirectory(t, third), "artifact", 1024, "smcov artifact"); err == nil || !strings.Contains(err.Error(), "not single-link after pending-link repair") {
		t.Fatalf("readImmutableAt(unrepairable multi-link) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(third, pendingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repairable pending link remains: %v", err)
	}
}

func TestSmCovStoreReadEventEntriesRejectsUnseekableFile(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})
	if _, err := readEventEntries(reader); err == nil || !strings.Contains(err.Error(), "rewind handoff events directory") {
		t.Fatalf("readEventEntries(pipe) error = %v", err)
	}
}

func TestSmCovStoreRepairPendingLinkAtRefusesUnopenablePendingName(t *testing.T) {
	t.Parallel()
	pendingName := ".pending-" + strings.Repeat("2", 32)
	directory := t.TempDir()
	target := filepath.Join(directory, "artifact")
	smCovWriteFile(t, target, []byte("payload"), 0o600)
	if err := os.Link(target, filepath.Join(directory, "sibling")); err != nil {
		t.Fatal(err)
	}
	// The pending name is a symlink, so O_NOFOLLOW refuses to open it and the
	// repair aborts rather than silently leaving the final file multi-link.
	if err := os.Symlink(target, filepath.Join(directory, pendingName)); err != nil {
		t.Fatal(err)
	}
	if _, err := readImmutableAt(smCovOpenDirectory(t, directory), "artifact", 1024, "smcov artifact"); err == nil || !strings.Contains(err.Error(), "repair interrupted smcov artifact publication") {
		t.Fatalf("readImmutableAt(pending symlink) error = %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("artifact disappeared during failed repair: %v", err)
	}
}
