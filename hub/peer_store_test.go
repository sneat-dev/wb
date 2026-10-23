package hub

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCollectionsListsPeerCollections(t *testing.T) {
	found := map[string]bool{}
	for _, collection := range Collections() {
		found[collection] = true
	}
	if !found[peerTrustCollection] || !found[peerStatsCollection] {
		t.Fatalf("Collections() = %v, want it to include %q and %q", Collections(), peerTrustCollection, peerStatsCollection)
	}
}

func TestPeerTrustStoreCreateGetAndFind(t *testing.T) {
	ctx := context.Background()
	trust, stats := NewPeerStores(newFirestoreMemoryBackend())
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	record := PeerRecord{MachineID: "machine_1", Name: "laptop", IdentityID: "local", Trust: PeerTrustActive, CreatedAt: now, TrustChangedAt: now}

	if _, found, err := trust.GetPeer(ctx, "machine_1"); err != nil || found {
		t.Fatalf("GetPeer before create = found=%t err=%v, want not found", found, err)
	}
	if err := trust.CreatePeer(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := trust.CreatePeer(ctx, record); !errors.Is(err, ErrPeerExists) {
		t.Fatalf("second CreatePeer = %v, want ErrPeerExists", err)
	}
	got, found, err := trust.GetPeer(ctx, "machine_1")
	if err != nil || !found || got != record {
		t.Fatalf("GetPeer = %+v, %t, %v, want %+v, true, nil", got, found, err, record)
	}
	byName, found, err := trust.FindPeerByName(ctx, "laptop")
	if err != nil || !found || byName != record {
		t.Fatalf("FindPeerByName = %+v, %t, %v", byName, found, err)
	}
	if _, found, err := trust.FindPeerByName(ctx, "no-such-peer"); err != nil || found {
		t.Fatalf("FindPeerByName(unknown) = found=%t err=%v", found, err)
	}

	if err := stats.CreateStats(ctx, "machine_1"); err != nil {
		t.Fatal(err)
	}
	statsRecord, found, err := stats.GetStats(ctx, "machine_1")
	if err != nil || !found || statsRecord.MachineID != "machine_1" || statsRecord.RXEvents != 0 {
		t.Fatalf("GetStats = %+v, %t, %v", statsRecord, found, err)
	}
	// A repeated CreateStats must not clobber a document a later writer has
	// already started filling in.
	if err := stats.CreateStats(ctx, "machine_1"); err != nil {
		t.Fatal(err)
	}
}

func TestPeerTrustStoreListPeers(t *testing.T) {
	ctx := context.Background()
	trust, _ := NewPeerStores(newFirestoreMemoryBackend())
	now := time.Now().UTC()
	for _, name := range []string{"a", "b", "c"} {
		if err := trust.CreatePeer(ctx, PeerRecord{MachineID: "machine_" + name, Name: name, IdentityID: "local", Trust: PeerTrustActive, CreatedAt: now, TrustChangedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := trust.ListPeers(ctx)
	if err != nil || len(records) != 3 {
		t.Fatalf("ListPeers = %+v, %v, want 3 records", records, err)
	}
}

func TestPeerTrustStoreUpdateTrustMergesAndRejectsUnknown(t *testing.T) {
	ctx := context.Background()
	trust, _ := NewPeerStores(newFirestoreMemoryBackend())
	now := time.Now().UTC()
	record := PeerRecord{MachineID: "machine_1", Name: "laptop", IdentityID: "local", Trust: PeerTrustActive, CreatedAt: now, TrustChangedAt: now}
	if err := trust.CreatePeer(ctx, record); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	updated, err := trust.UpdateTrust(ctx, "machine_1", func(r *PeerRecord) {
		r.Trust = PeerTrustBlocked
		r.TrustChangedAt = later
	})
	if err != nil || updated.Trust != PeerTrustBlocked || !updated.TrustChangedAt.Equal(later) || updated.Name != "laptop" || !updated.CreatedAt.Equal(now) {
		t.Fatalf("UpdateTrust = %+v, %v", updated, err)
	}
	stored, found, err := trust.GetPeer(ctx, "machine_1")
	if err != nil || !found || stored.Trust != PeerTrustBlocked {
		t.Fatalf("GetPeer after UpdateTrust = %+v, %t, %v", stored, found, err)
	}
	if _, err := trust.UpdateTrust(ctx, "no-such-machine", func(*PeerRecord) {}); !errors.Is(err, ErrPeerNotFound) {
		t.Fatalf("UpdateTrust(unknown) = %v, want ErrPeerNotFound", err)
	}
	if _, err := trust.UpdateTrust(ctx, "machine_1", nil); err == nil {
		t.Fatal("UpdateTrust with a nil mutation must be rejected")
	}
	if _, err := trust.UpdateTrust(ctx, "machine_1", func(r *PeerRecord) { r.MachineID = "different" }); err == nil {
		t.Fatal("UpdateTrust must reject a mutation that changes the MachineID")
	}
}

func TestPeerStoresReportBackendFailures(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	backend.failGet = failOnCollection(peerTrustCollection)
	trust, stats := NewPeerStores(backend)
	if _, _, err := trust.GetPeer(ctx, "machine_1"); err == nil {
		t.Fatal("GetPeer must surface a backend Get failure")
	}
	if err := trust.CreatePeer(ctx, PeerRecord{MachineID: "machine_1", Name: "laptop", IdentityID: "local", Trust: PeerTrustActive, CreatedAt: time.Now()}); err == nil {
		t.Fatal("CreatePeer must surface a transactional Get failure")
	}
	if _, err := trust.ListPeers(ctx); err != nil {
		// Query is unaffected by failGet.
		t.Fatalf("ListPeers unexpected error: %v", err)
	}
	backend2 := newFirestoreMemoryBackend()
	backend2.failQuery = failQueryOnCollection(peerTrustCollection)
	trust2, _ := NewPeerStores(backend2)
	if _, err := trust2.ListPeers(ctx); err == nil {
		t.Fatal("ListPeers must surface a backend Query failure")
	}
	if _, _, err := trust2.FindPeerByName(ctx, "laptop"); err == nil {
		t.Fatal("FindPeerByName must surface a backend Query failure")
	}

	backend3 := newFirestoreMemoryBackend()
	backend3.failGet = failOnCollection(peerStatsCollection)
	_, stats3 := NewPeerStores(backend3)
	if err := stats3.CreateStats(ctx, "machine_1"); err == nil {
		t.Fatal("CreateStats must surface a transactional Get failure")
	}
	if _, _, err := stats3.GetStats(ctx, "machine_1"); err == nil {
		t.Fatal("GetStats must surface a backend Get failure")
	}
	_ = stats
}

// TestPeerStoresReportSetAndUpdateTrustBackendFailures covers the Set-side
// backend-error branches CreatePeer/UpdateTrust/CreateStats each have,
// beyond the Get-side ones TestPeerStoresReportBackendFailures already
// covers, plus UpdateTrust's own transactional Get failure.
func TestPeerStoresReportSetAndUpdateTrustBackendFailures(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	setFailBackend := newFirestoreMemoryBackend()
	setFailBackend.failSet = failOnCollection(peerTrustCollection)
	trustSetFail, _ := NewPeerStores(setFailBackend)
	if err := trustSetFail.CreatePeer(ctx, PeerRecord{MachineID: "machine_1", Name: "laptop", IdentityID: "local", Trust: PeerTrustActive, CreatedAt: now, TrustChangedAt: now}); err == nil {
		t.Fatal("CreatePeer must surface a transactional Set failure")
	}

	getFailBackend := newFirestoreMemoryBackend()
	trustOK, _ := NewPeerStores(getFailBackend)
	if err := trustOK.CreatePeer(ctx, PeerRecord{MachineID: "machine_1", Name: "laptop", IdentityID: "local", Trust: PeerTrustActive, CreatedAt: now, TrustChangedAt: now}); err != nil {
		t.Fatal(err)
	}
	getFailBackend.failGet = failOnCollection(peerTrustCollection)
	if _, err := trustOK.UpdateTrust(ctx, "machine_1", func(*PeerRecord) {}); err == nil {
		t.Fatal("UpdateTrust must surface a transactional Get failure")
	}
	getFailBackend.failGet = nil
	getFailBackend.failSet = failOnCollection(peerTrustCollection)
	if _, err := trustOK.UpdateTrust(ctx, "machine_1", func(*PeerRecord) {}); err == nil {
		t.Fatal("UpdateTrust must surface a transactional Set failure")
	}

	statsSetFailBackend := newFirestoreMemoryBackend()
	statsSetFailBackend.failSet = failOnCollection(peerStatsCollection)
	_, statsSetFail := NewPeerStores(statsSetFailBackend)
	if err := statsSetFail.CreateStats(ctx, "machine_1"); err == nil {
		t.Fatal("CreateStats must surface a transactional Set failure")
	}
}

func TestPeerStoresReportUnconfiguredBackend(t *testing.T) {
	ctx := context.Background()
	var trust PeerTrustStore = peerTrustStore{}
	var stats PeerStatsStore = peerStatsStore{}
	if _, _, err := trust.GetPeer(ctx, "machine_1"); err == nil {
		t.Fatal("GetPeer with no backend must fail")
	}
	if err := trust.CreatePeer(ctx, PeerRecord{}); err == nil {
		t.Fatal("CreatePeer with no backend must fail")
	}
	if _, _, err := trust.FindPeerByName(ctx, "laptop"); err == nil {
		t.Fatal("FindPeerByName with no backend must fail")
	}
	if _, err := trust.ListPeers(ctx); err == nil {
		t.Fatal("ListPeers with no backend must fail")
	}
	if _, err := trust.UpdateTrust(ctx, "machine_1", func(*PeerRecord) {}); err == nil {
		t.Fatal("UpdateTrust with no backend must fail")
	}
	if err := stats.CreateStats(ctx, "machine_1"); err == nil {
		t.Fatal("CreateStats with no backend must fail")
	}
	if _, _, err := stats.GetStats(ctx, "machine_1"); err == nil {
		t.Fatal("GetStats with no backend must fail")
	}
}

func TestPeerTrustStoreCreatePeerRejectsInvalidRecord(t *testing.T) {
	trust, _ := NewPeerStores(newFirestoreMemoryBackend())
	if err := trust.CreatePeer(context.Background(), PeerRecord{}); err == nil {
		t.Fatal("CreatePeer must reject an invalid record")
	}
}

func TestPeerTrustStoreGetPeerRejectsAStoredInvalidRecord(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	backend.putDocument(peerTrustCollection, "machine_1", PeerRecord{MachineID: "machine_1"})
	trust, _ := NewPeerStores(backend)
	if _, _, err := trust.GetPeer(ctx, "machine_1"); err == nil {
		t.Fatal("GetPeer must reject an invalid stored record")
	}
	if _, _, err := trust.FindPeerByName(ctx, ""); err == nil {
		t.Fatal("FindPeerByName must reject an invalid stored record surfaced through ListPeers")
	}
	if _, err := trust.ListPeers(ctx); err == nil {
		t.Fatal("ListPeers must reject an invalid stored record")
	}
}
