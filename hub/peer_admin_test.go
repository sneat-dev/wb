package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
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
		Backend: backend, Pepper: []byte(testPeerAdminPepper), HubMachineName: "vm1",
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

// TestInviteValidatesNameAndReservesConnect is M4: invite names follow the
// same rules as machinesnapshot.ValidateIdentity, and "connect" — the exact
// path segment PeersConnectPath and composeWorkbenchAPI treat specially —
// is reserved case-insensitively so a peer can never become permanently
// unreachable by name through GET /v0/workbench/peers/{id}.
func TestInviteValidatesNameAndReservesConnect(t *testing.T) {
	service := newTestPeerAdminService(newFirestoreMemoryBackend())
	if _, err := service.Invite(context.Background(), "connect", false); err == nil {
		t.Fatal("\"connect\" must be reserved and refused as a peer name")
	}
	if _, err := service.Invite(context.Background(), "CONNECT", false); err == nil {
		t.Fatal("\"connect\" must be reserved case-insensitively")
	}
	if _, err := service.Invite(context.Background(), "bad name with spaces", false); err == nil {
		t.Fatal("an identity ValidateIdentity rejects must be refused")
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

// TestConcurrentInviteOfTheSameNameHasExactlyOneWinner is S2(b): Invite
// writes the credential, trust and statistics documents in one UpdateAtomic
// transaction specifically so two concurrent invites of the same name never
// both succeed. firestoreMemoryBackend's UpdateAtomic now holds its lock for
// the whole callback (see firestore_memory_test.go), so this test exercises
// genuine mutual exclusion, not just sequential calls that happen not to
// race.
func TestConcurrentInviteOfTheSameNameHasExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	service := newTestPeerAdminService(backend)

	const attempts = 8
	results := make([]PeerInviteResult, attempts)
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = service.Invite(ctx, "laptop", false)
		}(i)
	}
	wg.Wait()

	wins, tokens := 0, map[string]bool{}
	for i := 0; i < attempts; i++ {
		if errs[i] == nil {
			wins++
			tokens[results[i].Token] = true
			continue
		}
		if !strings.Contains(errs[i].Error(), "already a peer") {
			t.Fatalf("attempt %d failed with an unexpected error: %v", i, errs[i])
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent invite of the same name had %d winners, want exactly 1", wins)
	}
	if len(tokens) != 1 {
		t.Fatalf("winner token set = %v, want exactly one token", tokens)
	}

	// The winner's token must still resolve: a loser's mint (which happens
	// before the transaction, and is never persisted when the transaction's
	// re-check refuses it) must not have revoked the winner's credential.
	trust, _ := NewPeerStores(backend)
	record, found, err := trust.FindPeerByName(ctx, "laptop")
	if err != nil || !found {
		t.Fatalf("FindPeerByName after the race = %+v, %t, %v", record, found, err)
	}
	var winnerToken string
	for token := range tokens {
		winnerToken = token
	}
	_, resolver, _ := NewMachineStores(backend)
	digest, err := DigestMachineToken(winnerToken, []byte(testPeerAdminPepper))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveMachineCredential(ctx, digest); err != nil {
		t.Fatalf("winner's token no longer resolves after the race: %v", err)
	}
}

// TestMachineIndexHasCredential covers machineIndexStore.HasCredential's
// found/not-found/backend-error/unconfigured branches directly.
func TestMachineIndexHasCredential(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	index := NewMachineIndex(backend)
	if has, err := index.HasCredential(ctx, "machine_absent"); err != nil || has {
		t.Fatalf("HasCredential(absent) = %t, %v, want false, nil", has, err)
	}
	credentials, _, _ := NewMachineStores(backend)
	if _, err := credentials.RotateMachineCredential(ctx, MachineCredentialBinding{
		IdentityID: "local", MachineName: "studio-mac", IssuedAt: time.Now().UTC(), Scopes: cloneEnrollmentScopes(),
	}, MachineTokenDigest{0x02}); err != nil {
		t.Fatal(err)
	}
	machineID := MachineID("local", "studio-mac")
	if has, err := index.HasCredential(ctx, machineID); err != nil || !has {
		t.Fatalf("HasCredential(present) = %t, %v, want true, nil", has, err)
	}

	backend.failGet = failOnCollection(machineEnrollmentCollection)
	if _, err := index.HasCredential(ctx, machineID); err == nil {
		t.Fatal("HasCredential must surface a backend Get failure")
	}

	var unavailable MachineIndex = machineIndexStore{}
	if _, err := unavailable.HasCredential(ctx, "machine_1"); !errors.Is(err, errMachineCredentialUnavailable) {
		t.Fatalf("HasCredential with no backend = %v, want errMachineCredentialUnavailable", err)
	}
}

// TestPeerAdminServiceNowUsesInjectedClock covers the now() helper directly.
func TestPeerAdminServiceNowUsesInjectedClock(t *testing.T) {
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	service := PeerAdminService{Now: func() time.Time { return fixed }}
	if got := service.now(); !got.Equal(fixed) {
		t.Fatalf("now() = %v, want %v", got, fixed)
	}
	if got := (&PeerAdminService{}).now(); got.IsZero() {
		t.Fatal("now() with no injected clock must still return a real time")
	}
}

// TestInviteSurfacesPeerExistsCheckFailure covers Invite's one
// pre-transaction error branch directly (the transaction's own re-checks,
// including the non-peer-credential collision, are covered by
// TestInviteRefusesANameHeldByANonPeerCredential,
// TestConcurrentInviteOfTheSameNameHasExactlyOneWinner, and the store-level
// backend-failure tests in peer_store_test.go). Invite deliberately has no
// second pre-check against Index.HasCredential — see the doc comment on the
// pre-check in peer_admin.go for why a second, differently-timed collection
// read there would itself be a race.
func TestInviteSurfacesPeerExistsCheckFailure(t *testing.T) {
	backend := newFirestoreMemoryBackend()
	backend.failGet = failOnCollection(peerTrustCollection)
	service := newTestPeerAdminService(backend)
	if _, err := service.Invite(context.Background(), "laptop", false); err == nil {
		t.Fatal("Invite must surface a peerExists (Trust.GetPeer) failure")
	}
}

// TestResolvePeerBranches covers resolvePeer's guard, unavailable, and
// backend-failure branches directly, beyond the name/ID/unknown-peer paths
// TestBlockUnblockAndDisconnectResolveByNameOrID already covers.
func TestResolvePeerBranches(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	service := newTestPeerAdminService(backend)
	if _, err := service.resolvePeer(ctx, ""); err == nil {
		t.Fatal("resolvePeer must reject an empty name/ID")
	}
	var noTrust PeerAdminService
	if _, err := noTrust.resolvePeer(ctx, "laptop"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("resolvePeer with no Trust store = %v, want ErrUnavailable", err)
	}
	if _, err := service.Invite(ctx, "laptop", false); err != nil {
		t.Fatal(err)
	}
	backend.failGet = failOnCollection(peerTrustCollection)
	if _, err := service.resolvePeer(ctx, "machine_x"); err == nil {
		t.Fatal("resolvePeer must surface a GetPeer failure")
	}
	backend.failGet = nil
	backend.failQuery = failQueryOnCollection(peerTrustCollection)
	if _, err := service.resolvePeer(ctx, "laptop"); err == nil {
		t.Fatal("resolvePeer must surface a FindPeerByName (Query) failure")
	}
}

// TestPeerAdminServiceIdentityDefaultsToLocal covers identity()'s two
// branches directly.
func TestPeerAdminServiceIdentityDefaultsToLocal(t *testing.T) {
	var service PeerAdminService
	if got := service.identity(); got != "local" {
		t.Fatalf("identity() with LocalIdentityID unset = %q, want \"local\"", got)
	}
	service.LocalIdentityID = "hosted-identity"
	if got := service.identity(); got != "hosted-identity" {
		t.Fatalf("identity() with LocalIdentityID set = %q, want %q", got, "hosted-identity")
	}
}

// TestRefuseIfPeerNameCollision covers every branch of the S4 collision
// guard the owner RPC's "enroll" route calls: the empty-name guard, the
// hub's-own-name refusal, an existing peer's name, a nil Trust store
// (pass-through), a clean name, and a backend failure.
func TestRefuseIfPeerNameCollision(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	service := newTestPeerAdminService(backend)

	if err := service.RefuseIfPeerNameCollision(ctx, "  "); err == nil {
		t.Fatal("expected a refusal for an empty name")
	}
	if err := service.RefuseIfPeerNameCollision(ctx, "vm1"); err == nil {
		t.Fatal("expected a refusal for the hub's own machine name")
	}
	if err := service.RefuseIfPeerNameCollision(ctx, "VM1"); err == nil {
		t.Fatal("expected the hub's own machine name refusal to be case-insensitive")
	}
	if _, err := service.Invite(ctx, "laptop", false); err != nil {
		t.Fatal(err)
	}
	if err := service.RefuseIfPeerNameCollision(ctx, "laptop"); err == nil {
		t.Fatal("expected a refusal for a name that already belongs to a peer")
	}
	if err := service.RefuseIfPeerNameCollision(ctx, "second-mac"); err != nil {
		t.Fatalf("RefuseIfPeerNameCollision(clean name) = %v, want nil", err)
	}

	var noTrust PeerAdminService
	noTrust.HubMachineName = "vm1"
	if err := noTrust.RefuseIfPeerNameCollision(ctx, "anything"); err != nil {
		t.Fatalf("RefuseIfPeerNameCollision with no Trust store = %v, want a pass-through nil", err)
	}

	backend.failQuery = failQueryOnCollection(peerTrustCollection)
	if err := service.RefuseIfPeerNameCollision(ctx, "third-mac"); err == nil {
		t.Fatal("expected RefuseIfPeerNameCollision to surface a backend Query (FindPeerByName) failure")
	}
}

// TestInviteTransactionGuardsAndBackendErrors covers every Get/Set/Delete
// failure branch inside Invite's UpdateAtomic callback (round 3's coverage
// regression), mirroring the fault-injection style
// TestAcknowledgeGuardsAndBackendErrors and TestEnqueueForMachinesGuardsAnd
// BackendErrors already use for the sibling repository-event store.
func TestInviteTransactionGuardsAndBackendErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("enrollment read fails", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failGet = failOnCollection(machineEnrollmentCollection)
		service := newTestPeerAdminService(backend)
		if _, err := service.Invite(ctx, "laptop", false); err == nil {
			t.Fatal("expected Invite to surface the transaction's enrollment Get failure")
		}
	})

	t.Run("trust read fails inside the transaction", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		// The pre-check (peerExists) also reads peerTrustCollection once,
		// before the transaction; skip that call and fail only the
		// transaction's own read.
		backend.failGet = failNthCollectionCall(peerTrustCollection, 2)
		service := newTestPeerAdminService(backend)
		if _, err := service.Invite(ctx, "laptop", false); err == nil {
			t.Fatal("expected Invite to surface the transaction's trust Get failure")
		}
	})

	t.Run("credential digest collision check fails", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failGet = failOnCollection(machineCredentialCollection)
		service := newTestPeerAdminService(backend)
		if _, err := service.Invite(ctx, "laptop", false); err == nil {
			t.Fatal("expected Invite to surface the credential collision Get failure")
		}
	})

	t.Run("credential digest collision found", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		service := newTestPeerAdminService(backend)
		fixedToken := bytes.Repeat([]byte{0x07}, machineTokenBytes)
		service.Random = bytes.NewReader(fixedToken)
		token := base64.RawURLEncoding.EncodeToString(fixedToken)
		digest := digestMachineToken(token, []byte(testPeerAdminPepper))
		backend.putDocument(machineCredentialCollection, machineCredentialID(digest), machineCredentialDocument{})
		if _, err := service.Invite(ctx, "laptop", false); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Invite with a digest collision = %v, want ErrUnavailable", err)
		}
	})

	t.Run("revoke previous credential fails on rotate", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		service := newTestPeerAdminService(backend)
		if _, err := service.Invite(ctx, "laptop", false); err != nil {
			t.Fatal(err)
		}
		backend.failDelete = failOnCollection(machineCredentialCollection)
		if _, err := service.Invite(ctx, "laptop", true); err == nil {
			t.Fatal("expected Invite (rotate) to surface the previous-credential revoke failure")
		}
	})

	t.Run("credential write fails", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failSet = failOnCollection(machineCredentialCollection)
		service := newTestPeerAdminService(backend)
		if _, err := service.Invite(ctx, "laptop", false); err == nil {
			t.Fatal("expected Invite to surface the machine credential write failure")
		}
	})

	t.Run("enrollment write fails", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failSet = failOnCollection(machineEnrollmentCollection)
		service := newTestPeerAdminService(backend)
		if _, err := service.Invite(ctx, "laptop", false); err == nil {
			t.Fatal("expected Invite to surface the machine enrollment write failure")
		}
	})

	t.Run("trust write fails on rotate", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		service := newTestPeerAdminService(backend)
		if _, err := service.Invite(ctx, "laptop", false); err != nil {
			t.Fatal(err)
		}
		backend.failSet = failOnCollection(peerTrustCollection)
		if _, err := service.Invite(ctx, "laptop", true); err == nil {
			t.Fatal("expected Invite (rotate) to surface the trust record write failure")
		}
	})

	t.Run("trust write fails on a fresh invite", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failSet = failOnCollection(peerTrustCollection)
		service := newTestPeerAdminService(backend)
		if _, err := service.Invite(ctx, "laptop", false); err == nil {
			t.Fatal("expected Invite to surface the trust record write failure")
		}
	})

	t.Run("statistics read fails", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failGet = failOnCollection(peerStatsCollection)
		service := newTestPeerAdminService(backend)
		if _, err := service.Invite(ctx, "laptop", false); err == nil {
			t.Fatal("expected Invite to surface the statistics Get failure")
		}
	})

	t.Run("statistics write fails", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failSet = failOnCollection(peerStatsCollection)
		service := newTestPeerAdminService(backend)
		if _, err := service.Invite(ctx, "laptop", false); err == nil {
			t.Fatal("expected Invite to surface the statistics write failure")
		}
	})
}

// TestRotateMachineCredentialRefusesAMachineNameAlreadyClaimedByAPeer is
// M-c: RefuseIfPeerNameCollision's pre-check (the plain "enroll" RPC route's
// only guard before this fix, cmd/wb/daemon_peers.go) and
// MachineEnrollmentService.Enroll's write are two separate, non-atomic
// steps, so a concurrent Invite of the same name could commit its peer trust
// document in the gap between them and still lose to a plain enroll that
// silently rotated its credential. RotateMachineCredential's own transaction
// now re-checks the trust document at the one point that actually decides
// the write, closing that gap regardless of what any earlier, racy pre-check
// saw.
func TestRotateMachineCredentialRefusesAMachineNameAlreadyClaimedByAPeer(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	adminService := newTestPeerAdminService(backend)
	if _, err := adminService.Invite(ctx, "laptop", false); err != nil {
		t.Fatal(err)
	}

	credentials, _, _ := NewMachineStores(backend)

	// The store level, directly: proves the transaction's own message names
	// the actual reason (a peer already holds the name), not just "refused".
	binding := MachineCredentialBinding{IdentityID: "local", MachineName: "laptop", IssuedAt: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)}
	if _, storeErr := credentials.RotateMachineCredential(ctx, binding, MachineTokenDigest{0x09}); storeErr == nil {
		t.Fatal("expected RotateMachineCredential to refuse a machine name a peer already holds")
	} else if !strings.Contains(storeErr.Error(), "already belongs to a peer") {
		t.Fatalf("RotateMachineCredential error = %v, want an already-belongs-to-a-peer refusal", storeErr)
	}

	// The public Enroll path, end to end: MachineEnrollmentService.Enroll
	// collapses every store-side refusal (a digest collision, a backend
	// fault, and now this one) into the same ErrUnavailable, exactly as it
	// already did for the store's other transaction refusals before this
	// fix — this proves the race is closed through the real API surface a
	// concurrent invite-then-enroll actually calls, not only at the store's
	// own layer.
	enrollment := MachineEnrollmentService{
		Store: credentials, Pepper: []byte(testPeerAdminPepper),
		Now: func() time.Time { return time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC) },
	}
	_, err := enrollment.Enroll(ctx, Viewer{Authenticated: true, IdentityID: "local"}, MachineEnrollmentRequest{Name: "laptop"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Enroll error = %v, want ErrUnavailable (as every other RotateMachineCredential refusal already surfaces)", err)
	}

	// The peer's own credential and trust record must survive the refused
	// enroll attempt untouched.
	trust, _ := NewPeerStores(backend)
	record, found, findErr := trust.FindPeerByName(ctx, "laptop")
	if findErr != nil || !found || record.Trust != PeerTrustActive {
		t.Fatalf("peer trust record after the refused enroll = %+v, %t, %v", record, found, findErr)
	}
}

// TestRotateMachineCredentialSurfacesAPeerTrustReadFailure covers M-c's new
// peer-trust re-check's own error branch: a backend fault reading the trust
// document must fail the rotation rather than silently proceeding as if no
// peer held the name.
func TestRotateMachineCredentialSurfacesAPeerTrustReadFailure(t *testing.T) {
	backend := newFirestoreMemoryBackend()
	backend.failGet = failOnCollection(peerTrustCollection)
	credentials, _, _ := NewMachineStores(backend)
	binding := MachineCredentialBinding{IdentityID: "local", MachineName: "laptop", IssuedAt: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)}
	if _, err := credentials.RotateMachineCredential(context.Background(), binding, MachineTokenDigest{0x09}); err == nil {
		t.Fatal("expected RotateMachineCredential to surface the peer trust read failure")
	}
}

// failNthCollectionCall returns a fault-injection hook that fails only the
// nth (1-indexed) call against collection, letting every earlier call (and
// every call against a different collection) through — for a branch a test
// must reach past a pre-check that reads the same collection once already.
func failNthCollectionCall(collection string, n int) func(c, id string) error {
	calls := 0
	return func(c, _ string) error {
		if c != collection {
			return nil
		}
		calls++
		if calls == n {
			return errFirestoreMemoryFault
		}
		return nil
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
