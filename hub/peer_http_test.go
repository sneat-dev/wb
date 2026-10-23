package hub

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

// TestDisableSelfHostedEnrollmentUnmountsTheRoute encodes AC:identity-and-
// admission's "on a self-hosted hub, POST /v0/workbench/machines/enroll is
// not mounted", and its counterpart proves the hosted instance — which never
// sets the option — keeps serving it exactly as before.
func TestDisableSelfHostedEnrollmentUnmountsTheRoute(t *testing.T) {
	handler := NewHandler(HandlerOptions{
		ViewerResolver:              coverageViewerResolver{viewer: Viewer{Authenticated: true, IdentityID: "local"}},
		Enrollment:                  &MachineEnrollmentService{},
		DisableSelfHostedEnrollment: true,
	})
	response := coverageRequest(t, handler, http.MethodPost, MachineEnrollmentPath, `{"name":"laptop"}`, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("self-hosted enrollment route status = %d, want 404", response.Code)
	}
}

func TestEnrollmentRouteStaysMountedByDefault(t *testing.T) {
	handler := NewHandler(HandlerOptions{
		ViewerResolver: coverageViewerResolver{viewer: Viewer{Authenticated: true, IdentityID: "local"}},
		Enrollment:     &MachineEnrollmentService{},
	})
	response := coverageRequest(t, handler, http.MethodPost, MachineEnrollmentPath, `{"name":"laptop"}`, nil)
	if response.Code == http.StatusNotFound {
		t.Fatal("the hosted instance's enrollment route must stay mounted by default")
	}
}

// TestPeersConnectProbeAnswersAValidPeerBearer covers AC:identity-and-
// admission's join-verification probe.
func TestPeersConnectProbeAnswersAValidPeerBearer(t *testing.T) {
	machine := Machine{ID: "machine_1", Name: "laptop", IdentityID: "local", Scopes: clonePeerScopes()}
	handler := NewHandler(HandlerOptions{MachineBearer: coverageMachineResolver{machine: machine}})
	response := coverageRequest(t, handler, http.MethodGet, PeersConnectPath, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("peers connect probe status = %d, want 200", response.Code)
	}
	var body peersConnectProbeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SchemaVersion != 1 || body.PeerID != "machine_1" || body.Name != "laptop" {
		t.Fatalf("peers connect probe body = %+v", body)
	}
}

func TestPeersConnectProbeRefusesAWebSocketUpgrade(t *testing.T) {
	machine := Machine{ID: "machine_1", Name: "laptop", IdentityID: "local", Scopes: clonePeerScopes()}
	handler := NewHandler(HandlerOptions{MachineBearer: coverageMachineResolver{machine: machine}})
	response := coverageRequest(t, handler, http.MethodGet, PeersConnectPath, "", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"})
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("peers connect probe with an upgrade request = %d, want 501", response.Code)
	}
}

func TestPeersConnectProbeRefusesAnUnauthenticatedRequest(t *testing.T) {
	handler := NewHandler(HandlerOptions{MachineBearer: coverageMachineResolver{err: ErrUnauthorized}})
	response := coverageRequest(t, handler, http.MethodGet, PeersConnectPath, "", nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("peers connect probe with a bad token = %d, want 401", response.Code)
	}
}

// TestPeersConnectProbeRefusesANonPeerCredential proves a valid, but
// non-peer, credential is refused distinctly from an invalid one.
func TestPeersConnectProbeRefusesANonPeerCredential(t *testing.T) {
	machine := Machine{ID: "machine_2", Name: "studio-mac", IdentityID: "local", Scopes: cloneEnrollmentScopes()}
	handler := NewHandler(HandlerOptions{MachineBearer: coverageMachineResolver{machine: machine}})
	response := coverageRequest(t, handler, http.MethodGet, PeersConnectPath, "", nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("peers connect probe with a non-peer credential = %d, want 403", response.Code)
	}
}

// TestPeersConnectProbeRefusesABlockedPeer proves the peer-aware bearer
// resolver's blocked refusal is what a real deployment sees here too.
func TestPeersConnectProbeRefusesABlockedPeer(t *testing.T) {
	machine := Machine{ID: "machine_1", Name: "laptop", IdentityID: "local", Scopes: clonePeerScopes()}
	inner := coverageMachineResolver{machine: machine}
	trust := fakePeerTrustResolver{records: map[string]PeerRecord{"machine_1": {MachineID: "machine_1", Trust: PeerTrustBlocked}}}
	handler := NewHandler(HandlerOptions{MachineBearer: NewPeerAwareBearerResolver(inner, trust)})
	response := coverageRequest(t, handler, http.MethodGet, PeersConnectPath, "", nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("peers connect probe with a blocked peer = %d, want 401", response.Code)
	}
}

// TestBlockedPeerRefusedOnEveryAuthenticatedRoute is the full-handler
// version of TestPeerAwareBearerResolverRefusesABlockedPeer: it proves the
// same wiring cmd/wb's daemon_hub.go uses (MachineBearer:
// NewPeerAwareBearerResolver(...)) refuses a blocked credential on the probe,
// the HTTP long poll and ack, and the snapshot routes alike — because every
// one of them resolves through the same bearer — while an unrelated
// enrollment credential with no peer trust record is unaffected on the same
// routes.
//
// The blocked test credential carries every scope any of the four routes
// checks (peer:session for the probe, the enrollment scopes for poll/ack/
// snapshot) — a combination Invite and Enroll never actually mint together
// (validScopes only ever admits one of the two sets), but exactly what this
// test needs: with every route's own scope check satisfied, a 401 on all
// four can only come from the block check itself, not from an unrelated
// scope mismatch a peer-scoped-only credential would already have hit on
// poll/ack/snapshot regardless of trust.
func TestBlockedPeerRefusedOnEveryAuthenticatedRoute(t *testing.T) {
	fullScopes := append(append([]MachineScope{}, cloneEnrollmentScopes()...), clonePeerScopes()...)
	blockedPeer := Machine{ID: "machine_1", Name: "laptop", IdentityID: "local", Scopes: fullScopes}
	trust := fakePeerTrustResolver{records: map[string]PeerRecord{"machine_1": {MachineID: "machine_1", Trust: PeerTrustBlocked}}}
	blockedResolver := NewPeerAwareBearerResolver(coverageMachineResolver{machine: blockedPeer}, trust)
	blockedHandler := NewHandler(HandlerOptions{
		MachineBearer:    blockedResolver,
		Snapshots:        &MachineSnapshotService{Store: machineSnapshotStore{backend: newFirestoreMemoryBackend()}},
		RepositoryEvents: &RepositoryEventService{Store: &coverageEventStore{}},
	})
	for _, probe := range []struct {
		method, path, body string
	}{
		{http.MethodGet, PeersConnectPath, ""},
		{http.MethodGet, repositoryevent.EventsPath, ""},
		{http.MethodPost, repositoryevent.AckPath, `{"version":1,"cursor":"c","event_ids":["e"]}`},
		{http.MethodGet, machinesnapshot.SnapshotPath, ""},
	} {
		response := coverageRequest(t, blockedHandler, probe.method, probe.path, probe.body, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s for a blocked peer = %d, want 401", probe.method, probe.path, response.Code)
		}
	}

	// The same trust store, applied to a credential it has never heard of
	// (an ordinary enrollment, not a peer), must not refuse anything: found
	// stays false, and the wrapped resolver's own result passes through.
	nonPeer := Machine{ID: "machine_2", Name: "studio-mac", IdentityID: "local", Scopes: cloneEnrollmentScopes()}
	unaffectedHandler := NewHandler(HandlerOptions{
		MachineBearer:    NewPeerAwareBearerResolver(coverageMachineResolver{machine: nonPeer}, trust),
		RepositoryEvents: &RepositoryEventService{Store: &coverageEventStore{}},
	})
	response := coverageRequest(t, unaffectedHandler, http.MethodGet, repositoryevent.EventsPath, "", nil)
	if response.Code == http.StatusUnauthorized {
		t.Fatal("an unrelated enrollment credential must not be refused by a peer trust store that has no record for it")
	}
}
