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
)

// PeerAdminService implements every admission and trust change
// peer-connectivity#req:admin-requires-owner-credential requires: invite,
// rotate, block, unblock and disconnect. It is deliberately not reachable
// from any HTTP route; cmd/wb mounts it only on the daemon's owner-token
// unix-socket RPC.
type PeerAdminService struct {
	Credentials MachineCredentialStore
	Index       MachineIndex
	Trust       PeerTrustStore
	Stats       PeerStatsStore
	Pepper      []byte
	Random      io.Reader
	Now         func() time.Time
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
// credential, or an existing peer unless rotate is true.
func (service *PeerAdminService) Invite(ctx context.Context, name string, rotate bool) (PeerInviteResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return PeerInviteResult{}, errors.New("peer name is required")
	}
	if service.MemoryEngine {
		return PeerInviteResult{}, errors.New("wb peers invite is refused while the hub's store engine is memory")
	}
	if service.Credentials == nil || service.Index == nil || service.Trust == nil || service.Stats == nil || len(service.Pepper) < minimumPepperBytes {
		return PeerInviteResult{}, ErrUnavailable
	}
	identity := service.LocalIdentityID
	if identity == "" {
		// "local" matches the one identity a self-hosted daemon knows
		// (cmd/wb's localIdentityID, and MachineEnrollmentService's own
		// self-enrollment path).
		identity = "local"
	}
	if strings.EqualFold(name, service.HubMachineName) {
		return PeerInviteResult{}, fmt.Errorf("%q is this hub's own machine name and can never be a peer", name)
	}
	machineID := MachineID(identity, name)
	foundPeer, err := service.peerExists(ctx, machineID)
	if err != nil {
		return PeerInviteResult{}, err
	}
	if foundPeer && !rotate {
		return PeerInviteResult{}, fmt.Errorf("%q is already a peer; pass --rotate to reissue its credential", name)
	}
	if !foundPeer {
		hasCredential, err := service.Index.HasCredential(ctx, machineID)
		if err != nil {
			return PeerInviteResult{}, err
		}
		if hasCredential {
			return PeerInviteResult{}, fmt.Errorf("%q is already held by a non-peer machine credential", name)
		}
	}
	raw := make([]byte, machineTokenBytes)
	if _, err := io.ReadFull(service.random(), raw); err != nil {
		return PeerInviteResult{}, ErrUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := digestMachineToken(token, service.Pepper)
	now := service.now()
	binding := MachineCredentialBinding{
		IdentityID: identity, MachineName: name, IssuedAt: now, Scopes: clonePeerScopes(),
	}
	stored, err := service.Credentials.RotateMachineCredential(ctx, binding, digest)
	if err != nil || !stored.valid() || stored.MachineID != machineID {
		return PeerInviteResult{}, ErrUnavailable
	}
	if foundPeer {
		if _, err := service.Trust.UpdateTrust(ctx, machineID, func(record *PeerRecord) {
			record.NodeID = ""
			record.ResetPending = true
		}); err != nil {
			return PeerInviteResult{}, err
		}
		return PeerInviteResult{PeerID: machineID, Name: name, Token: token, CreatedAt: now, Rotated: true}, nil
	}
	if err := service.Trust.CreatePeer(ctx, PeerRecord{
		MachineID: machineID, Name: name, IdentityID: identity,
		Trust: PeerTrustActive, CreatedAt: now, TrustChangedAt: now,
	}); err != nil {
		return PeerInviteResult{}, err
	}
	if err := service.Stats.CreateStats(ctx, machineID); err != nil {
		return PeerInviteResult{}, err
	}
	return PeerInviteResult{PeerID: machineID, Name: name, Token: token, CreatedAt: now}, nil
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
