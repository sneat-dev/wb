package sessionpark

import (
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/unixcompat"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTargetAdmissionRejectsDecodedUnencodableTimestamp(t *testing.T) {
	t.Parallel()
	raw := targetEnvelopeForTest(t)
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	request := value["request"].(map[string]any)
	request["created_at"] = "2026-01-01T00:00:00+24:00"
	malformed, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEnvelope(malformed); err != nil {
		t.Fatalf("fixture must reach canonical encoding: %v", err)
	}
	root := filepath.Join(t.TempDir(), "store")
	if _, err := NewTargetStore(root).Admit(malformed); err == nil || !strings.Contains(err.Error(), "timezone hour outside of range") {
		t.Fatalf("admit = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("invalid envelope created root: %v", err)
	}
}

func TestTargetOperationsRefuseMissingEventsDirectory(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	lock := spCovTargetLock(t, store, admission)
	if err := os.Remove(filepath.Join(store.Root, admission.Envelope.Request.ResumeID, targetEventsDirName)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEventUnderLock(lock, admission.Envelope.Request, admission.Digest, "received", time.Unix(100, 0)); err == nil {
		t.Fatal("append accepted missing events")
	}
	if _, err := store.EventsUnderLock(lock, admission.Envelope.Request, admission.Digest); err == nil {
		t.Fatal("read accepted missing events")
	}
}

func TestTargetContextPublicationPreservesOccupiedArtifact(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	lock := spCovTargetLock(t, store, admission)
	receipt := spCovTargetReceipt(t, admission)
	path := filepath.Join(store.Root, admission.Envelope.Request.ResumeID, SuccessorContextFileName)
	original := []byte("occupied continuation\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnsureSuccessorContextUnderLock(lock, admission.Envelope.Request, admission.Digest, receipt.Members); err == nil {
		t.Fatal("conflicting context accepted")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != string(original) {
		t.Fatalf("occupied context changed: %q, %v", raw, err)
	}
}

func TestTargetReceiptRejectsDecodedUnencodableTimestamp(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	lock := spCovTargetLock(t, store, admission)
	receipt := spCovTargetReceipt(t, admission)
	raw, err := EncodeReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	value["started_at"] = "2026-01-01T00:00:00+24:00"
	malformed, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReceipt(malformed); err != nil {
		t.Fatalf("fixture must reach canonical encoding: %v", err)
	}
	path := filepath.Join(store.Root, admission.Envelope.Request.ResumeID, targetReceiptFileName)
	if err := os.WriteFile(path, malformed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveReceiptUnderLock(lock, admission.Envelope.Request, admission.Digest, receipt); err == nil {
		t.Fatal("unencodable competing receipt accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(malformed) {
		t.Fatalf("competing receipt changed: %q, %v", after, err)
	}
}

func TestTargetAdmissionPreservesAdmitLockRefusal(t *testing.T) {
	t.Parallel()
	store := NewTargetStore(filepath.Join(t.TempDir(), "store"))
	refused := errors.New("admit flock refused")
	result, err := store.admitWithOperations(targetEnvelopeForTest(t), nil, unix.Mkdirat, unix.Openat, func(fd, flags int) error { return refused })
	if !errors.Is(err, refused) || result.Digest != "" {
		t.Fatalf("admit = %#v, %v", result, err)
	}
}

func TestExecutionLockSurvivesCompetingCreation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directoryFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	directory := os.NewFile(uintptr(directoryFD), root)
	t.Cleanup(func() { _ = directory.Close() })
	var competing *os.File
	calls := 0
	fd, err := openTargetLockWithOpen(directoryFD, func(parent int, name string, flags int, mode uint32) (int, error) {
		calls++
		if flags&unix.O_EXCL != 0 {
			created, err := unix.Openat(parent, name, flags, mode)
			if err != nil {
				t.Fatal(err)
			}
			competing = os.NewFile(uintptr(created), "competing-execution-lock")
			t.Cleanup(func() { _ = competing.Close() })
		}
		return unix.Openat(parent, name, flags, mode)
	})
	if err != nil {
		t.Fatal(err)
	}
	held := os.NewFile(uintptr(fd), "acquired-execution-lock")
	t.Cleanup(func() { _ = held.Close() })
	if calls != 3 || competing == nil || !sameFile(held, competing) {
		t.Fatalf("lock did not retain competing inode: calls=%d", calls)
	}
}

func TestTargetPublicationPreservesWriteRefusals(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	lock := spCovTargetLock(t, store, admission)
	request := admission.Envelope.Request
	refused := errors.New("target publication refused")
	receipt := spCovTargetReceipt(t, admission)
	if _, _, err := store.saveReceiptUnderLockInjected(lock, request, admission.Digest, receipt, &filewrite.Injector{Step: filewrite.StepWrite, Err: refused}); !errors.Is(err, refused) {
		t.Fatalf("receipt write = %v", err)
	}
	if _, err := store.appendEventUnderLockInjected(lock, request, admission.Digest, "received", time.Unix(100, 0), &filewrite.Injector{Step: filewrite.StepWrite, Err: refused}); !errors.Is(err, refused) {
		t.Fatalf("event write = %v", err)
	}
}

func TestTargetReceiptRefusesMalformedCompetingPublication(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	lock := spCovTargetLock(t, store, admission)
	path := filepath.Join(store.Root, admission.Envelope.Request.ResumeID, targetReceiptFileName)
	if err := os.WriteFile(path, []byte("{malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveReceiptUnderLock(lock, admission.Envelope.Request, admission.Digest, spCovTargetReceipt(t, admission)); err == nil || !strings.Contains(err.Error(), "load durable") {
		t.Fatalf("malformed competing receipt = %v", err)
	}
}

func TestTargetEventEncodingFailurePreservesDurableHistory(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	lock := spCovTargetLock(t, store, admission)
	request := admission.Envelope.Request
	original, err := store.AppendEventUnderLock(lock, request, admission.Digest, "received", time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(store.Root, request.ResumeID, targetEventsDirName)
	first := filepath.Join(events, "00000000000000000001.json")
	before, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	publications := 0
	candidate, err := store.appendEventUnderLockInjected(lock, request, admission.Digest, "finalized", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), &filewrite.Injector{Step: filewrite.StepWrite, Hook: func() { publications++ }})
	var encodingErr *json.MarshalerError
	if !errors.As(err, &encodingErr) || !strings.Contains(err.Error(), wantYearOutOfRangeSubstring) || candidate.Sequence != 0 {
		t.Fatalf("event encoding = %#v, %v", candidate, err)
	}
	if publications != 0 {
		t.Fatalf("invalid event reached publication %d times", publications)
	}
	after, err := os.ReadFile(first)
	if err != nil || string(after) != string(before) {
		t.Fatalf("durable event changed: %q, %v", after, err)
	}
	if _, err := os.Stat(filepath.Join(events, "00000000000000000002.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid event artifact exists: %v", err)
	}
	history, err := store.EventsUnderLock(lock, request, admission.Digest)
	if err != nil || len(history) != 1 || history[0] != original {
		t.Fatalf("history = %#v, %v", history, err)
	}
	if !lock.HeldForSession(store.Root, request.ResumeID, string(admission.Digest)) {
		t.Fatal("encoding failure discarded retained authority")
	}
}
