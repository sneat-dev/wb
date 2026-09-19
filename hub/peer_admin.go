package hub

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp"
	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

// reservedPeerName is refused as a peer name: it is the exact path segment
// PeersConnectPath and composeWorkbenchAPI treat specially (the WebSocket
// session route, and its Task 1 verification probe), so a peer admitted
// under this name could never be looked up by name through
// GET /v0/workbench/peers/{id} — that exact path is always routed to the
// connect route instead.
const reservedPeerName = "connect"

// PeerAdminService implements every admission and trust change
// peer-connectivity#req:admin-requires-owner-credential requires: invite,
// rotate, block, unblock and disconnect. It is deliberately not reachable
// from any HTTP route; cmd/wb mounts it only on the daemon's owner-token
// unix-socket RPC.
type PeerAdminService struct {
	// Credentials, Index, Trust and Stats are the seams Block, Unblock,
	// Disconnect, resolvePeer and RefuseIfPeerNameCollision use. Invite does
	// not use Credentials: see Backend.
	Credentials MachineCredentialStore
	Index       MachineIndex
	Trust       PeerTrustStore
	Stats       PeerStatsStore
	// Backend is the raw document store Invite writes the credential, trust
	// and statistics documents against inside one UpdateAtomic transaction,
	// so a partial invite (a credential with no matching trust document, or
	// the reverse) can never be observed. DocumentStore's contract promises
	// serializable transaction semantics, so two concurrent invites of the
	// same name always resolve to exactly one winner with a working token;
	// the loser's writes (including its own randomly minted token) are
	// discarded by the retried transaction rather than partially applied,
	// and it observes ErrPeerExists-shaped refusal instead.
	Backend githubapp.DocumentStore
	Pepper  []byte
	Random  io.Reader
	Now     func() time.Time
	// LocalIdentityID is the fixed self-hosted identity every peer credential
	// is minted under (hub.MachineID's first argument), matching
	// ensureLocalEnrollment's "local".
	LocalIdentityID string
	// HubMachineName is the hub's own machine name. invite-and-join refuses
	// it outright: the hub is never its own peer.
	HubMachineName string
	// MemoryEngine is true when the hub's store engine is "memory". Invite
	// refuses admission in that case, per invite-and-join: a peer credential
	// minted against a store that discards on exit could never be reasoned
	// about as durable state.
	MemoryEngine bool
}

func (service *PeerAdminService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

func (service *PeerAdminService) random() io.Reader {
	if service.Random != nil {
		return service.Random
	}
	return rand.Reader
}

func (service *PeerAdminService) identity() string {
	if service.LocalIdentityID != "" {
		return service.LocalIdentityID
	}
	// "local" matches the one identity a self-hosted daemon knows (cmd/wb's
	// localIdentityID, and MachineEnrollmentService's own self-enrollment
	// path).
	return "local"
}

// PeerInviteResult is what `wb peers invite` and the owner RPC return: the
// token is present exactly once, here, and never retrievable again.
type PeerInviteResult struct {
	PeerID    string
	Name      string
	Token     string
	CreatedAt time.Time
	Rotated   bool
}

// Invite mints a peer credential and its trust/statistics records. name must
// not already belong to the hub's own machine, an existing non-peer
// credential, or an existing peer unless rotate is true. The credential,
// trust and statistics documents are written inside one atomic transaction:
// see Backend's doc comment for why.
func (service *PeerAdminService) Invite(ctx context.Context, name string, rotate bool) (PeerInviteResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return PeerInviteResult{}, errors.New("peer name is required")
	}
	if err := machinesnapshot.ValidateIdentity(name); err != nil {
		return PeerInviteResult{}, fmt.Errorf("peer name is invalid: %w", err)
	}
	if strings.EqualFold(name, reservedPeerName) {
		return PeerInviteResult{}, fmt.Errorf("%q is reserved and cannot be used as a peer name", reservedPeerName)
	}
	if service.MemoryEngine {
		return PeerInviteResult{}, errors.New("wb peers invite is refused while the hub's store engine is memory")
	}
	if service.Backend == nil || service.Trust == nil || service.Stats == nil || len(service.Pepper) < minimumPepperBytes {
		return PeerInviteResult{}, ErrUnavailable
	}
	identity := service.identity()
	if strings.EqualFold(name, service.HubMachineName) {
		return PeerInviteResult{}, fmt.Errorf("%q is this hub's own machine name and can never be a peer", name)
	}
	machineID := MachineID(identity, name)
	// A fast, single-collection, non-transactional pre-check reports the
	// ordinary "already a peer" refusal without minting a token first. Its
	// worst failure mode is a stale "not found" (a concurrent invite commits
	// between this read and the transaction below), which the transaction's
	// own re-check corrects harmlessly by refusing there instead. It
	// deliberately does NOT also pre-check Index.HasCredential: that would
	// read a second, different collection at a second, different instant,
	// and a concurrent invite of the same name completing in the gap
	// between the two reads could make this pre-check see "not a peer yet"
	// and "has a credential" — true of a peer immediately after another
	// caller's invite committed — and misreport it as a non-peer collision
	// instead of the ordinary "already a peer" outcome. The transaction's
	// own check reads both documents from one consistent, serialized point
	// and is the only place that refusal is decided.
	foundPeer, err := service.peerExists(ctx, machineID)
	if err != nil {
		return PeerInviteResult{}, err
	}
	if foundPeer && !rotate {
		return PeerInviteResult{}, fmt.Errorf("%q is already a peer; pass --rotate to reissue its credential", name)
	}
	raw := make([]byte, machineTokenBytes)
	if _, err := io.ReadFull(service.random(), raw); err != nil {
		return PeerInviteResult{}, ErrUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := digestMachineToken(token, service.Pepper)
	credentialID := machineCredentialID(digest)
	now := service.now()
	binding := MachineCredentialBinding{
		IdentityID: identity, MachineName: name, IssuedAt: now, Scopes: clonePeerScopes(),
		MachineID: machineID,
	}

	var result PeerInviteResult
	err = service.Backend.UpdateAtomic(ctx, func(transaction githubapp.DocumentTransaction) error {
		var enrollment machineEnrollmentDocument
		enrollmentFound, err := transaction.Get(ctx, machineEnrollmentCollection, machineID, &enrollment)
		if err != nil {
			return fmt.Errorf("read current machine enrollment: %w", err)
		}
		var trustRecord PeerRecord
		trustFound, err := transaction.Get(ctx, peerTrustCollection, machineID, &trustRecord)
		if err != nil {
			return fmt.Errorf("read current peer trust record: %w", err)
		}
		if trustFound && !rotate {
			return fmt.Errorf("%q is already a peer; pass --rotate to reissue its credential", name)
		}
		if !trustFound && enrollmentFound {
			return fmt.Errorf("%q is already held by a non-peer machine credential", name)
		}
		var collision machineCredentialDocument
		collisionFound, err := transaction.Get(ctx, machineCredentialCollection, credentialID, &collision)
		if err != nil {
			return fmt.Errorf("check machine credential digest: %w", err)
		}
		if collisionFound {
			// An astronomically unlikely digest collision on a fresh random
			// token. Reported as unavailable rather than retried here: the
			// caller (or an operator re-running invite) mints a fresh token.
			return ErrUnavailable
		}
		// Every read this transaction needs happens above this line, and
		// every write happens below it. The strict Firestore profile (like
		// real Firestore) refuses a transactional read that follows a write,
		// so the stats existence check — needed only on the fresh-invite
		// path below — is read here, before the credential revoke/write
		// calls that follow, rather than being read lazily where it is used.
		var existingStats PeerStats
		var statsFound bool
		if !trustFound {
			statsFound, err = transaction.Get(ctx, peerStatsCollection, machineID, &existingStats)
			if err != nil {
				return fmt.Errorf("read current peer statistics: %w", err)
			}
		}
		if enrollmentFound && enrollment.CredentialID != "" && enrollment.CredentialID != credentialID {
			if err := transaction.Delete(ctx, machineCredentialCollection, enrollment.CredentialID); err != nil {
				return fmt.Errorf("revoke previous machine credential: %w", err)
			}
		}
		if err := transaction.Set(ctx, machineCredentialCollection, credentialID, machineCredentialDocument{Binding: binding}); err != nil {
			return fmt.Errorf("write machine credential: %w", err)
		}
		if err := transaction.Set(ctx, machineEnrollmentCollection, machineID, machineEnrollmentDocument{Binding: binding, CredentialID: credentialID}); err != nil {
			return fmt.Errorf("write machine enrollment: %w", err)
		}
		if trustFound {
			trustRecord.NodeID = ""
			trustRecord.ResetPending = true
			if err := transaction.Set(ctx, peerTrustCollection, machineID, trustRecord); err != nil {
				return fmt.Errorf("write peer trust record: %w", err)
			}
			result = PeerInviteResult{PeerID: machineID, Name: name, Token: token, CreatedAt: now, Rotated: true}
			return nil
		}
		newRecord := PeerRecord{
			MachineID: machineID, Name: name, IdentityID: identity,
			Trust: PeerTrustActive, CreatedAt: now, TrustChangedAt: now,
		}
		if err := transaction.Set(ctx, peerTrustCollection, machineID, newRecord); err != nil {
			return fmt.Errorf("write peer trust record: %w", err)
		}
		if !statsFound {
			if err := transaction.Set(ctx, peerStatsCollection, machineID, PeerStats{MachineID: machineID}); err != nil {
				return fmt.Errorf("write peer statistics: %w", err)
			}
		}
		result = PeerInviteResult{PeerID: machineID, Name: name, Token: token, CreatedAt: now}
		return nil
	})
	if err != nil {
		return PeerInviteResult{}, err
	}
	return result, nil
}

// RefuseIfPeerNameCollision reports whether name is off-limits to the owner
// RPC's plain (non-peer) "enroll" route: the hub's own machine name, or a
// name already held by a peer trust document. It does not apply to
// ensureLocalEnrollment's own self-bootstrap, which enrolls the hub's own
// machine name under the "local" identity by design and must keep doing so.
func (service *PeerAdminService) RefuseIfPeerNameCollision(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("machine name is required")
	}
	if strings.EqualFold(name, service.HubMachineName) {
		return fmt.Errorf("%q is this hub's own machine name and cannot be enrolled again", name)
	}
	if service.Trust == nil {
		return nil
	}
	if _, found, err := service.Trust.FindPeerByName(ctx, name); err != nil {
		return err
	} else if found {
		return fmt.Errorf("%q already belongs to a peer; enrolling it as a plain machine would shadow that peer", name)
	}
	return nil
}

// peerExists reports whether machineID already has a peer trust document,
// without exposing it — Invite only needs the boolean to decide whether it is
// looking at a fresh admission or a rotation.
func (service *PeerAdminService) peerExists(ctx context.Context, machineID string) (bool, error) {
	_, found, err := service.Trust.GetPeer(ctx, machineID)
	return found, err
}

// resolvePeer accepts a name or a peer (machine) ID, matching `<peer>`'s
// documented contract on every management command.
func (service *PeerAdminService) resolvePeer(ctx context.Context, nameOrID string) (PeerRecord, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return PeerRecord{}, errors.New("peer name or ID is required")
	}
	if service.Trust == nil {
		return PeerRecord{}, ErrUnavailable
	}
	if record, found, err := service.Trust.GetPeer(ctx, nameOrID); err != nil {
		return PeerRecord{}, err
	} else if found {
		return record, nil
	}
	record, found, err := service.Trust.FindPeerByName(ctx, nameOrID)
	if err != nil {
		return PeerRecord{}, err
	}
	if !found {
		return PeerRecord{}, ErrPeerNotFound
	}
	return record, nil
}

// Block closes the live session (a no-op until Task 2) and refuses future
// sessions and HTTP calls with that credential. The record, cursor and
// lifetime counters are unchanged.
func (service *PeerAdminService) Block(ctx context.Context, nameOrID string) (PeerRecord, error) {
	peer, err := service.resolvePeer(ctx, nameOrID)
	if err != nil {
		return PeerRecord{}, err
	}
	return service.Trust.UpdateTrust(ctx, peer.MachineID, func(record *PeerRecord) {
		record.Trust = PeerTrustBlocked
		record.TrustChangedAt = service.now()
	})
}

// Unblock allows sessions again and sets reset_pending, so the peer's next
// redial reconciles from the hub's heads instead of resuming a cursor that
// may have gone stale while it was refused.
func (service *PeerAdminService) Unblock(ctx context.Context, nameOrID string) (PeerRecord, error) {
	peer, err := service.resolvePeer(ctx, nameOrID)
	if err != nil {
		return PeerRecord{}, err
	}
	return service.Trust.UpdateTrust(ctx, peer.MachineID, func(record *PeerRecord) {
		record.Trust = PeerTrustActive
		record.TrustChangedAt = service.now()
		record.ResetPending = true
	})
}

// PeerDisconnectResult is what `wb peers disconnect` reports. Until Task 2
// adds the live-session registry, there is never a live session to close, so
// Disconnected is always false and Message says so plainly.
type PeerDisconnectResult struct {
	Peer         PeerRecord
	Disconnected bool
	Message      string
}

// Disconnect closes the live session only; the peer stays trusted and
// reconnects by itself. Task 2 adds the session registry this needs; until
// then it answers "no live session" for any known peer, and an error for an
// unknown one.
func (service *PeerAdminService) Disconnect(ctx context.Context, nameOrID string) (PeerDisconnectResult, error) {
	peer, err := service.resolvePeer(ctx, nameOrID)
	if err != nil {
		return PeerDisconnectResult{}, err
	}
	return PeerDisconnectResult{Peer: peer, Disconnected: false, Message: "no live session"}, nil
}
