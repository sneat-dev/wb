package sessionpark

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestSpCovResumeSurfaceFailsClosed(t *testing.T) {
	store := NewStore(spCovStoreRoot(t))
	if _, err := store.Resume("..", session.Record{PID: 1, WBSessionID: "wbs-x"}, time.Now()); err == nil {
		t.Fatal("resume with an invalid park ID accepted")
	}

	bundle := remoteTestBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	if _, err := store.PrepareRemoteUnderLock(lock, "target", "", string(sessionmove.CourierSSH), testParkedSSH(), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resume(bundle.ParkedSessionID, session.Record{PID: 1, WBSessionID: "wbs-x"}, time.Now()); err == nil {
		t.Fatal("local resume accepted over a durably claimed remote route")
	}
}

func TestSpCovSourceAcquireRejectsMissingAggregateInPrivateStore(t *testing.T) {
	store := NewStore(spCovStoreRoot(t))
	if _, err := store.Acquire(context.Background(), "park-test"); err == nil {
		t.Fatal("missing aggregate acquired from a valid private store root")
	}
}

func TestSpCovLoadRejectsTamperedBundleEncoding(t *testing.T) {
	t.Run("malformed JSON", func(t *testing.T) {
		store, bundle := spCovCreatedStore(t)
		path := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceBundleFileName)
		spCovWriteRaw(t, path, []byte("{not json"), 0o600)
		if _, err := store.Load(bundle.ParkedSessionID); err == nil || !strings.Contains(err.Error(), "parse parked session bundle") {
			t.Fatalf("malformed bundle error = %v", err)
		}
	})
	t.Run("noncanonical JSON", func(t *testing.T) {
		store, bundle := spCovCreatedStore(t)
		compact, err := json.Marshal(bundle)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceBundleFileName)
		spCovWriteRaw(t, path, compact, 0o600)
		if _, err := store.Load(bundle.ParkedSessionID); err == nil || !strings.Contains(err.Error(), "canonical JSON") {
			t.Fatalf("noncanonical bundle error = %v", err)
		}
	})
}

func TestSpCovClaimResumeRouteRefusesResumedAggregateWithoutMarker(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	if _, _, err := store.PrepareLocalUnderLock(lock, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeUnderLock(lock, session.Record{PID: 11, WBSessionID: "wbs-x"}, time.Unix(150, 0)); err != nil {
		t.Fatal(err)
	}
	routePath := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceResumeRouteFileName)
	if err := os.Remove(routePath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.claimResumeRouteUnderLock(lock, ResumeRouteLocal, "", "", sessionmove.SSHConfig{}, time.Now()); err == nil ||
		!strings.Contains(err.Error(), "no authenticated route marker") {
		t.Fatalf("resumed aggregate without marker error = %v", err)
	}
}

func TestSpCovRemoteAdmissionRequiresClaimedRoute(t *testing.T) {
	store := NewStore(spCovStoreRoot(t))
	bundle := remoteTestBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	request := BuildRemoteRequest(bundle, "target", "", time.Unix(100, 0).UTC())
	raw, err := EncodeEnvelope(Envelope{SchemaVersion: EnvelopeSchemaVersion, Kind: EnvelopeKind, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	admission := RemoteAdmission{Envelope: Envelope{SchemaVersion: EnvelopeSchemaVersion, Kind: EnvelopeKind, Request: request},
		Raw: raw, Digest: sessionmove.DigestBytes(raw)}
	if _, err := store.LoadRemoteReceiptUnderLock(lock, admission); err == nil {
		t.Fatal("receipt loaded without a durably claimed route")
	}
}

func TestSpCovUnderLockLoadFailsWhenEventEvidenceIsMissing(t *testing.T) {
	store, bundle := spCovCreatedStore(t)
	lock := spCovAcquire(t, store, bundle.ParkedSessionID)
	eventsDir := filepath.Join(spCovAggregatePath(store.Root, bundle.ParkedSessionID), sourceEventsDirName)
	if err := os.RemoveAll(eventsDir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareRemoteUnderLock(lock, "target", "", string(sessionmove.CourierSSH), testParkedSSH(), time.Now()); err == nil {
		t.Fatal("remote admission prepared without event evidence")
	}
	if _, _, err := store.claimResumeRouteUnderLock(lock, ResumeRouteLocal, "", "", sessionmove.SSHConfig{}, time.Now()); err == nil {
		t.Fatal("resume route claimed without event evidence")
	}
}
