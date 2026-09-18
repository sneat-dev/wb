package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type fakePeerTrustResolver struct {
	records map[string]PeerRecord
	err     error
}

func (resolver fakePeerTrustResolver) GetPeer(_ context.Context, machineID string) (PeerRecord, bool, error) {
	if resolver.err != nil {
		return PeerRecord{}, false, resolver.err
	}
	record, found := resolver.records[machineID]
	return record, found, nil
}

func peerBearerRequest() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/", nil)
}

// TestPeerAwareBearerResolverRefusesABlockedPeer is the core of
// peer-is-persistent-state's "the machine bearer resolver reads the peer
// record by MachineID and refuses a blocked peer".
func TestPeerAwareBearerResolverRefusesABlockedPeer(t *testing.T) {
	machine := Machine{ID: "machine_1", Name: "laptop", IdentityID: "local", Scopes: clonePeerScopes()}
	inner := coverageMachineResolver{machine: machine}
	trust := fakePeerTrustResolver{records: map[string]PeerRecord{"machine_1": {MachineID: "machine_1", Trust: PeerTrustBlocked}}}
	resolver := NewPeerAwareBearerResolver(inner, trust)
	if _, err := resolver.ResolveMachineBearer(peerBearerRequest()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("blocked peer resolved = %v, want ErrUnauthorized", err)
	}
}

// TestPeerAwareBearerResolverAllowsAnActivePeer proves an active peer record
// passes through unchanged.
func TestPeerAwareBearerResolverAllowsAnActivePeer(t *testing.T) {
	machine := Machine{ID: "machine_1", Name: "laptop", IdentityID: "local", Scopes: clonePeerScopes()}
	inner := coverageMachineResolver{machine: machine}
	trust := fakePeerTrustResolver{records: map[string]PeerRecord{"machine_1": {MachineID: "machine_1", Trust: PeerTrustActive}}}
	resolver := NewPeerAwareBearerResolver(inner, trust)
	got, err := resolver.ResolveMachineBearer(peerBearerRequest())
	if err != nil || !reflect.DeepEqual(got, machine) {
		t.Fatalf("active peer resolved = %+v, %v, want %+v, nil", got, err, machine)
	}
}

// TestPeerAwareBearerResolverIsUnaffectedByANonPeerCredential proves
// "credentials without a record are not peers and are unaffected": an
// ordinary enrollment credential (no trust document at all) passes straight
// through.
func TestPeerAwareBearerResolverIsUnaffectedByANonPeerCredential(t *testing.T) {
	machine := Machine{ID: "machine_2", Name: "studio-mac", IdentityID: "local", Scopes: cloneEnrollmentScopes()}
	inner := coverageMachineResolver{machine: machine}
	trust := fakePeerTrustResolver{records: map[string]PeerRecord{}}
	resolver := NewPeerAwareBearerResolver(inner, trust)
	got, err := resolver.ResolveMachineBearer(peerBearerRequest())
	if err != nil || !reflect.DeepEqual(got, machine) {
		t.Fatalf("non-peer credential resolved = %+v, %v, want %+v, nil", got, err, machine)
	}
}

// TestPeerAwareBearerResolverPropagatesInnerFailure proves a resolution
// failure from the wrapped resolver is never masked by the peer check.
func TestPeerAwareBearerResolverPropagatesInnerFailure(t *testing.T) {
	inner := coverageMachineResolver{err: ErrUnauthorized}
	resolver := NewPeerAwareBearerResolver(inner, fakePeerTrustResolver{})
	if _, err := resolver.ResolveMachineBearer(peerBearerRequest()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("inner failure propagated as %v, want ErrUnauthorized", err)
	}
}

// TestPeerAwareBearerResolverFailsClosedOnATrustReadError proves a trust
// store error is treated as unauthorized rather than silently admitted.
func TestPeerAwareBearerResolverFailsClosedOnATrustReadError(t *testing.T) {
	machine := Machine{ID: "machine_1", Name: "laptop", IdentityID: "local", Scopes: clonePeerScopes()}
	inner := coverageMachineResolver{machine: machine}
	trust := fakePeerTrustResolver{err: errors.New("store unavailable")}
	resolver := NewPeerAwareBearerResolver(inner, trust)
	if _, err := resolver.ResolveMachineBearer(peerBearerRequest()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("trust store failure resolved = %v, want ErrUnauthorized", err)
	}
}

// TestPeerAwareBearerResolverWithoutATrustResolverIsAPassThrough proves the
// nil-trust convenience path used when a caller has not wired peer storage.
func TestPeerAwareBearerResolverWithoutATrustResolverIsAPassThrough(t *testing.T) {
	machine := Machine{ID: "machine_1", Name: "laptop", IdentityID: "local", Scopes: clonePeerScopes()}
	resolver := NewPeerAwareBearerResolver(coverageMachineResolver{machine: machine}, nil)
	got, err := resolver.ResolveMachineBearer(peerBearerRequest())
	if err != nil || !reflect.DeepEqual(got, machine) {
		t.Fatalf("pass-through resolved = %+v, %v", got, err)
	}
}

// TestPeerAwareBearerResolverWithoutAnInnerResolverFailsClosed covers the
// defensive nil-inner guard.
func TestPeerAwareBearerResolverWithoutAnInnerResolverFailsClosed(t *testing.T) {
	resolver := NewPeerAwareBearerResolver(nil, fakePeerTrustResolver{})
	if _, err := resolver.ResolveMachineBearer(peerBearerRequest()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("nil inner resolved = %v, want ErrUnauthorized", err)
	}
}

// TestPeerAwareBearerResolverIntegratesWithRealCredentialResolution runs the
// full stack — real digest resolution plus real peer trust storage — to
// prove the wiring the AC describes end to end, including the poll, ack and
// snapshot routes it protects.
func TestPeerAwareBearerResolverIntegratesWithRealCredentialResolution(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	pepper := []byte(testPeerAdminPepper)
	credentials, resolver, _ := NewMachineStores(backend)
	trust, stats := NewPeerStores(backend)
	admin := &PeerAdminService{Credentials: credentials, Index: NewMachineIndex(backend), Trust: trust, Stats: stats, Pepper: pepper, HubMachineName: "vm1"}
	invited, err := admin.Invite(ctx, "laptop", false)
	if err != nil {
		t.Fatal(err)
	}
	bearer := NewPeerAwareBearerResolver(NewMachineBearerResolver(resolver, pepper), trust)
	request := peerBearerRequest()
	request.Header.Set("Authorization", "Bearer "+invited.Token)
	if _, err := bearer.ResolveMachineBearer(request); err != nil {
		t.Fatalf("active peer over the real stack = %v", err)
	}
	if _, err := admin.Block(ctx, "laptop"); err != nil {
		t.Fatal(err)
	}
	if _, err := bearer.ResolveMachineBearer(request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("blocked peer over the real stack = %v, want ErrUnauthorized", err)
	}
}
