package sessionpark

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func spCovTargetMembers(t *testing.T, request RemoteRequest, digest sessionmove.Digest) []ReceiptMember {
	t.Helper()
	members := make([]ReceiptMember, len(request.Members))
	for index, member := range request.Members {
		reference, err := TargetWorkLogReference(request, digest, member)
		if err != nil {
			t.Fatal(err)
		}
		members[index] = ReceiptMember{MemberID: member.MemberID, Repository: member.Repository,
			TargetPath: "/tmp/target-" + member.MemberID, Pin: MemberPin(request.ResumeID, member.MemberID),
			Commit: member.Commit, TargetWorkLogReference: reference}
	}
	return members
}

func TestSpCovTargetAdmitRejectsUnsafeAndConflictingArtifacts(t *testing.T) {
	t.Parallel()
	raw := targetEnvelopeForTest(t)
	envelope, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("invalid envelope", func(t *testing.T) {
		t.Parallel()
		if _, err := NewTargetStore(filepath.Join(t.TempDir(), "store")).Admit([]byte("{}\n")); err == nil {
			t.Fatal("invalid envelope admitted")
		}
	})
	t.Run("noncanonical envelope", func(t *testing.T) {
		t.Parallel()
		compact, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewTargetStore(filepath.Join(t.TempDir(), "store")).Admit(compact); err == nil {
			t.Fatal("noncanonical envelope admitted")
		}
	})
	t.Run("relative root", func(t *testing.T) {
		t.Parallel()
		if _, err := NewTargetStore("relative-store").Admit(raw); err == nil {
			t.Fatal("relative root admitted")
		}
	})
	t.Run("root beneath regular file", func(t *testing.T) {
		t.Parallel()
		blocker := filepath.Join(t.TempDir(), "blocker")
		spCovWriteRaw(t, blocker, []byte("x"), 0o600)
		if _, err := NewTargetStore(filepath.Join(blocker, "store")).Admit(raw); err == nil {
			t.Fatal("root beneath a regular file admitted")
		}
	})
	t.Run("root symlink", func(t *testing.T) {
		t.Parallel()
		parent := t.TempDir()
		real := filepath.Join(parent, "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := NewTargetStore(link).Admit(raw); err == nil {
			t.Fatal("symlinked root admitted")
		}
	})
	t.Run("admit lock is a directory", func(t *testing.T) {
		t.Parallel()
		root := filepath.Join(t.TempDir(), "store")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, ".admit-"+envelope.Request.ResumeID+".lock"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := NewTargetStore(root).Admit(raw); err == nil {
			t.Fatal("directory admit lock accepted")
		}
	})
}

func TestSpCovTargetAdmitReplayAndDurableConflicts(t *testing.T) {
	t.Parallel()
	t.Run("first admission and idle replay", func(t *testing.T) {
		t.Parallel()
		store, admission := spCovTargetFixture(t)
		if admission.Replay || admission.Receipt != nil {
			t.Fatalf("first admission = %#v", admission)
		}
		again, err := store.Admit(targetEnvelopeForTest(t))
		if err != nil || !again.Replay || again.Receipt != nil {
			t.Fatalf("replay = %#v err=%v", again, err)
		}
	})
	t.Run("continuation conflict on replay", func(t *testing.T) {
		t.Parallel()
		store, admission := spCovTargetFixture(t)
		path := filepath.Join(store.Root, admission.Envelope.Request.ResumeID, ContinuationFileName)
		spCovWriteRaw(t, path, []byte("a different continuation"), 0o600)
		if _, err := store.Admit(targetEnvelopeForTest(t)); err == nil {
			t.Fatal("conflicting continuation admitted")
		}
	})
	t.Run("receipt directory", func(t *testing.T) {
		t.Parallel()
		store, admission := spCovTargetFixture(t)
		path := filepath.Join(store.Root, admission.Envelope.Request.ResumeID, targetReceiptFileName)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Admit(targetEnvelopeForTest(t)); err == nil {
			t.Fatal("directory receipt accepted")
		}
	})
	t.Run("conflicting durable receipt", func(t *testing.T) {
		t.Parallel()
		store, admission := spCovTargetFixture(t)
		path := filepath.Join(store.Root, admission.Envelope.Request.ResumeID, targetReceiptFileName)
		conflicting := spCovReceiptFixture("resume-other", admission.Digest)
		encoded, err := EncodeReceipt(conflicting)
		if err != nil {
			t.Fatal(err)
		}
		spCovWriteRaw(t, path, encoded, 0o600)
		if _, err := store.Admit(targetEnvelopeForTest(t)); err == nil {
			t.Fatal("conflicting durable receipt admitted")
		}
	})
	t.Run("events path is a regular file", func(t *testing.T) {
		t.Parallel()
		store, admission := spCovTargetFixture(t)
		path := filepath.Join(store.Root, admission.Envelope.Request.ResumeID, targetEventsDirName)
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		spCovWriteRaw(t, path, []byte("x"), 0o600)
		if _, err := store.Admit(targetEnvelopeForTest(t)); err == nil {
			t.Fatal("regular file accepted as an events directory")
		}
	})
	t.Run("existing receipt is validated on replay", func(t *testing.T) {
		t.Parallel()
		store, admission := spCovTargetFixture(t)
		lock := spCovTargetLock(t, store, admission)
		receipt := spCovTargetReceipt(t, admission)
		if _, _, err := store.SaveReceiptUnderLock(lock, admission.Envelope.Request, admission.Digest, receipt); err != nil {
			t.Fatal(err)
		}
		replayed, err := store.Admit(targetEnvelopeForTest(t))
		if err != nil || !replayed.Replay || replayed.Receipt == nil || replayed.Receipt.ResumeID != receipt.ResumeID {
			t.Fatalf("replay = %#v err=%v", replayed, err)
		}
	})
}

func TestSpCovTargetAcquireRejectsUnsafeAggregates(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	id := admission.Envelope.Request.ResumeID
	digest := admission.Digest
	ctx := context.Background()

	if _, err := store.Acquire(ctx, "resume-absent", digest); err == nil {
		t.Fatal("missing aggregate acquired")
	}
	if _, err := store.Acquire(ctx, "bad-identity", digest); err == nil {
		t.Fatal("invalid resume ID acquired")
	}
	if _, err := store.Acquire(ctx, id, "md5:abc"); err == nil {
		t.Fatal("invalid digest acquired")
	}
	if _, err := NewTargetStore("relative-store").Acquire(ctx, id, digest); err == nil {
		t.Fatal("relative root acquired")
	}
	if _, err := NewTargetStore(filepath.Join(t.TempDir(), "missing")).Acquire(ctx, id, digest); err == nil {
		t.Fatal("missing root acquired")
	}
	if _, err := store.Acquire(ctx, id, spCovDigest("sha256:"+strings.Repeat("0", 64))); err == nil {
		t.Fatal("mismatched digest acquired")
	}

	envelopePath := filepath.Join(store.Root, id, EnvelopeFileName)
	// These three subtests share and mutate envelopePath in place, so they
	// are left serial rather than racing each other.
	t.Run("envelope missing", func(t *testing.T) {
		moved := envelopePath + "-backup"
		if err := os.Rename(envelopePath, moved); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Rename(moved, envelopePath) }()
		if _, err := store.Acquire(ctx, id, digest); err == nil {
			t.Fatal("missing envelope acquired")
		}
	})
	t.Run("envelope bytes changed", func(t *testing.T) {
		original, err := os.ReadFile(envelopePath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { spCovWriteRaw(t, envelopePath, original, 0o600) }()
		spCovWriteRaw(t, envelopePath, append(original, ' '), 0o600)
		if _, err := store.Acquire(ctx, id, digest); err == nil {
			t.Fatal("tampered envelope acquired")
		}
	})
	t.Run("envelope identity conflicts with aggregate", func(t *testing.T) {
		otherBundle := remoteTestBundle(t)
		otherBundle.ParkedSessionID = "park-other-source"
		otherBundle.Source.WBSessionID = "wbs-other-source"
		other := BuildRemoteRequest(otherBundle, "target", "", time.Unix(100, 0).UTC())
		otherBytes, err := EncodeEnvelope(Envelope{SchemaVersion: EnvelopeSchemaVersion, Kind: EnvelopeKind, Request: other})
		if err != nil {
			t.Fatal(err)
		}
		original, err := os.ReadFile(envelopePath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { spCovWriteRaw(t, envelopePath, original, 0o600) }()
		spCovWriteRaw(t, envelopePath, otherBytes, 0o600)
		if _, err := store.Acquire(ctx, id, sessionmove.DigestBytes(otherBytes)); err == nil {
			t.Fatal("foreign envelope acquired")
		}
	})
	t.Run("receive lock is a directory", func(t *testing.T) {
		t.Parallel()
		lockPath := filepath.Join(store.Root, id, targetLockFileName)
		_ = os.Remove(lockPath)
		if err := os.Mkdir(lockPath, 0o700); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Remove(lockPath) }()
		if _, err := store.Acquire(ctx, id, digest); err == nil {
			t.Fatal("directory receive lock accepted")
		}
	})
}

func TestSpCovTargetLockAuthenticatesOnlyExactAggregateBytes(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	id := admission.Envelope.Request.ResumeID
	digest := string(admission.Digest)
	lock := spCovTargetLock(t, store, admission)

	if !lock.HeldForSession(store.Root, id, digest) {
		t.Fatal("held target lock did not authenticate its exact aggregate")
	}
	if lock.HeldForSession(store.Root, id, "sha256:"+strings.Repeat("0", 64)) {
		t.Fatal("held target lock authenticated a foreign digest")
	}
	if lock.HeldForSession(store.Root, "resume-other", digest) {
		t.Fatal("held target lock authenticated a foreign aggregate")
	}
	if lock.HeldForSession(filepath.Join(store.Root, "elsewhere"), id, digest) {
		t.Fatal("held target lock authenticated a foreign root")
	}
	if lock.Envelope().Request.ResumeID != id || lock.Envelope().Kind != EnvelopeKind {
		t.Fatalf("envelope = %#v", lock.Envelope())
	}
	var nilLock *TargetLock
	if nilLock.HeldForSession(store.Root, id, digest) {
		t.Fatal("nil target lock authenticated")
	}

	for name, mutate := range map[string]func(t *testing.T, store TargetStore, id string){
		"root replaced": func(t *testing.T, store TargetStore, id string) {
			backup := store.Root + "-backup"
			if err := os.Rename(store.Root, backup); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(store.Root, 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"root removed": func(t *testing.T, store TargetStore, id string) {
			if err := os.RemoveAll(store.Root); err != nil {
				t.Fatal(err)
			}
		},
		"aggregate removed": func(t *testing.T, store TargetStore, id string) {
			if err := os.RemoveAll(filepath.Join(store.Root, id)); err != nil {
				t.Fatal(err)
			}
		},
		"aggregate replaced": func(t *testing.T, store TargetStore, id string) {
			agg := filepath.Join(store.Root, id)
			if err := os.Rename(agg, agg+"-backup"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(agg, 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"envelope removed": func(t *testing.T, store TargetStore, id string) {
			if err := os.Remove(filepath.Join(store.Root, id, EnvelopeFileName)); err != nil {
				t.Fatal(err)
			}
		},
		"envelope replaced": func(t *testing.T, store TargetStore, id string) {
			path := filepath.Join(store.Root, id, EnvelopeFileName)
			if err := os.Rename(path, path+"-backup"); err != nil {
				t.Fatal(err)
			}
			spCovWriteRaw(t, path, []byte("{}\n"), 0o600)
		},
		"envelope bytes changed": func(t *testing.T, store TargetStore, id string) {
			path := filepath.Join(store.Root, id, EnvelopeFileName)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			spCovWriteRaw(t, path, append(raw, ' '), 0o600)
		},
		"lock removed": func(t *testing.T, store TargetStore, id string) {
			if err := os.Remove(filepath.Join(store.Root, id, targetLockFileName)); err != nil {
				t.Fatal(err)
			}
		},
		"lock replaced": func(t *testing.T, store TargetStore, id string) {
			path := filepath.Join(store.Root, id, targetLockFileName)
			if err := os.Rename(path, path+"-backup"); err != nil {
				t.Fatal(err)
			}
			spCovWriteRaw(t, path, nil, 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, admission := spCovTargetFixture(t)
			id := admission.Envelope.Request.ResumeID
			digest := string(admission.Digest)
			lock := spCovTargetLock(t, store, admission)
			mutate(t, store, id)
			if lock.HeldForSession(store.Root, id, digest) {
				t.Fatal("tampered target evidence authenticated")
			}
		})
	}
}

func TestSpCovTargetRetainSessionDir(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	id := admission.Envelope.Request.ResumeID
	digest := string(admission.Digest)
	lock := spCovTargetLock(t, store, admission)
	if _, err := lock.RetainSessionDir(store.Root, id, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("retain with a foreign digest succeeded")
	}
	if _, err := lock.RetainSessionDir(store.Root, "resume-other", digest); err == nil {
		t.Fatal("retain with a foreign aggregate succeeded")
	}
	retained, err := lock.RetainSessionDir(store.Root, id, digest)
	if err != nil || retained == nil {
		t.Fatalf("retain = %v err=%v", retained, err)
	}
	if err := retained.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSpCovTargetLoadReceiptUnderLock(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	request := admission.Envelope.Request
	lock := spCovTargetLock(t, store, admission)
	if _, err := store.LoadReceiptUnderLock(nil, request, admission.Digest); err == nil {
		t.Fatal("nil lock loaded a receipt")
	}
	if receipt, err := store.LoadReceiptUnderLock(lock, request, admission.Digest); err != nil || receipt != nil {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	receiptPath := filepath.Join(store.Root, request.ResumeID, targetReceiptFileName)
	if err := os.Mkdir(receiptPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadReceiptUnderLock(lock, request, admission.Digest); err == nil {
		t.Fatal("directory receipt loaded")
	}
	if err := os.Remove(receiptPath); err != nil {
		t.Fatal(err)
	}
	spCovWriteRaw(t, receiptPath, []byte("{}\n"), 0o600)
	if _, err := store.LoadReceiptUnderLock(lock, request, admission.Digest); err == nil {
		t.Fatal("malformed receipt loaded")
	}
	conflicting := spCovReceiptFixture("resume-other", admission.Digest)
	encoded, err := EncodeReceipt(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	spCovWriteRaw(t, receiptPath, encoded, 0o600)
	if _, err := store.LoadReceiptUnderLock(lock, request, admission.Digest); err == nil {
		t.Fatal("conflicting receipt loaded")
	}
	good := spCovTargetReceipt(t, admission)
	encoded, err = EncodeReceipt(good)
	if err != nil {
		t.Fatal(err)
	}
	spCovWriteRaw(t, receiptPath, encoded, 0o600)
	loaded, err := store.LoadReceiptUnderLock(lock, request, admission.Digest)
	if err != nil || loaded == nil || loaded.ResumeID != good.ResumeID {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestSpCovTargetEnsureSuccessorContextUnderLock(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	request := admission.Envelope.Request
	lock := spCovTargetLock(t, store, admission)
	members := spCovTargetMembers(t, request, admission.Digest)

	if _, _, err := store.EnsureSuccessorContextUnderLock(nil, request, admission.Digest, members); err == nil {
		t.Fatal("nil lock published successor context")
	}
	if _, _, err := store.EnsureSuccessorContextUnderLock(lock, request, admission.Digest, nil); err == nil {
		t.Fatal("missing members published successor context")
	}
	if _, _, err := store.EnsureSuccessorContextUnderLock(lock, request, admission.Digest, append(members, members[0])); err == nil {
		t.Fatal("extra members published successor context")
	}
	for name, mutate := range map[string]func([]ReceiptMember){
		"member key":    func(value []ReceiptMember) { value[0].MemberID = "m-999-00000000" },
		"member repo":   func(value []ReceiptMember) { value[0].Repository = "acme/other" },
		"member commit": func(value []ReceiptMember) { value[0].Commit = strings.Repeat("d", 40) },
		"member pin":    func(value []ReceiptMember) { value[0].Pin += "-other" },
		"member path":   func(value []ReceiptMember) { value[0].TargetPath = "relative/path" },
		"member dirty path": func(value []ReceiptMember) {
			value[0].TargetPath = "/tmp/../escape"
		},
		"member reference": func(value []ReceiptMember) { value[0].TargetWorkLogReference = "bogus" },
	} {
		// Left serial: the trailing checks below publish and then
		// overwrite the same successor-context path on this store/lock,
		// and must run only after every rejection case has executed.
		t.Run(name, func(t *testing.T) {
			candidate := append([]ReceiptMember(nil), members...)
			mutate(candidate)
			if _, _, err := store.EnsureSuccessorContextUnderLock(lock, request, admission.Digest, candidate); err == nil {
				t.Fatal("conflicting member published successor context")
			}
		})
	}
	if _, _, err := store.EnsureSuccessorContextUnderLock(lock, request, admission.Digest, spCovTargetMembers(t, request, spCovDigest("sha256:"+strings.Repeat("0", 64)))); err == nil {
		t.Fatal("foreign digest published successor context")
	}

	path, raw, err := store.EnsureSuccessorContextUnderLock(lock, request, admission.Digest, members)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(store.Root, request.ResumeID, SuccessorContextFileName)
	if path != wantPath {
		t.Fatalf("context path = %q want %q", path, wantPath)
	}
	body := string(raw)
	if !strings.HasPrefix(body, request.Continuation) || !strings.Contains(body, "\nTarget worktrees:\n") ||
		!strings.Contains(body, request.Members[0].MemberID) || !strings.Contains(body, "work_log: "+members[0].TargetWorkLogReference) {
		t.Fatalf("successor context body = %q", body)
	}
	fileRaw, err := os.ReadFile(path)
	if err != nil || string(fileRaw) != body {
		t.Fatalf("published context differs: err=%v", err)
	}

	oversized := append([]ReceiptMember(nil), members...)
	oversized[0].TargetPath = "/" + strings.Repeat("t", MaxSuccessorContextBytes+1024)
	if _, _, err := store.EnsureSuccessorContextUnderLock(lock, request, admission.Digest, oversized); err == nil {
		t.Fatal("oversized successor context published")
	}
	spCovWriteRaw(t, path, []byte("conflicting target context"), 0o600)
	if _, _, err := store.EnsureSuccessorContextUnderLock(lock, request, admission.Digest, members); err == nil {
		t.Fatal("conflicting target successor context republished")
	}
}

func TestSpCovTargetSaveReceiptUnderLock(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	request := admission.Envelope.Request
	lock := spCovTargetLock(t, store, admission)
	receipt := spCovTargetReceipt(t, admission)

	if _, _, err := store.SaveReceiptUnderLock(nil, request, admission.Digest, receipt); err == nil {
		t.Fatal("nil lock saved a receipt")
	}
	if _, _, err := store.SaveReceiptUnderLock(lock, request, admission.Digest, spCovReceiptFixture("resume-other", admission.Digest)); err == nil {
		t.Fatal("conflicting receipt saved")
	}
	saved, replay, err := store.SaveReceiptUnderLock(lock, request, admission.Digest, receipt)
	if err != nil || replay || saved.ResumeID != receipt.ResumeID || saved.AttemptID != receipt.AttemptID {
		t.Fatalf("saved=%#v replay=%t err=%v", saved, replay, err)
	}
	again, replay, err := store.SaveReceiptUnderLock(lock, request, admission.Digest, receipt)
	if err != nil || !replay || again.AttemptID != receipt.AttemptID {
		t.Fatalf("again=%#v replay=%t err=%v", again, replay, err)
	}

	other := receipt
	other.AttemptID = "000002-22222222222222222222222222222222"
	other.AttemptIndex = 2
	if _, _, err := store.SaveReceiptUnderLock(lock, request, admission.Digest, other); err == nil || !strings.Contains(err.Error(), "conflicting durable receipt") {
		t.Fatalf("second receipt error = %v", err)
	}
}

func TestSpCovTargetAppendAndReadEvents(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	request := admission.Envelope.Request
	lock := spCovTargetLock(t, store, admission)
	if _, err := store.AppendEventUnderLock(nil, request, admission.Digest, "received", time.Now()); err == nil {
		t.Fatal("nil lock appended an event")
	}
	first, err := store.AppendEventUnderLock(lock, request, admission.Digest, "received", time.Time{})
	if err != nil || first.Sequence != 1 || first.Phase != "received" || first.At.IsZero() || first.ResumeID != request.ResumeID {
		t.Fatalf("first event = %#v err=%v", first, err)
	}
	idempotent, err := store.AppendEventUnderLock(lock, request, admission.Digest, "received", time.Unix(999, 0))
	if err != nil || idempotent != first {
		t.Fatalf("idempotent event = %#v err=%v", idempotent, err)
	}
	second, err := store.AppendEventUnderLock(lock, request, admission.Digest, "launched", time.Unix(200, 0))
	if err != nil || second.Sequence != 2 || second.Phase != "launched" {
		t.Fatalf("second event = %#v err=%v", second, err)
	}
	events, err := store.EventsUnderLock(lock, request, admission.Digest)
	if err != nil || len(events) != 2 || events[0].Phase != "received" || events[1].Phase != "launched" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if _, err := store.EventsUnderLock(nil, request, admission.Digest); err == nil {
		t.Fatal("nil lock read events")
	}
	eventsDir := filepath.Join(store.Root, request.ResumeID, targetEventsDirName)
	spCovWriteRaw(t, filepath.Join(eventsDir, "rogue.json"), []byte("{}\n"), 0o600)
	if _, err := store.AppendEventUnderLock(lock, request, admission.Digest, "third", time.Now()); err == nil {
		t.Fatal("event appended beside a rogue artifact")
	}
	if err := os.Remove(filepath.Join(eventsDir, "rogue.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(eventsDir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EventsUnderLock(lock, request, admission.Digest); err == nil {
		t.Fatal("events read without an events directory")
	}
	if _, err := store.AppendEventUnderLock(lock, request, admission.Digest, "third", time.Now()); err == nil {
		t.Fatal("event appended without an events directory")
	}
}

func TestSpCovListTargetEventsRejectsEveryMalformedArtifact(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	request := admission.Envelope.Request
	eventsDir := filepath.Join(store.Root, request.ResumeID, targetEventsDirName)
	openEvents := func(t *testing.T) *os.File {
		t.Helper()
		dir, err := os.Open(eventsDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = dir.Close() })
		return dir
	}

	envelopeFile, err := os.Open(filepath.Join(store.Root, request.ResumeID, EnvelopeFileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listTargetEventsAt(envelopeFile, request.ResumeID); err == nil {
		t.Fatal("regular file accepted as an events directory")
	}
	_ = envelopeFile.Close()

	spCovWriteRaw(t, filepath.Join(eventsDir, "rogue.json"), []byte("{}\n"), 0o600)
	if _, err := listTargetEventsAt(openEvents(t), request.ResumeID); err == nil {
		t.Fatal("rogue target artifact accepted")
	}
	if err := os.Remove(filepath.Join(eventsDir, "rogue.json")); err != nil {
		t.Fatal(err)
	}
	eventPath := filepath.Join(eventsDir, "00000000000000000001.json")
	if err := os.Mkdir(eventPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := listTargetEventsAt(openEvents(t), request.ResumeID); err == nil {
		t.Fatal("directory named like a target event accepted")
	}
	if err := os.Remove(eventPath); err != nil {
		t.Fatal(err)
	}
	spCovWriteRaw(t, eventPath, []byte("{}\n"), 0o600)
	if _, err := listTargetEventsAt(openEvents(t), request.ResumeID); err == nil {
		t.Fatal("malformed target event accepted")
	}
	if err := os.Remove(eventPath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-event")
	spCovWriteRaw(t, outside, []byte("{}\n"), 0o600)
	if err := os.Symlink(outside, eventPath); err != nil {
		t.Fatal(err)
	}
	if _, err := listTargetEventsAt(openEvents(t), request.ResumeID); err == nil {
		t.Fatal("symlinked target event accepted")
	}
	if err := os.Remove(eventPath); err != nil {
		t.Fatal(err)
	}
	spCovWriteRaw(t, eventPath, []byte("{}\n"), 0o600)
	foreign := TargetEvent{SchemaVersion: 1, Sequence: 1, ResumeID: "resume-other", Phase: "received", At: time.Unix(1, 0)}
	raw, err := jsonMarshal(foreign)
	if err != nil {
		t.Fatal(err)
	}
	spCovWriteRaw(t, eventPath, raw, 0o600)
	if _, err := listTargetEventsAt(openEvents(t), request.ResumeID); err == nil {
		t.Fatal("foreign target event accepted")
	}

	for sequence, phase := range map[uint64]string{1: "received", 2: "launched"} {
		event := TargetEvent{SchemaVersion: 1, Sequence: sequence, ResumeID: request.ResumeID, Phase: phase, At: time.Unix(int64(sequence), 0)}
		raw, err := jsonMarshal(event)
		if err != nil {
			t.Fatal(err)
		}
		spCovWriteRaw(t, filepath.Join(eventsDir, spCovEventName(sequence)), raw, 0o600)
	}
	events, err := listTargetEventsAt(openEvents(t), request.ResumeID)
	if err != nil || len(events) != 2 || events[0].Phase != "received" || events[1].Phase != "launched" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestSpCovCleanAbsoluteStoreRootRules(t *testing.T) {
	t.Parallel()
	if _, err := cleanAbsoluteStoreRoot(""); err == nil {
		t.Fatal("empty root accepted")
	}
	if _, err := cleanAbsoluteStoreRoot(" /tmp/x"); err == nil {
		t.Fatal("padded root accepted")
	}
	if _, err := cleanAbsoluteStoreRoot("relative/store"); err == nil {
		t.Fatal("relative root accepted")
	}
	if _, err := cleanAbsoluteStoreRoot("/tmp/./store"); err == nil {
		t.Fatal("unclean root accepted")
	}
	root := filepath.Join(t.TempDir(), "store")
	got, err := cleanAbsoluteStoreRoot(root)
	if err != nil || got != root {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestSpCovOpenTargetLockCreatesAndRejectsDirectories(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	aggPath := filepath.Join(store.Root, admission.Envelope.Request.ResumeID)
	aggregate, err := os.Open(aggPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = aggregate.Close() })

	fd, err := openTargetLock(int(aggregate.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	fd, err = openTargetLock(int(aggregate.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)

	lockPath := filepath.Join(aggPath, targetLockFileName)
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if fd, err := openTargetLock(int(aggregate.Fd())); err == nil {
		_ = unix.Close(fd)
		t.Fatal("directory target lock opened")
	}
}

func TestSpCovReadBoundedRegularRejectsUnusableHandles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	spCovWriteRaw(t, regular, []byte("hello"), 0o600)
	file, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := readBoundedRegular(file, 16)
	if err != nil || string(raw) != "hello" {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRegular(file, 16); err == nil {
		t.Fatal("closed handle accepted")
	}

	directory, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	if _, err := readBoundedRegular(directory, 16); err == nil {
		t.Fatal("directory accepted as a bounded regular file")
	}

	oversized := filepath.Join(dir, "oversized")
	spCovWriteRaw(t, oversized, []byte("0123456789"), 0o600)
	oversizedFile, err := os.Open(oversized)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oversizedFile.Close() })
	if _, err := readBoundedRegular(oversizedFile, 4); err == nil {
		t.Fatal("oversized artifact accepted")
	}
}

func TestSpCovWriteImmutableAndExactPrivateArtifacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	directory, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })

	created, err := writeImmutableAt(directory, "first.json", []byte("body\n"), 0o600)
	if err != nil || !created {
		t.Fatalf("created=%t err=%v", created, err)
	}
	created, err = writeImmutableAt(directory, "first.json", []byte("body\n"), 0o600)
	if err != nil || created {
		t.Fatalf("immutable rewrite created=%t err=%v", created, err)
	}
	if _, err := writeImmutableAt(directory, "nested/missing.json", []byte("x"), 0o600); err == nil {
		t.Fatal("write with a missing parent directory accepted")
	}
	if fd, err := openOrCreateRegularAt(int(directory.Fd()), "plain.lock", 0o600); err != nil {
		t.Fatalf("openOrCreateRegularAt = %v", err)
	} else {
		_ = unix.Close(fd)
	}
	if fd, err := openOrCreateRegularAt(int(directory.Fd()), "plain.lock", 0o600); err != nil {
		t.Fatalf("openOrCreateRegularAt reopen = %v", err)
	} else {
		_ = unix.Close(fd)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if fd, err := openOrCreateRegularAt(int(directory.Fd()), "dir.lock", 0o600); err == nil {
		_ = unix.Close(fd)
		t.Fatal("directory accepted by openOrCreateRegularAt")
	}

	if err := writeExactPrivateAt(directory, "exact.json", []byte("same\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeExactPrivateAt(directory, "exact.json", []byte("same\n")); err != nil {
		t.Fatalf("idempotent writeExactPrivateAt = %v", err)
	}
	if err := writeExactPrivateAt(directory, "exact.json", []byte("different\n")); err == nil {
		t.Fatal("conflicting immutable artifact accepted")
	}
	if err := writeExactPrivateAt(directory, "nested/missing.json", []byte("x")); err == nil {
		t.Fatal("writeExactPrivateAt accepted a missing parent directory")
	}
	spCovWriteRaw(t, filepath.Join(dir, "loose.json"), []byte("x"), 0o644)
	if err := writeExactPrivateAt(directory, "loose.json", []byte("x")); err == nil {
		t.Fatal("writeExactPrivateAt accepted a non-private artifact")
	}
}

func TestSpCovLoadReceiptAtBranches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	directory, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	receipt, err := loadReceiptAt(directory)
	if err != nil || receipt != nil {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	spCovWriteRaw(t, filepath.Join(dir, targetReceiptFileName), []byte("{}\n"), 0o600)
	if _, err := loadReceiptAt(directory); err == nil {
		t.Fatal("malformed durable receipt loaded")
	}
	if err := os.Remove(filepath.Join(dir, targetReceiptFileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, targetReceiptFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReceiptAt(directory); err == nil {
		t.Fatal("directory receipt loaded")
	}
	if err := os.Remove(filepath.Join(dir, targetReceiptFileName)); err != nil {
		t.Fatal(err)
	}
	good := spCovReceiptFixture("resume-x", spCovDigest("sha256:"+strings.Repeat("0", 64)))
	encoded, err := EncodeReceipt(good)
	if err != nil {
		t.Fatal(err)
	}
	spCovWriteRaw(t, filepath.Join(dir, targetReceiptFileName), encoded, 0o600)
	loaded, err := loadReceiptAt(directory)
	if err != nil || loaded == nil || loaded.ResumeID != "resume-x" {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestSpCovTargetAcquireFenceHonoursCancelledContext(t *testing.T) {
	t.Parallel()
	store, admission := spCovTargetFixture(t)
	_ = spCovTargetLock(t, store, admission)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Acquire(ctx, admission.Envelope.Request.ResumeID, admission.Digest); err == nil {
		t.Fatal("contending target acquire ignored the held fence")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("contending target acquire error = %v", err)
	}
}

func TestSpCovOpenOrCreateRegularAtRejectsNonRegularDeviceNode(t *testing.T) {
	t.Parallel()
	// /dev/null is the one portable non-regular node every unix host provides:
	// it opens read-write but must be rejected by the descriptor shape check.
	dev, err := os.Open("/dev")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dev.Close() })
	fd, err := openOrCreateRegularAt(int(dev.Fd()), "null", 0o600)
	if err == nil {
		_ = unix.Close(fd)
		t.Fatal("character device accepted as one regular file")
	}
}
