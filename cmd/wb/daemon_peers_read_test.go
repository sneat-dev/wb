package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/dashboard"
	"github.com/sneat-dev/wb/internal/peers"
)

// TestPeersReadParityAcrossHubAndLocalMounts is S5's read-parity test: the
// REAL /v0/workbench/peers route (through composeWorkbenchAPI, exactly as
// buildHubMount wires it) and the REAL /api/v1/peers route (through
// dashboard.NewHandler, exactly as serveDashboard wires it) must answer
// byte-identical JSON for the same underlying peer trust data — list and
// get-by-name alike, including status derivation (active/blocked) and
// node_id truncation.
func TestPeersReadParityAcrossHubAndLocalMounts(t *testing.T) {
	mount, err := mountHub(context.Background(), memoryHubConfig(t), "127.0.0.1:0", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })

	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	longNodeID := "0123456789abcdef0123456789abcdef"
	if err := mount.PeerAdmin.Trust.CreatePeer(context.Background(), hub.PeerRecord{
		MachineID: hub.MachineID("local", "laptop"), Name: "laptop", IdentityID: "local",
		NodeID: longNodeID, Trust: hub.PeerTrustActive, CreatedAt: now, TrustChangedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mount.PeerAdmin.Trust.CreatePeer(context.Background(), hub.PeerRecord{
		MachineID: hub.MachineID("local", "old-laptop"), Name: "old-laptop", IdentityID: "local",
		Trust: hub.PeerTrustBlocked, CreatedAt: now, TrustChangedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// Seed a queue-state document directly, as Task 3/4's own writer would
	// have left it — nothing else writes queued_work yet. The collection
	// name and firestore tag mirror hub's own unexported
	// repositoryEventQueueCollection/repositoryEventQueueState exactly
	// (see hub/repository_event_store.go); PeerAdmin.Backend is the same
	// raw document store hub.NewQueuedWorkStore(store) reads in
	// buildHubMount.
	if err := mount.PeerAdmin.Backend.Set(context.Background(), "workbench_repository_event_queues", hub.MachineID("local", "laptop"), struct {
		QueuedWork int64 `firestore:"queued_work"`
	}{QueuedWork: 42}); err != nil {
		t.Fatal(err)
	}

	hubHandler := mount.Mounts[hub.APIPrefix+"/"]
	localHandler := dashboard.NewHandler(dashboard.Options{
		Peers: peers.NewHandler("/api/v1/peers", mount.PeersSource, nil),
	})

	hubList := httptest.NewRecorder()
	hubHandler.ServeHTTP(hubList, httptest.NewRequest(http.MethodGet, hub.APIPrefix+"/peers", nil))
	localList := httptest.NewRecorder()
	localHandler.ServeHTTP(localList, httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil))
	if hubList.Code != http.StatusOK || localList.Code != http.StatusOK {
		t.Fatalf("list status = hub:%d local:%d, want 200/200", hubList.Code, localList.Code)
	}
	if hubList.Body.String() != localList.Body.String() {
		t.Fatalf("list JSON differs:\nhub:   %s\nlocal: %s", hubList.Body.String(), localList.Body.String())
	}
	var listBody peers.ListResponse
	if err := json.Unmarshal(hubList.Body.Bytes(), &listBody); err != nil || len(listBody.Peers) != 2 {
		t.Fatalf("list body = %s, %v", hubList.Body.String(), err)
	}
	byName := map[string]peers.Record{}
	for _, record := range listBody.Peers {
		byName[record.Name] = record
	}
	if byName["laptop"].Status != "offline" || byName["laptop"].NodeID != longNodeID[:8] {
		t.Fatalf("laptop record = %+v, want status offline and an 8-char node id", byName["laptop"])
	}
	if byName["laptop"].Lag == nil || *byName["laptop"].Lag != 42 {
		t.Fatalf("laptop record = %+v, want lag=42 from the seeded queue-state document", byName["laptop"])
	}
	if byName["old-laptop"].Lag != nil {
		t.Fatalf("old-laptop record = %+v, want lag=nil (no queue-state document seeded for it)", byName["old-laptop"])
	}
	if byName["old-laptop"].Status != "blocked" {
		t.Fatalf("old-laptop record = %+v, want status blocked", byName["old-laptop"])
	}

	// Get-by-name parity, for both an active and a blocked peer.
	for _, name := range []string{"laptop", "old-laptop"} {
		hubGet := httptest.NewRecorder()
		hubHandler.ServeHTTP(hubGet, httptest.NewRequest(http.MethodGet, hub.APIPrefix+"/peers/"+name, nil))
		localGet := httptest.NewRecorder()
		localHandler.ServeHTTP(localGet, httptest.NewRequest(http.MethodGet, "/api/v1/peers/"+name, nil))
		if hubGet.Code != http.StatusOK || localGet.Code != http.StatusOK {
			t.Fatalf("get(%s) status = hub:%d local:%d, want 200/200", name, hubGet.Code, localGet.Code)
		}
		if hubGet.Body.String() != localGet.Body.String() {
			t.Fatalf("get(%s) JSON differs:\nhub:   %s\nlocal: %s", name, hubGet.Body.String(), localGet.Body.String())
		}
	}

	// Get by machine ID parity too (the hub's own generated ID, not the
	// display name).
	machineID := hub.MachineID("local", "laptop")
	hubGetByID := httptest.NewRecorder()
	hubHandler.ServeHTTP(hubGetByID, httptest.NewRequest(http.MethodGet, hub.APIPrefix+"/peers/"+machineID, nil))
	localGetByID := httptest.NewRecorder()
	localHandler.ServeHTTP(localGetByID, httptest.NewRequest(http.MethodGet, "/api/v1/peers/"+machineID, nil))
	if hubGetByID.Code != http.StatusOK || localGetByID.Code != http.StatusOK {
		t.Fatalf("get-by-id status = hub:%d local:%d, want 200/200", hubGetByID.Code, localGetByID.Code)
	}
	if hubGetByID.Body.String() != localGetByID.Body.String() {
		t.Fatalf("get-by-id JSON differs:\nhub:   %s\nlocal: %s", hubGetByID.Body.String(), localGetByID.Body.String())
	}
}

// TestLaptopWithoutHubPeersAPIAnswersEmptyListNotHTML is B2's server-level
// regression test: a laptop daemon with no hub: section still mounts
// /api/v1/peers, answering an empty JSON list and a JSON 404 for a specific
// id — never the dashboard's HTML index, which is what broke `wb peers
// list`/`get` before this route was mounted unconditionally.
func TestLaptopWithoutHubPeersAPIAnswersEmptyListNotHTML(t *testing.T) {
	handler := dashboard.NewHandler(dashboard.Options{
		Peers: peers.NewHandler("/api/v1/peers", emptyPeersSource{}, nil),
	})

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", list.Code)
	}
	if contentType := list.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("list content-type = %q, want application/json (not the dashboard's HTML index)", contentType)
	}
	var body peers.ListResponse
	if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil || len(body.Peers) != 0 || body.SchemaVersion != peers.SchemaVersion {
		t.Fatalf("list body = %s, %v, want an empty, schema-versioned list", list.Body.String(), err)
	}

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/peers/laptop", nil))
	if get.Code != http.StatusNotFound {
		t.Fatalf("get status = %d, want 404", get.Code)
	}
	if contentType := get.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("get content-type = %q, want application/json", contentType)
	}
}

// TestEmptyPeersSourceIsAlwaysEmpty covers emptyPeersSource's two methods
// directly.
func TestEmptyPeersSourceIsAlwaysEmpty(t *testing.T) {
	var source peers.Source = emptyPeersSource{}
	list, err := source.ListPeers(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("ListPeers = %v, %v, want an empty, nil-error list", list, err)
	}
	_, found, err := source.GetPeer(context.Background(), "anything")
	if err != nil || found {
		t.Fatalf("GetPeer = found=%t, %v, want false, nil", found, err)
	}
}

// TestHubMountPeersSourceIsNilSafe covers mount.peersSource()'s nil-receiver
// branch directly: a *hubMount that is nil (no hub section at all) answers
// nil, letting the caller fall back to emptyPeersSource uniformly.
func TestHubMountPeersSourceIsNilSafe(t *testing.T) {
	var mount *hubMount
	if source := mount.peersSource(); source != nil {
		t.Fatalf("nil *hubMount.peersSource() = %v, want nil", source)
	}
}

// TestPeersViewerAuthorizeAlwaysAdmitsTheLoopbackOperator covers the
// authorize hook every peers mount wires: it matches the always-true
// loopback-operator viewer the sibling read routes already use, so it never
// refuses a request today.
func TestPeersViewerAuthorizeAlwaysAdmitsTheLoopbackOperator(t *testing.T) {
	authorize := peersViewerAuthorize("local")
	if err := authorize(httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil)); err != nil {
		t.Fatalf("peersViewerAuthorize refused a loopback request: %v", err)
	}
}
