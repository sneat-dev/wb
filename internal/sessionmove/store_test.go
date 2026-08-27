package sessionmove

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func validRequest() Request {
	const sourceOfferMessage = "Session handoff offered"
	const sourceOfferNextAction = "Continue from the immutable handover document"
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
		WorkLogReference:       "worklog:effort-123/run-456/" + strings.Repeat("c", 64),
		SourceOfferMessage:     sourceOfferMessage,
		SourceOfferNextAction:  sourceOfferNextAction,
		SourceOfferDigest:      DigestSourceOffer(sourceOfferMessage, sourceOfferNextAction),
		CreatedAt:              time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC),
	}
}

func validReceipt(request Request, digest Digest) Receipt {
	runtime := strings.TrimSpace(request.RequestedHarness)
	if runtime == "" {
		runtime = strings.TrimSpace(request.SourceRuntime)
	}
	model := ""
	if runtime == strings.TrimSpace(request.SourceRuntime) {
		model = strings.TrimSpace(request.SourceModel)
	}
	return Receipt{
		SchemaVersion:          ReceiptSchemaVersion,
		HandoffID:              request.HandoffID,
		RequestDigest:          digest,
		SuccessorWBSessionID:   request.SuccessorWBSessionID,
		PredecessorWBSessionID: request.PredecessorWBSessionID,
		TargetMachine:          request.TargetMachine,
		TmuxName:               "wb-session-wbs-successor",
		Runtime:                runtime,
		Model:                  model,
		AttemptID:              "000001-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		AttemptIndex:           1,
		PID:                    1234,
		TargetWorkLogReference: mustExpectedTargetWorkLogReference(request, digest),
		PinnedCommit:           request.BundleCommit,
		StartedAt:              time.Date(2026, 8, 25, 10, 1, 0, 0, time.UTC),
	}
}

func mustExpectedTargetWorkLogReference(request Request, digest Digest) string {
	reference, err := ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		panic(err)
	}
	return reference.String()
}

func TestWorkLogReferenceStrictRoundTrip(t *testing.T) {
	const raw = "worklog:effort-123/run_456/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reference, err := ParseWorkLogReference(raw)
	if err != nil {
		t.Fatal(err)
	}
	if reference.EffortID != "effort-123" || reference.RunID != "run_456" ||
		reference.ClaimID != strings.Repeat("a", 64) || reference.String() != raw {
		t.Fatalf("parsed reference = %#v, string = %q", reference, reference.String())
	}

	invalid := []string{
		"", "worklog:", "worklog:effort/run", "worklog:effort/run/claim/extra",
		"WORKLOG:effort/run/" + strings.Repeat("a", 64),
		"worklog:bad effort/run/" + strings.Repeat("a", 64),
		"worklog:./run/" + strings.Repeat("a", 64),
		"worklog:effort/../" + strings.Repeat("a", 64),
		"worklog:effort/bad/run/" + strings.Repeat("a", 64),
		"worklog:effort/run/" + strings.Repeat("A", 64),
		"worklog:effort/run/" + strings.Repeat("a", 63),
	}
	for _, value := range invalid {
		if _, err := ParseWorkLogReference(value); err == nil {
			t.Errorf("ParseWorkLogReference(%q) succeeded", value)
		}
	}
}

func TestRequestCarriesExactBoundedSourceOfferContent(t *testing.T) {
	message, nextAction := NormalizeSourceOfferContent("  Ready to move\n", "\nContinue on target  ")
	if message != "Ready to move" || nextAction != "Continue on target" {
		t.Fatalf("normalized source offer = (%q, %q)", message, nextAction)
	}
	digest := DigestSourceOffer("  Ready to move\n", "\nContinue on target  ")
	if digest != DigestSourceOffer(message, nextAction) {
		t.Fatalf("source offer digest changed after normalization: %s", digest)
	}
	if digest != "sha256:8df986a2b84f6ca7a890482fd081c3ac2e65f70af6e9521607babcdb2609d138" {
		t.Fatalf("source offer digest = %s", digest)
	}

	base := validRequest()
	tests := []struct {
		name   string
		mutate func(*Request)
		want   string
	}{
		{"missing message", func(request *Request) { request.SourceOfferMessage = "" }, "source_offer_message"},
		{"unnormalized message", func(request *Request) { request.SourceOfferMessage = " " + request.SourceOfferMessage }, "source_offer_message"},
		{"oversized message", func(request *Request) {
			request.SourceOfferMessage = strings.Repeat("m", MaxSourceOfferFieldBytes+1)
			request.SourceOfferDigest = DigestSourceOffer(request.SourceOfferMessage, request.SourceOfferNextAction)
		}, "source_offer_message"},
		{"missing next action", func(request *Request) { request.SourceOfferNextAction = "" }, "source_offer_next_action"},
		{"unnormalized next action", func(request *Request) { request.SourceOfferNextAction += "\n" }, "source_offer_next_action"},
		{"oversized next action", func(request *Request) {
			request.SourceOfferNextAction = strings.Repeat("n", MaxSourceOfferFieldBytes+1)
			request.SourceOfferDigest = DigestSourceOffer(request.SourceOfferMessage, request.SourceOfferNextAction)
		}, "source_offer_next_action"},
		{"invalid digest", func(request *Request) { request.SourceOfferDigest = "sha256:not-a-digest" }, "source_offer_digest"},
		{"mismatched digest", func(request *Request) { request.SourceOfferDigest = DigestBytes([]byte("other offer")) }, "source_offer_digest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.mutate(&request)
			if _, err := EncodeRequest(request); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("EncodeRequest error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRequestRefusesCompletedSuccessorIndexNamespaceAsHandoffID(t *testing.T) {
	request := validRequest()
	request.HandoffID = successorAddressesDirName
	if _, err := EncodeRequest(request); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("EncodeRequest reserved handoff ID error = %v", err)
	}
}

func TestExpectedTargetWorkLogReferenceIsDeterministicAndPreservesRun(t *testing.T) {
	claimID, err := ExternalHandoffClaimID(Digest("sha256:"+strings.Repeat("a", 64)), "wbs-successor")
	if err != nil {
		t.Fatal(err)
	}
	if claimID != "a89608997a66ad75715ed0bb5ebd88b263353e9dd4821435b9f76c03110f9911" {
		t.Fatalf("external claim ID = %q", claimID)
	}

	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	first, err := ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.EffortID != "effort-123" || first.RunID != "run-456" || len(first.ClaimID) != 64 {
		t.Fatalf("target references = %#v and %#v", first, second)
	}
	changedDigest, err := ExpectedTargetWorkLogReference(request, DigestBytes([]byte("other exact request")))
	if err != nil {
		t.Fatal(err)
	}
	if changedDigest.ClaimID == first.ClaimID {
		t.Fatal("exact request digest did not affect external claim ID")
	}
	changedSuccessor := request
	changedSuccessor.SuccessorWBSessionID = "wbs-other"
	changedRaw, err := EncodeRequest(changedSuccessor)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := ExpectedTargetWorkLogReference(changedSuccessor, DigestBytes(changedRaw))
	if err != nil {
		t.Fatal(err)
	}
	if changed.ClaimID == first.ClaimID {
		t.Fatal("successor WB session ID did not affect external claim ID")
	}
}

func TestNoncanonicalExactRequestDigestBindsTargetReferenceAndReceipt(t *testing.T) {
	request := validRequest()
	canonical, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	noncanonical := append([]byte(" \n\t"), canonical...)
	digest := DigestBytes(noncanonical)
	decoded, err := DecodeRequest(noncanonical)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := ExpectedTargetWorkLogReference(decoded, digest)
	if err != nil {
		t.Fatal(err)
	}
	canonicalExpected, err := ExpectedTargetWorkLogReference(request, DigestBytes(canonical))
	if err != nil {
		t.Fatal(err)
	}
	if expected.ClaimID == canonicalExpected.ClaimID {
		t.Fatal("noncanonical exact bytes reused canonical request claim identity")
	}
	store := NewStore(filepath.Join(t.TempDir(), DirName))
	if _, err := store.Admit(noncanonical, digest); err != nil {
		t.Fatal(err)
	}
	receipt := validReceipt(decoded, digest)
	if receipt.TargetWorkLogReference != expected.String() {
		t.Fatalf("receipt target ref = %q, want %q", receipt.TargetWorkLogReference, expected.String())
	}
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); err != nil {
		t.Fatal(err)
	}
}

func TestRequestAndReceiptRequireStrictWorkLogReferences(t *testing.T) {
	for _, value := range []string{"", "worklog:effort/run/not-a-claim"} {
		request := validRequest()
		request.WorkLogReference = value
		if _, err := EncodeRequest(request); err == nil || !strings.Contains(err.Error(), "work_log_reference") {
			t.Fatalf("EncodeRequest work_log_reference %q error = %v", value, err)
		}
	}

	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	receipt := validReceipt(request, DigestBytes(raw))
	for _, value := range []string{"", "worklog:effort/run/not-a-claim"} {
		candidate := receipt
		candidate.TargetWorkLogReference = value
		if _, err := EncodeReceipt(candidate); err == nil || !strings.Contains(err.Error(), "target_work_log_reference") {
			t.Fatalf("EncodeReceipt target_work_log_reference %q error = %v", value, err)
		}
	}
	for name, mutate := range map[string]func(*Receipt){
		"missing attempt":   func(candidate *Receipt) { candidate.AttemptID = "" },
		"uppercase attempt": func(candidate *Receipt) { candidate.AttemptID = "000001-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" },
		"wrong index":       func(candidate *Receipt) { candidate.AttemptIndex = 2 },
		"missing pid":       func(candidate *Receipt) { candidate.PID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := receipt
			mutate(&candidate)
			if _, err := EncodeReceipt(candidate); err == nil {
				t.Fatal("EncodeReceipt accepted invalid winning launch attempt identity")
			}
		})
	}
}

func TestSaveReceiptRequiresDeterministicTargetWorkLogReference(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), DirName))
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	receipt := validReceipt(request, digest)
	receipt.TargetWorkLogReference = "worklog:effort-123/run-456/" + strings.Repeat("d", 64)
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("SaveReceipt error = %v, want ErrHandoffConflict", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, request.HandoffID, receiptFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched receipt was persisted: %v", err)
	}
}

func TestSaveAndLoadReceiptBindLaunchPolicy(t *testing.T) {
	request := validRequest()
	request.SourceModel = "gpt-5"
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	base := validReceipt(request, digest)
	mutations := map[string]func(*Receipt){
		"tmux":    func(receipt *Receipt) { receipt.TmuxName = "wb-session-other" },
		"runtime": func(receipt *Receipt) { receipt.Runtime = "claude-code" },
		"model":   func(receipt *Receipt) { receipt.Model = "different-model" },
	}
	for name, mutate := range mutations {
		t.Run("save-"+name, func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), DirName))
			if _, err := store.Admit(raw, digest); err != nil {
				t.Fatal(err)
			}
			candidate := base
			mutate(&candidate)
			if _, _, err := store.SaveReceipt(request.HandoffID, digest, candidate); !errors.Is(err, ErrHandoffConflict) {
				t.Fatalf("SaveReceipt error = %v, want ErrHandoffConflict", err)
			}
		})
		t.Run("load-"+name, func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), DirName))
			if _, err := store.Admit(raw, digest); err != nil {
				t.Fatal(err)
			}
			candidate := base
			mutate(&candidate)
			candidateRaw, err := EncodeReceipt(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store.Root, request.HandoffID, receiptFileName), candidateRaw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(request.HandoffID); !errors.Is(err, ErrHandoffConflict) {
				t.Fatalf("Load error = %v, want ErrHandoffConflict", err)
			}
		})
	}

	crossHarness := request
	crossHarness.RequestedHarness = "claude-code"
	crossRaw, err := EncodeRequest(crossHarness)
	if err != nil {
		t.Fatal(err)
	}
	crossDigest := DigestBytes(crossRaw)
	store := NewStore(filepath.Join(t.TempDir(), DirName))
	if _, err := store.Admit(crossRaw, crossDigest); err != nil {
		t.Fatal(err)
	}
	crossReceipt := validReceipt(crossHarness, crossDigest)
	crossReceipt.Model = request.SourceModel
	if _, _, err := store.SaveReceipt(crossHarness.HandoffID, crossDigest, crossReceipt); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("cross-harness source-model SaveReceipt error = %v, want ErrHandoffConflict", err)
	}
}

func TestReceiptStorageRejectsSymlinkHardlinkAndUnsafeMode(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	receipt := validReceipt(request, digest)
	receiptRaw, err := EncodeReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		install func(t *testing.T, receiptPath string)
	}{
		{
			name: "symlink",
			install: func(t *testing.T, receiptPath string) {
				external := filepath.Join(t.TempDir(), "external-receipt.json")
				if err := os.WriteFile(external, receiptRaw, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, receiptPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hardlink",
			install: func(t *testing.T, receiptPath string) {
				external := filepath.Join(t.TempDir(), "external-receipt.json")
				if err := os.WriteFile(external, receiptRaw, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(external, receiptPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unsafe-mode",
			install: func(t *testing.T, receiptPath string) {
				if err := os.WriteFile(receiptPath, receiptRaw, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), DirName))
			if _, err := store.Admit(raw, digest); err != nil {
				t.Fatal(err)
			}
			receiptPath := filepath.Join(store.Root, request.HandoffID, receiptFileName)
			test.install(t, receiptPath)
			if _, err := store.Load(request.HandoffID); err == nil || !strings.Contains(err.Error(), "receipt") {
				t.Fatalf("Load error = %v, want unsafe receipt refusal", err)
			}
			if _, err := store.Admit(raw, digest); err == nil || !strings.Contains(err.Error(), "receipt") {
				t.Fatalf("Admit replay error = %v, want unsafe receipt refusal", err)
			}
		})
	}
}

func TestSaveReceiptUnderLockRequiresAndRetainsExactAuthority(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	root := filepath.Join(t.TempDir(), DirName)
	store := NewStore(root)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	receipt := validReceipt(request, digest)
	if _, _, err := store.SaveReceiptUnderLock(nil, request.HandoffID, digest, receipt); err == nil {
		t.Fatal("SaveReceiptUnderLock accepted nil execution authority")
	}

	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	written, replay, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, receipt)
	if err != nil || replay || written != receipt {
		t.Fatalf("SaveReceiptUnderLock = (%#v, replay=%t, err=%v)", written, replay, err)
	}
	info, err := os.Stat(filepath.Join(root, request.HandoffID, receiptFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %v", info.Mode())
	}
	if _, replay, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, receipt); err != nil || !replay {
		t.Fatalf("SaveReceiptUnderLock replay = %t, err %v", replay, err)
	}
}

func TestReadmitUnderLockReturnsExactReceiptWithoutPathMutation(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	store := NewStore(filepath.Join(t.TempDir(), DirName))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()

	readmitted, err := store.ReadmitUnderLock(lock, request.HandoffID, digest, raw)
	if err != nil || !readmitted.Replay || readmitted.Request != request || readmitted.Digest != digest || readmitted.Receipt != nil {
		t.Fatalf("first ReadmitUnderLock = %#v, err=%v", readmitted, err)
	}
	receipt := validReceipt(request, digest)
	if _, _, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, receipt); err != nil {
		t.Fatal(err)
	}
	readmitted, err = store.ReadmitUnderLock(lock, request.HandoffID, digest, raw)
	if err != nil || readmitted.Receipt == nil || *readmitted.Receipt != receipt {
		t.Fatalf("receipt ReadmitUnderLock = %#v, err=%v", readmitted, err)
	}
	if _, err := store.ReadmitUnderLock(lock, request.HandoffID, digest, append(append([]byte(nil), raw...), '\n')); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("mutated ReadmitUnderLock error = %v, want ErrDigestMismatch", err)
	}
}

func TestReadmitUnderLockRefusesPathSwapsWithoutMutatingDecoy(t *testing.T) {
	for _, swapRoot := range []bool{false, true} {
		name := "handoff"
		if swapRoot {
			name = "root"
		}
		t.Run(name, func(t *testing.T) {
			request := validRequest()
			raw, err := EncodeRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			digest := DigestBytes(raw)
			store := NewStore(filepath.Join(t.TempDir(), DirName))
			if _, err := store.Admit(raw, digest); err != nil {
				t.Fatal(err)
			}
			lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lock.Close() }()

			retained := ""
			if swapRoot {
				retained = store.Root + ".retained"
				if err := os.Rename(store.Root, retained); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(store.Root, request.HandoffID), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				handoff := filepath.Join(store.Root, request.HandoffID)
				retained = handoff + ".retained"
				if err := os.Rename(handoff, retained); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(handoff, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			decoy := filepath.Join(store.Root, request.HandoffID)
			if err := os.WriteFile(filepath.Join(decoy, requestFileName), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReadmitUnderLock(lock, request.HandoffID, digest, raw); err == nil || !strings.Contains(err.Error(), "exact admitted") {
				t.Fatalf("ReadmitUnderLock path-swap error = %v", err)
			}
			entries, err := os.ReadDir(decoy)
			if err != nil || len(entries) != 1 || entries[0].Name() != requestFileName {
				t.Fatalf("decoy mutated by ReadmitUnderLock: entries=%v err=%v", entries, err)
			}
			retainedHandoff := retained
			if swapRoot {
				retainedHandoff = filepath.Join(retained, request.HandoffID)
			}
			for _, name := range []string{receiptFileName, eventsDirName} {
				if _, err := os.Stat(filepath.Join(retainedHandoff, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("retained aggregate gained %s during refused re-admission: %v", name, err)
				}
			}
		})
	}
}

func TestSaveReceiptUnderLockRefusesHandoffPathSwap(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	root := filepath.Join(t.TempDir(), DirName)
	store := NewStore(root)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	handoffPath := filepath.Join(root, request.HandoffID)
	retainedPath := handoffPath + ".retained"
	if err := os.Rename(handoffPath, retainedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(handoffPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handoffPath, requestFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, validReceipt(request, digest)); err == nil || !strings.Contains(err.Error(), "exact admitted") {
		t.Fatalf("SaveReceiptUnderLock path-swap error = %v", err)
	}
	for _, directory := range []string{handoffPath, retainedPath} {
		if _, err := os.Stat(filepath.Join(directory, receiptFileName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("path swap published receipt under %s: %v", directory, err)
		}
	}
}

func TestAppendAndLoadUnderLockUseExactAggregate(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	store := NewStore(filepath.Join(t.TempDir(), DirName))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	want, err := store.AppendEventUnderLock(lock, request.HandoffID, digest, HandoffEvent{
		Phase: PhaseReceived,
		At:    time.Date(2026, 8, 25, 10, 2, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadUnderLock(lock, request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 1 || state.Events[0] != want {
		t.Fatalf("LoadUnderLock events = %#v, want %#v", state.Events, want)
	}
}

func TestCompletedPhaseRequiresExactDurableReceiptOnAppendAndLoad(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	store := NewStore(filepath.Join(t.TempDir(), DirName))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	completed := HandoffEvent{Phase: PhaseCompleted, At: time.Date(2026, 8, 25, 10, 2, 0, 0, time.UTC)}
	if _, err := store.AppendEvent(request.HandoffID, digest, completed); err == nil || !strings.Contains(err.Error(), "durable receipt") {
		t.Fatalf("AppendEvent completed-without-receipt error = %v", err)
	}
	if _, err := store.AppendEventUnderLock(lock, request.HandoffID, digest, completed); err == nil || !strings.Contains(err.Error(), "durable receipt") {
		t.Fatalf("AppendEventUnderLock completed-without-receipt error = %v", err)
	}
	eventsPath := filepath.Join(store.Root, request.HandoffID, eventsDirName)
	if _, err := os.Stat(eventsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused completed append created events directory: %v", err)
	}

	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatal(err)
	}
	forged := completed
	forged.SchemaVersion = EventSchemaVersion
	forged.Sequence = 1
	forged.HandoffID = request.HandoffID
	forged.RequestDigest = digest
	forgedRaw, err := marshalJSON(forged)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventsPath, eventFileName(1)), forgedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(request.HandoffID); !errors.Is(err, ErrHandoffConflict) || !strings.Contains(err.Error(), "without an exact durable receipt") {
		t.Fatalf("Load completed-without-receipt error = %v", err)
	}
	if _, err := store.LoadUnderLock(lock, request.HandoffID, digest); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("LoadUnderLock completed-without-receipt error = %v", err)
	}
	if err := os.RemoveAll(eventsPath); err != nil {
		t.Fatal(err)
	}

	receipt := validReceipt(request, digest)
	if _, _, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, receipt); err != nil {
		t.Fatal(err)
	}
	event, err := store.AppendEventUnderLock(lock, request.HandoffID, digest, completed)
	if err != nil {
		t.Fatalf("AppendEventUnderLock rejected receipt-backed completion: %v", err)
	}
	state, err := store.LoadUnderLock(lock, request.HandoffID, digest)
	if err != nil || state.Receipt == nil || *state.Receipt != receipt || len(state.Events) != 1 || state.Events[0] != event {
		t.Fatalf("receipt-backed completed state = %#v, err=%v", state, err)
	}
}

func TestEventStorageRejectsSymlinkHardlinkAndUnsafeMode(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	eventRaw, err := marshalJSON(HandoffEvent{
		SchemaVersion: EventSchemaVersion,
		Sequence:      1,
		HandoffID:     request.HandoffID,
		RequestDigest: digest,
		Phase:         PhaseReceived,
		At:            time.Date(2026, 8, 25, 10, 2, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		install func(*testing.T, string)
	}{
		{
			name: "symlink",
			install: func(t *testing.T, eventPath string) {
				external := filepath.Join(t.TempDir(), "external-event.json")
				if err := os.WriteFile(external, eventRaw, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, eventPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hardlink",
			install: func(t *testing.T, eventPath string) {
				external := filepath.Join(t.TempDir(), "external-event.json")
				if err := os.WriteFile(external, eventRaw, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(external, eventPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unsafe-mode",
			install: func(t *testing.T, eventPath string) {
				if err := os.WriteFile(eventPath, eventRaw, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), DirName))
			if _, err := store.Admit(raw, digest); err != nil {
				t.Fatal(err)
			}
			eventsPath := filepath.Join(store.Root, request.HandoffID, eventsDirName)
			if err := os.Mkdir(eventsPath, 0o700); err != nil {
				t.Fatal(err)
			}
			test.install(t, filepath.Join(eventsPath, eventFileName(1)))
			if _, err := store.Load(request.HandoffID); err == nil || !strings.Contains(err.Error(), "event") {
				t.Fatalf("Load error = %v, want unsafe event refusal", err)
			}
		})
	}
}

func TestAppendAndLoadUnderLockRefuseHandoffPathSwap(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	root := filepath.Join(t.TempDir(), DirName)
	store := NewStore(root)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	handoffPath := filepath.Join(root, request.HandoffID)
	retainedPath := handoffPath + ".retained"
	if err := os.Rename(handoffPath, retainedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(handoffPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handoffPath, requestFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	event := HandoffEvent{Phase: PhaseCompleted, At: time.Date(2026, 8, 25, 10, 2, 0, 0, time.UTC)}
	if _, err := store.AppendEventUnderLock(lock, request.HandoffID, digest, event); err == nil || !strings.Contains(err.Error(), "exact admitted") {
		t.Fatalf("AppendEventUnderLock path-swap error = %v", err)
	}
	if _, err := store.LoadUnderLock(lock, request.HandoffID, digest); err == nil {
		t.Fatal("LoadUnderLock accepted swapped handoff path")
	}
	for _, directory := range []string{handoffPath, retainedPath} {
		if _, err := os.Stat(filepath.Join(directory, eventsDirName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("path swap published event under %s: %v", directory, err)
		}
	}
}

func TestInterruptedHardLinkPublicationsRepairExactPendingSibling(t *testing.T) {
	const pendingName = ".pending-00000000000000000000000000000000"
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)

	t.Run("request", func(t *testing.T) {
		store := NewStore(filepath.Join(t.TempDir(), DirName))
		if _, err := store.Admit(raw, digest); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(store.Root, request.HandoffID)
		if err := os.Link(filepath.Join(directory, requestFileName), filepath.Join(directory, pendingName)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Admit(raw, digest); err != nil {
			t.Fatalf("Admit did not repair interrupted request publication: %v", err)
		}
		if _, err := os.Stat(filepath.Join(directory, pendingName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("request pending link remains: %v", err)
		}
	})

	t.Run("receipt", func(t *testing.T) {
		store := NewStore(filepath.Join(t.TempDir(), DirName))
		if _, err := store.Admit(raw, digest); err != nil {
			t.Fatal(err)
		}
		receipt := validReceipt(request, digest)
		if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(store.Root, request.HandoffID)
		if err := os.Link(filepath.Join(directory, receiptFileName), filepath.Join(directory, pendingName)); err != nil {
			t.Fatal(err)
		}
		if _, replay, err := store.SaveReceipt(request.HandoffID, digest, receipt); err != nil || !replay {
			t.Fatalf("SaveReceipt replay did not repair interrupted publication: replay=%t err=%v", replay, err)
		}
		if _, err := os.Stat(filepath.Join(directory, pendingName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("receipt pending link remains: %v", err)
		}
	})

	t.Run("event", func(t *testing.T) {
		store := NewStore(filepath.Join(t.TempDir(), DirName))
		if _, err := store.Admit(raw, digest); err != nil {
			t.Fatal(err)
		}
		event, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{
			Phase: PhaseReceived, At: time.Date(2026, 8, 25, 10, 2, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(store.Root, request.HandoffID, eventsDirName)
		if err := os.Link(filepath.Join(directory, eventFileName(event.Sequence)), filepath.Join(directory, pendingName)); err != nil {
			t.Fatal(err)
		}
		state, err := store.Load(request.HandoffID)
		if err != nil || len(state.Events) != 1 || state.Events[0] != event {
			t.Fatalf("Load did not repair interrupted event publication: state=%#v err=%v", state, err)
		}
		if _, err := os.Stat(filepath.Join(directory, pendingName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("event pending link remains: %v", err)
		}
	})
}

func TestAppendEventEnforcesEncodedSizeBoundaryBeforePublication(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	at := time.Date(2026, 8, 25, 10, 2, 0, 0, time.UTC)
	projection := HandoffEvent{
		SchemaVersion: EventSchemaVersion, Sequence: 1, HandoffID: request.HandoffID,
		RequestDigest: digest, Phase: PhaseFailed, At: at, Diagnostic: "x",
	}
	projectionRaw, err := marshalJSON(projection)
	if err != nil {
		t.Fatal(err)
	}
	overhead := len(projectionRaw) - 1
	fit := strings.Repeat("x", maxEventBytes-overhead)
	projection.Diagnostic = fit
	projectionRaw, err = marshalJSON(projection)
	if err != nil || len(projectionRaw) != maxEventBytes {
		t.Fatalf("boundary fixture size = %d, err=%v", len(projectionRaw), err)
	}

	store := NewStore(filepath.Join(t.TempDir(), DirName))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseFailed, At: at, Diagnostic: fit}); err != nil {
		t.Fatalf("AppendEvent rejected exact size boundary: %v", err)
	}
	if _, err := store.Load(request.HandoffID); err != nil {
		t.Fatalf("Load rejected exact size boundary: %v", err)
	}

	oversizedStore := NewStore(filepath.Join(t.TempDir(), DirName))
	if _, err := oversizedStore.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := oversizedStore.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseFailed, At: at, Diagnostic: fit + "x"}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("AppendEvent oversized error = %v", err)
	}
	files, err := filepath.Glob(filepath.Join(oversizedStore.Root, request.HandoffID, eventsDirName, "*.json"))
	if err != nil || len(files) != 0 {
		t.Fatalf("oversized event published files %v, err=%v", files, err)
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

	receiptRequest := validRequest()
	receiptRequestRaw, err := EncodeRequest(receiptRequest)
	if err != nil {
		t.Fatal(err)
	}
	receipt := validReceipt(receiptRequest, DigestBytes(receiptRequestRaw))
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

// validRequestWithInlineHandover returns a request shaped the way every
// checkpoint created after the ContinuationPrivate cutover is: no legacy
// HandoverPath, and the rendered document carried inline instead.
func validRequestWithInlineHandover(content string) Request {
	request := validRequest()
	request.HandoverPath = ""
	request.HandoverContent = content
	request.HandoverDigest = DigestBytes([]byte(content))
	return request
}

func TestEnsureHandoverUnderLockMaterializesPrivateFileReadableByReadHandover(t *testing.T) {
	request := validRequestWithInlineHandover("# handover\n\ncontinue here\n")
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	root := filepath.Join(t.TempDir(), DirName)
	store := NewStore(root)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()

	path, err := store.EnsureHandoverUnderLock(lock, request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := PrivateHandoverPath(root, request.HandoffID)
	if path != wantPath {
		t.Fatalf("materialized path = %q, want %q", path, wantPath)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("materialized path %q is not absolute", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("private handover mode = %v", info.Mode())
	}

	got, err := store.ReadHandover(request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != request.HandoverContent {
		t.Fatalf("read handover = %q, want %q", got, request.HandoverContent)
	}

	// Repeat materialization is a safe no-op: the content is derived from the
	// same immutable admitted request, so it is always byte-identical.
	again, err := store.EnsureHandoverUnderLock(lock, request.HandoffID, digest)
	if err != nil || again != path {
		t.Fatalf("repeat EnsureHandoverUnderLock = (%q, %v), want (%q, nil)", again, err, path)
	}
}

func TestEnsureHandoverUnderLockRejectsRequestWithNoInlineContent(t *testing.T) {
	request := validRequest() // legacy shape: HandoverPath set, HandoverContent empty
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	store := NewStore(filepath.Join(t.TempDir(), DirName))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()

	if _, err := store.EnsureHandoverUnderLock(lock, request.HandoffID, digest); err == nil {
		t.Fatal("EnsureHandoverUnderLock accepted a request with no inline handover content")
	}
}

func TestReadHandoverRejectsAModifiedPrivateFile(t *testing.T) {
	request := validRequestWithInlineHandover("original content\n")
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	root := filepath.Join(t.TempDir(), DirName)
	store := NewStore(root)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	path, err := store.EnsureHandoverUnderLock(lock, request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}

	// The immutable single-link publication is not writable through the
	// normal store API; simulate tampering directly to prove ReadHandover's
	// hardened reader (mode/link-count checks) still catches it, exactly as
	// it does for every other durable handoff artifact.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadHandover(request.HandoffID); err == nil {
		t.Fatal("ReadHandover accepted a private handover with a changed mode")
	}
}
