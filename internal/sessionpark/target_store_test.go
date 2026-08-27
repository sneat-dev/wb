package sessionpark

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTargetStoreAdmitsPrivateExactArtifactsAndStrictEvents(t *testing.T) {
	root := filepath.Join(t.TempDir(), "target-store")
	store := NewTargetStore(root)
	raw := targetEnvelopeForTest(t)
	admission, err := store.Admit(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{EnvelopeFileName, ContinuationFileName} {
		info, err := os.Stat(filepath.Join(root, admission.Envelope.Request.ResumeID, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s info=%v err=%v", name, info, err)
		}
	}
	lock, err := store.Acquire(context.Background(), admission.Envelope.Request.ResumeID, admission.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if _, err := store.AppendEventUnderLock(lock, admission.Envelope.Request, admission.Digest, "received", time.Unix(110, 0)); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(root, admission.Envelope.Request.ResumeID, targetEventsDirName)
	if err := os.WriteFile(filepath.Join(events, "rogue.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EventsUnderLock(lock, admission.Envelope.Request, admission.Digest); err == nil {
		t.Fatal("unexpected event filename accepted")
	}
}

func TestTargetStoreRefusesAggregateAndArtifactSymlinks(t *testing.T) {
	raw := targetEnvelopeForTest(t)
	envelope, _ := DecodeEnvelope(raw)
	t.Run("aggregate", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(root, envelope.Request.ResumeID)); err != nil {
			t.Fatal(err)
		}
		if _, err := NewTargetStore(root).Admit(raw); err == nil {
			t.Fatal("target aggregate symlink followed")
		}
	})
	t.Run("envelope", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, envelope.Request.ResumeID)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, EnvelopeFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := NewTargetStore(root).Admit(raw); err == nil {
			t.Fatal("target envelope symlink followed")
		}
	})
}

func TestTargetLockRetainAndCloseRaceNeverReturnsUnvalidatedCapability(t *testing.T) {
	store := NewTargetStore(filepath.Join(t.TempDir(), "target-store"))
	raw := targetEnvelopeForTest(t)
	admission, err := store.Admit(raw)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.Acquire(context.Background(), admission.Envelope.Request.ResumeID, admission.Digest)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		for range 100 {
			retained, retainErr := lock.RetainSessionDir(store.Root, admission.Envelope.Request.ResumeID, string(admission.Digest))
			if retainErr == nil {
				if retained == nil {
					t.Error("successful retain returned nil capability")
				} else {
					_ = retained.Close()
				}
			}
		}
	}()
	go func() {
		defer group.Done()
		_ = lock.Close()
	}()
	group.Wait()
	if lock.HeldForSession(store.Root, admission.Envelope.Request.ResumeID, string(admission.Digest)) {
		t.Fatal("closed target lock still authenticates")
	}
}

func targetEnvelopeForTest(t *testing.T) []byte {
	t.Helper()
	request := BuildRemoteRequest(remoteTestBundle(t), "target", "", time.Unix(100, 0).UTC())
	raw, err := EncodeEnvelope(Envelope{SchemaVersion: EnvelopeSchemaVersion, Kind: EnvelopeKind, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
