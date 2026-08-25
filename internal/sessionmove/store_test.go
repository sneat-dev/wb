package sessionmove

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func validRequest() Request {
	return Request{
		SchemaVersion:          RequestSchemaVersion,
		HandoffID:              "handoff-123",
		SuccessorWBSessionID:   "wbs-successor",
		PredecessorWBSessionID: "wbs-source",
		SourceMachine:          "laptop",
		TargetMachine:          "hetzner-vm1",
		RepositoryRemote:       "git@github.com:acme/widgets.git",
		Branch:                 "agent/session-move",
		SourceWorkCommit:       strings.Repeat("a", 40),
		BundleCommit:           strings.Repeat("b", 40),
		HandoverPath:           ".wb/handoffs/handoff-123.md",
		HandoverDigest:         DigestBytes([]byte("handover document")),
		SourceRuntime:          "codex",
		CreatedAt:              time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC),
	}
}

func validReceipt(request Request, digest Digest) Receipt {
	return Receipt{
		SchemaVersion:          ReceiptSchemaVersion,
		HandoffID:              request.HandoffID,
		RequestDigest:          digest,
		SuccessorWBSessionID:   request.SuccessorWBSessionID,
		PredecessorWBSessionID: request.PredecessorWBSessionID,
		TargetMachine:          request.TargetMachine,
		TmuxName:               "wb-session-wbs-successor",
		Runtime:                "codex",
		PinnedCommit:           request.BundleCommit,
		StartedAt:              time.Date(2026, 8, 25, 10, 1, 0, 0, time.UTC),
	}
}

func TestAdmitReplayReturnsExistingReceiptAndRejectsConflictingBytes(t *testing.T) {
	root := filepath.Join(t.TempDir(), DirName)
	store := NewStore(root)
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	digest := DigestBytes(raw)

	first, err := store.Admit(raw, digest)
	if err != nil {
		t.Fatalf("first Admit: %v", err)
	}
	if first.Replay || first.Receipt != nil {
		t.Fatalf("first admission = %+v, want a new handoff without receipt", first)
	}

	wantReceipt := validReceipt(request, digest)
	written, replay, err := store.SaveReceipt(request.HandoffID, digest, wantReceipt)
	if err != nil {
		t.Fatalf("SaveReceipt: %v", err)
	}
	if replay || written.SuccessorWBSessionID != wantReceipt.SuccessorWBSessionID {
		t.Fatalf("first receipt write = (%+v, replay=%t)", written, replay)
	}

	again, err := store.Admit(raw, digest)
	if err != nil {
		t.Fatalf("replayed Admit: %v", err)
	}
	if !again.Replay || again.Receipt == nil || *again.Receipt != wantReceipt {
		t.Fatalf("replayed admission = %+v, want existing receipt %+v", again, wantReceipt)
	}

	_, receiptReplay, err := store.SaveReceipt(request.HandoffID, digest, wantReceipt)
	if err != nil || !receiptReplay {
		t.Fatalf("replayed SaveReceipt = replay %t, err %v", receiptReplay, err)
	}
	conflictingReceipt := wantReceipt
	conflictingReceipt.TmuxName = "wb-session-another-successor"
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, conflictingReceipt); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("conflicting SaveReceipt error = %v, want ErrHandoffConflict", err)
	}

	conflicting := request
	conflicting.BundleCommit = strings.Repeat("c", 40)
	conflictingRaw, err := EncodeRequest(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(conflictingRaw, DigestBytes(conflictingRaw)); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("conflicting Admit error = %v, want ErrHandoffConflict", err)
	}

	state, err := store.Load(request.HandoffID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if state.Digest != digest || state.Request.BundleCommit != request.BundleCommit || state.Receipt == nil || *state.Receipt != wantReceipt {
		t.Fatalf("state changed after conflict: %+v", state)
	}
}

func TestConcurrentIdenticalAdmissionsCreateOneRequest(t *testing.T) {
	root := filepath.Join(t.TempDir(), DirName)
	store := NewStore(root)
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)

	const callers = 16
	start := make(chan struct{})
	results := make(chan Admission, callers)
	errorsFound := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			admission, err := store.Admit(raw, digest)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- admission
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("Admit: %v", err)
	}
	newCount, replayCount := 0, 0
	for result := range results {
		if result.Replay {
			replayCount++
		} else {
			newCount++
		}
	}
	if newCount != 1 || replayCount != callers-1 {
		t.Fatalf("admissions = %d new, %d replay; want 1 new, %d replay", newCount, replayCount, callers-1)
	}
	requestFiles, err := filepath.Glob(filepath.Join(root, request.HandoffID, "request*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(requestFiles) != 1 {
		t.Fatalf("request files = %v, want exactly one", requestFiles)
	}
}

func TestAdmitRejectsDigestMismatchWithoutCreatingHandoff(t *testing.T) {
	root := filepath.Join(t.TempDir(), DirName)
	store := NewStore(root)
	raw, err := EncodeRequest(validRequest())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Admit(raw, DigestBytes([]byte("different bytes"))); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Admit error = %v, want ErrDigestMismatch", err)
	}
	if _, err := os.Stat(filepath.Join(root, validRequest().HandoffID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched digest created state: stat error = %v", err)
	}
}

func TestHandoffEventsAreAppendOnlyAndOrdered(t *testing.T) {
	root := filepath.Join(t.TempDir(), DirName)
	store := NewStore(root)
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}

	firstAt := time.Date(2026, 8, 25, 10, 0, 1, 0, time.UTC)
	first, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseReceived, At: firstAt})
	if err != nil {
		t.Fatalf("AppendEvent received: %v", err)
	}
	second, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseFailed, At: firstAt.Add(time.Second), Diagnostic: "tmux unavailable"})
	if err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("event sequences = %d, %d; want 1, 2", first.Sequence, second.Sequence)
	}

	state, err := store.Load(request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 2 || state.Events[0] != first || state.Events[1] != second {
		t.Fatalf("events = %+v, want the two immutable records", state.Events)
	}

	eventFiles, err := filepath.Glob(filepath.Join(root, request.HandoffID, "events", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(eventFiles) != 2 {
		t.Fatalf("event files = %v, want one file per append", eventFiles)
	}
}

func TestVersionedProtocolTypesRejectNewerSchemas(t *testing.T) {
	request := validRequest()
	request.SchemaVersion = RequestSchemaVersion + 1
	requestRaw, err := marshalJSON(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRequest(requestRaw); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("DecodeRequest error = %v", err)
	}

	receipt := validReceipt(validRequest(), DigestBytes([]byte("request")))
	receipt.SchemaVersion = ReceiptSchemaVersion + 1
	receiptRaw, err := marshalJSON(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReceipt(receiptRaw); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("DecodeReceipt error = %v", err)
	}

	message := Message{
		SchemaVersion:        MessageSchemaVersion + 1,
		MessageID:            "message-1",
		RecipientWBSessionID: "wbs-successor",
		Kind:                 MessageKindText,
		Body:                 "continue",
		SentAt:               time.Now().UTC(),
	}
	messageRaw, err := marshalJSON(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeMessage(messageRaw); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("DecodeMessage error = %v", err)
	}
}
