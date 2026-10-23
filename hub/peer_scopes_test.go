package hub

import (
	"context"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

// TestValidScopesAcceptsExactlyTwoSets encodes
// peer-connectivity#req:invite-and-join's scope design: validScopes accepts
// the existing full enrollment set, or the peer set (peer:session alone),
// and rejects everything else — including a mix of the two, a subset, and a
// superset.
func TestValidScopesAcceptsExactlyTwoSets(t *testing.T) {
	if !validScopes(cloneEnrollmentScopes()) {
		t.Fatal("the enrollment scope set must remain valid")
	}
	if !validScopes(clonePeerScopes()) {
		t.Fatal("the peer scope set (peer:session alone) must be valid")
	}
	if validScopes(nil) {
		t.Fatal("empty scopes must be rejected")
	}
	if validScopes([]MachineScope{ScopePeerSession, ScopeEventsPoll}) {
		t.Fatal("a mix of peer and enrollment scopes must be rejected")
	}
	if validScopes([]MachineScope{ScopeSnapshotPublish, ScopeSnapshotRead, ScopeEventsPoll}) {
		t.Fatal("a subset of the enrollment scopes must be rejected")
	}
	if validScopes(append(cloneEnrollmentScopes(), ScopePeerSession)) {
		t.Fatal("a superset of the enrollment scopes must be rejected")
	}
}

// TestIsPeerScopesDistinguishesTheTwoValidSets is what
// peerAwareBearerResolver and any future caller use to tell a peer
// credential from an enrollment credential once validScopes has already
// passed.
func TestIsPeerScopesDistinguishesTheTwoValidSets(t *testing.T) {
	if !isPeerScopes(clonePeerScopes()) {
		t.Fatal("the peer scope set must be recognised as peer scopes")
	}
	if isPeerScopes(cloneEnrollmentScopes()) {
		t.Fatal("the enrollment scope set must not be recognised as peer scopes")
	}
	if isPeerScopes(nil) {
		t.Fatal("no scopes must not be recognised as peer scopes")
	}
}

// TestPeerTokenCannotOpenSnapshotOrEventRoutes is the AC's "peer token scope"
// assertion at the service layer: MachineSnapshotService and
// RepositoryEventService gate every operation on hasScope, and a peer
// credential's only scope is peer:session, so every one of these calls must
// be refused regardless of how the machine bearer resolved.
func TestPeerTokenCannotOpenSnapshotOrEventRoutes(t *testing.T) {
	ctx := context.Background()
	peer := Machine{ID: "machine_peer", Name: "laptop", IdentityID: "local", Scopes: clonePeerScopes()}

	snapshots := MachineSnapshotService{Store: machineSnapshotStore{backend: newFirestoreMemoryBackend()}}
	if _, err := snapshots.Publish(ctx, peer, validSnapshot(time.Now())); err != ErrUnauthorized {
		t.Fatalf("Publish with a peer token = %v, want ErrUnauthorized", err)
	}
	if _, err := snapshots.List(ctx, peer); err != ErrUnauthorized {
		t.Fatalf("List with a peer token = %v, want ErrUnauthorized", err)
	}

	events := RepositoryEventService{Store: &coverageEventStore{}}
	if _, err := events.Poll(ctx, peer, "", 1, 0); err != ErrUnauthorized {
		t.Fatalf("Poll with a peer token = %v, want ErrUnauthorized", err)
	}
	if _, err := events.Acknowledge(ctx, peer, repositoryevent.AckRequest{}); err != ErrUnauthorized {
		t.Fatalf("Acknowledge with a peer token = %v, want ErrUnauthorized", err)
	}
}
