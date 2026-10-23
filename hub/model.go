package hub

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

const APIPrefix = "/v0/workbench"

var (
	ErrUnauthorized             = errors.New("workbench github app authentication failed")
	ErrUnavailable              = errors.New("workbench github app service is unavailable")
	ErrInvalidInstallationState = errors.New("workbench github app installation state is invalid")
)

type Viewer struct {
	Authenticated bool
	IdentityID    string
	DisplayName   string
}

type ViewerResolver interface {
	Viewer(*http.Request) (Viewer, error)
}

type MachineScope string

const (
	ScopeSnapshotPublish MachineScope = "machine_snapshot:publish"
	ScopeSnapshotRead    MachineScope = "machine_snapshot:read"
	ScopeEventsPoll      MachineScope = "repository_events:poll"
	ScopeEventsAck       MachineScope = "repository_events:ack"
	// ScopePeerSession is the sole scope a peer credential carries
	// (peer-connectivity#req:invite-and-join). A peer token is deliberately
	// refused by every route gated on the enrollment scopes above, so the
	// session it opens can never become a second consumer of its own queue.
	ScopePeerSession MachineScope = "peer:session"
)

var enrollmentScopes = []MachineScope{ScopeSnapshotPublish, ScopeSnapshotRead, ScopeEventsPoll, ScopeEventsAck}

// peerScopes is the second valid scope set validScopes accepts: exactly
// ScopePeerSession, nothing more and nothing less.
var peerScopes = []MachineScope{ScopePeerSession}

func cloneEnrollmentScopes() []MachineScope { return append([]MachineScope(nil), enrollmentScopes...) }

// clonePeerScopes returns the one-scope set a peer credential is minted with.
func clonePeerScopes() []MachineScope { return append([]MachineScope(nil), peerScopes...) }

func hasScope(scopes []MachineScope, required MachineScope) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}

// scopeSetsEqual reports whether scopes holds exactly the scopes in want,
// with no extras and nothing missing, order-independent.
func scopeSetsEqual(scopes, want []MachineScope) bool {
	if len(scopes) != len(want) {
		return false
	}
	for _, scope := range want {
		if !hasScope(scopes, scope) {
			return false
		}
	}
	return true
}

// validScopes accepts exactly one of two sets: the existing full enrollment
// set, or the peer set (peer:session alone). Anything else — a subset, a
// superset, or a mix — is rejected, so a credential's scopes are always
// unambiguously "an enrollment" or "a peer session".
func validScopes(scopes []MachineScope) bool {
	return scopeSetsEqual(scopes, enrollmentScopes) || scopeSetsEqual(scopes, peerScopes)
}

// isPeerScopes reports whether scopes is exactly the peer set, which is how
// callers tell a peer credential from an enrollment credential once a
// binding has already passed validScopes.
func isPeerScopes(scopes []MachineScope) bool {
	return scopeSetsEqual(scopes, peerScopes)
}

type MachineCredentialBinding struct {
	MachineID           string         `firestore:"machine_id"`
	MachineName         string         `firestore:"machine_name"`
	IdentityID          string         `firestore:"identity_id"`
	IdentityDisplayName string         `firestore:"identity_display_name,omitempty"`
	IssuedAt            time.Time      `firestore:"issued_at"`
	Scopes              []MachineScope `firestore:"scopes"`
}

func (binding MachineCredentialBinding) valid() bool {
	return strings.TrimSpace(binding.MachineID) != "" && strings.TrimSpace(binding.MachineName) != "" &&
		strings.TrimSpace(binding.IdentityID) != "" && !binding.IssuedAt.IsZero() && validScopes(binding.Scopes)
}

type MachineTokenDigest [sha256.Size]byte

type MachineCredentialStore interface {
	RotateMachineCredential(context.Context, MachineCredentialBinding, MachineTokenDigest) (MachineCredentialBinding, error)
}

type MachineCredentialResolver interface {
	ResolveMachineCredential(context.Context, MachineTokenDigest) (MachineCredentialBinding, error)
}

type Machine struct {
	ID         string
	Name       string
	IdentityID string
	Scopes     []MachineScope
}

func (machine Machine) valid() bool {
	return strings.TrimSpace(machine.ID) != "" && strings.TrimSpace(machine.Name) != "" && strings.TrimSpace(machine.IdentityID) != ""
}

type MachineBearerResolver interface {
	ResolveMachineBearer(*http.Request) (Machine, error)
}

// MachineIndex answers whether any credential — peer or not — already exists
// for a MachineID, without exposing the credential body. Invite uses it to
// refuse a name already held by a non-peer credential
// (peer-connectivity#req:invite-and-join): the peer trust store alone cannot
// tell "no peer" from "an enrolled machine that keeps its name".
type MachineIndex interface {
	HasCredential(context.Context, string) (bool, error)
}

type MachineSnapshotStore interface {
	StoreLatest(context.Context, StoredMachineSnapshot) (MachineSnapshotStoreResult, error)
	ListLatest(context.Context) ([]StoredMachineSnapshot, error)
}

type StoredMachineSnapshot struct {
	IdentityID string                   `firestore:"identity_id"`
	MachineID  string                   `firestore:"machine_id"`
	Snapshot   machinesnapshot.Snapshot `firestore:"snapshot"`
	ReceivedAt time.Time                `firestore:"received_at"`
	Digest     string                   `firestore:"digest"`
}

type MachineSnapshotStoreResult struct {
	Current StoredMachineSnapshot
	Updated bool
}

type RepositoryEventStore interface {
	EnqueueForMachines(context.Context, repositoryevent.Event, []Machine) (EnqueueResult, error)
	Poll(context.Context, Machine, string, int) (repositoryevent.PollResponse, error)
	Acknowledge(context.Context, Machine, repositoryevent.AckRequest) (repositoryevent.AckResponse, error)
}

type EnqueueResult struct {
	Enqueued  int  `json:"enqueued"`
	Duplicate bool `json:"duplicate"`
}

type RepositoryEntitlementResolver interface {
	IdentityHasRepositoryEntitlement(context.Context, string, int64, int64) (bool, error)
}

type InstallationLifecycleAction string

const (
	InstallationSuspended           InstallationLifecycleAction = "suspended"
	InstallationRevoked             InstallationLifecycleAction = "revoked"
	InstallationRepositoriesRemoved InstallationLifecycleAction = "repositories_removed"
	InstallationUserAccessRemoved   InstallationLifecycleAction = "user_access_removed"
	GitHubUserAuthorizationRevoked  InstallationLifecycleAction = "github_user_authorization_revoked"
	RepositoryUserAccessRemoved     InstallationLifecycleAction = "repository_user_access_removed"
)

type InstallationLifecycleEvent struct {
	DeliveryID     string                      `firestore:"delivery_id"`
	Action         InstallationLifecycleAction `firestore:"action"`
	InstallationID int64                       `firestore:"installation_id"`
	RepositoryIDs  []int64                     `firestore:"repository_ids,omitempty"`
	RepositoryID   int64                       `firestore:"repository_id,omitempty"`
	GitHubUserID   int64                       `firestore:"github_user_id,omitempty"`
}

type InstallationLifecycleStore interface {
	// ApplyInstallationLifecycle is idempotent by DeliveryID and atomically
	// removes or disables every affected entitlement before it returns success.
	// A GitHubUserAuthorizationRevoked event applies to every binding for that
	// GitHub user; RepositoryUserAccessRemoved applies only to its exact
	// installation, repository, and GitHub user tuple.
	ApplyInstallationLifecycle(context.Context, InstallationLifecycleEvent) error
}

type WebhookDelivery struct {
	ID         string
	Event      string
	Repository string
	Payload    []byte
}

// ProjectionProcessor lets an existing dashboard projection subsystem consume
// the same already authenticated delivery while that subsystem is migrated.
type ProjectionProcessor interface {
	ProcessProjection(context.Context, WebhookDelivery, string) error
}
