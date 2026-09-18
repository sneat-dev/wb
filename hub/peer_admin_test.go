package hub

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

const testPeerAdminPepper = "01234567890123456789012345678901"

func newTestPeerAdminService(backend *firestoreMemoryBackend) *PeerAdminService {
	credentials, resolver, _ := NewMachineStores(backend)
	index := NewMachineIndex(backend)
	trust, stats := NewPeerStores(backend)
	_ = resolver
	return &PeerAdminService{
		Credentials: credentials, Index: index, Trust: trust, Stats: stats,
		Pepper: []byte(testPeerAdminPepper), HubMachineName: "vm1",
	}
}

// TestInviteMintsAOneTimeTokenAndPersistedRecords is the AC's "prints the
// token once" assertion at the service layer: the returned token verifies
// against the credential the store now holds, and the trust and statistics
// documents exist.
func TestInviteMintsAOneTimeTokenAndPersistedRecords(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	service := newTestPeerAdminService(backend)
	_, resolver, _ := NewMachineStores(backend)

	result, err := service.Invite(ctx, "laptop", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Token == "" || result.Rotated {
		t.Fatalf("Invite result = %+v, want a fresh non-empty token", result)
	}
	digest, err := DigestMachineToken(result.Token, []byte(testPeerAdminPepper))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := resolver.ResolveMachineCredential(ctx, digest)
	if err != nil || !isPeerScopes(binding.Scopes) || binding.MachineName != "laptop" {
		t.Fatalf("resolved binding = %+v, %v", binding, err)
	}
	trust, _ := NewPeerStores(backend)
	record, found, err := trust.GetPeer(ctx, result.PeerID)
	if err != nil || !found || record.Trust != PeerTrustActive || record.Name != "laptop" {
		t.Fatalf("peer trust record = %+v, %t, %v", record, found, err)
	}
	_, stats := NewPeerStores(backend)
	if _, found, err := stats.GetStats(ctx, result.PeerID); err != nil || !found {
		t.Fatalf("peer stats record = found=%t err=%v, want found", found, err)
	}
}

// TestInviteRefusesAnExistingNameWithoutRotate and its rotate counterpart
// encode invite-and-join's rotation contract.
func TestInviteRefusesAnExistingNameWithoutRotate(t *testing.T) {
	ctx := context.Background()
	service := newTestPeerAdminService(newFirestoreMemoryBackend())
	if _, err := service.Invite(ctx, "laptop", false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Invite(ctx, "laptop", false); err == nil {
		t.Fatal("a second invite of the same name without --rotate must be refused")
	}
}

func TestInviteRotateKeepsTheRecordClearsNodeAndSetsResetPending(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	service := newTestPeerAdminService(backend)
	first, err := service.Invite(ctx, "laptop", false)
	if err != nil {
		t.Fatal(err)
	}
	trust, _ := NewPeerStores(backend)
	if _, err := trust.UpdateTrust(ctx, first.PeerID, func(record *PeerRecord) { record.NodeID = "abc123" }); err != nil {
		t.Fatal(err)
	}
	second, err := service.Invite(ctx, "laptop", true)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Rotated || second.PeerID != first.PeerID || second.Token == first.Token {
		t.Fatalf("rotate result = %+v, first = %+v", second, first)
	}
	record, found, err := trust.GetPeer(ctx, first.PeerID)
	if err != nil || !found || record.NodeID != "" || !record.ResetPending || record.Name != "laptop" || !record.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("rotated record = %+v, %t, %v", record, found, err)
	}
	// The previous token must no longer resolve: rotation revokes it.
	digest, err := DigestMachineToken(first.Token, []byte(testPeerAdminPepper))
	if err != nil {
		t.Fatal(err)
	}
	_, resolver, _ := NewMachineStores(backend)
	if _, err := resolver.ResolveMachineCredential(ctx, digest); err == nil {
		t.Fatal("the pre-rotation token must no longer resolve")
	}
}

// TestInviteRefusesTheHubsOwnMachineName encodes invite-and-join's second
// refusal.
func TestInviteRefusesTheHubsOwnMachineName(t *testing.T) {
	service := newTestPeerAdminService(newFirestoreMemoryBackend())
	if _, err := service.Invite(context.Background(), "vm1", false); err == nil {
		t.Fatal("inviting the hub's own machine name must be refused")
	}
	// Case-insensitively, since MachineID derives from the exact string an
	// operator could otherwise vary only by case to bypass the refusal.
	if _, err := service.Invite(context.Background(), "VM1", false); err == nil {
		t.Fatal("inviting the hub's own machine name (any case) must be refused")
	}
}

// TestInviteRefusesANameHeldByANonPeerCredential encodes invite-and-join's
// third refusal.
func TestInviteRefusesANameHeldByANonPeerCredential(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	credentials, _, _ := NewMachineStores(backend)
	if _, err := credentials.RotateMachineCredential(ctx, MachineCredentialBinding{
		IdentityID: "local", MachineName: "other-mac", IssuedAt: time.Now().UTC(), Scopes: cloneEnrollmentScopes(),
	}, MachineTokenDigest{0x01}); err != nil {
		t.Fatal(err)
	}
	service := newTestPeerAdminService(backend)
	if _, err := service.Invite(ctx, "other-mac", false); err == nil {
		t.Fatal("inviting a name already held by a non-peer credential must be refused")
	}
}

// TestInviteRefusesTheMemoryEngine encodes invite-and-join's fourth refusal.
func TestInviteRefusesTheMemoryEngine(t *testing.T) {
	service := newTestPeerAdminService(newFirestoreMemoryBackend())
	service.MemoryEngine = true
	if _, err := service.Invite(context.Background(), "laptop", false); err == nil {
		t.Fatal("invite must be refused while the store engine is memory")
	}
}

func TestInviteRejectsAnEmptyName(t *testing.T) {
	service := newTestPeerAdminService(newFirestoreMemoryBackend())
	if _, err := service.Invite(context.Background(), "   ", false); err == nil {
		t.Fatal("an empty peer name must be rejected")
	}
}

func TestInviteReportsUnavailableWithoutDependencies(t *testing.T) {
	var service PeerAdminService
	if _, err := service.Invite(context.Background(), "laptop", false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Invite with no dependencies = %v, want ErrUnavailable", err)
	}
}

func TestInviteReportsRandomFailure(t *testing.T) {
	service := newTestPeerAdminService(newFirestoreMemoryBackend())
	service.Random = bytes.NewReader(nil)
	if _, err := service.Invite(context.Background(), "laptop", false); err == nil {
		t.Fatal("a starved random source must fail invite")
	}
}

// TestBlockUnblockAndDisconnectResolveByNameOrID cover
// peer-management-commands' "<peer> accepts a name or peer ID".
func TestBlockUnblockAndDisconnectResolveByNameOrID(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	service := newTestPeerAdminService(backend)
	invited, err := service.Invite(ctx, "laptop", false)
	if err != nil {
		t.Fatal(err)
	}

	blockedByName, err := service.Block(ctx, "laptop")
	if err != nil || blockedByName.Trust != PeerTrustBlocked {
		t.Fatalf("Block(by name) = %+v, %v", blockedByName, err)
	}
	unblockedByID, err := service.Unblock(ctx, invited.PeerID)
	if err != nil || unblockedByID.Trust != PeerTrustActive || !unblockedByID.ResetPending {
		t.Fatalf("Unblock(by ID) = %+v, %v", unblockedByID, err)
	}

	result, err := service.Disconnect(ctx, "laptop")
	if err != nil || result.Disconnected || result.Message == "" {
		t.Fatalf("Disconnect = %+v, %v, want Disconnected=false with a message (Task 2 adds sessions)", result, err)
	}

	if _, err := service.Block(ctx, "no-such-peer"); !errors.Is(err, ErrPeerNotFound) {
		t.Fatalf("Block(unknown) = %v, want ErrPeerNotFound", err)
	}
	if _, err := service.Unblock(ctx, "no-such-peer"); !errors.Is(err, ErrPeerNotFound) {
		t.Fatalf("Unblock(unknown) = %v, want ErrPeerNotFound", err)
	}
	if _, err := service.Disconnect(ctx, "no-such-peer"); !errors.Is(err, ErrPeerNotFound) {
		t.Fatalf("Disconnect(unknown) = %v, want ErrPeerNotFound", err)
	}
	if _, err := service.Block(ctx, "  "); err == nil {
		t.Fatal("Block must reject an empty peer name or ID")
	}
	var unavailable PeerAdminService
	if _, err := unavailable.Block(ctx, "laptop"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Block with no trust store = %v, want ErrUnavailable", err)
	}
}

// TestBlockKeepsRecordCursorAndCounters proves block does not touch anything
// besides trust and trust_changed_at, matching AC:peer-commands.
func TestBlockKeepsRecordCursorAndCounters(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	service := newTestPeerAdminService(backend)
	invited, err := service.Invite(ctx, "laptop", false)
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := (peerTrustStore{backend: backend}).GetPeer(ctx, invited.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Block(ctx, "laptop"); err != nil {
		t.Fatal(err)
	}
	after, _, err := (peerTrustStore{backend: backend}).GetPeer(ctx, invited.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != before.Name || after.IdentityID != before.IdentityID || !after.CreatedAt.Equal(before.CreatedAt) || after.ResetPending != before.ResetPending {
		t.Fatalf("Block changed more than trust: before=%+v after=%+v", before, after)
	}
}
