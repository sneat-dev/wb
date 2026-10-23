package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

const (
	MachineEnrollmentPath = APIPrefix + "/machines/enroll"
	machineTokenBytes     = 32
	minimumPepperBytes    = 32
)

type MachineEnrollmentRequest struct {
	Name string `json:"name"`
}

type MachineEnrollmentResponse struct {
	Machine    EnrolledMachine  `json:"machine"`
	Identity   EnrolledIdentity `json:"identity"`
	Token      string           `json:"token"`
	EnrolledAt time.Time        `json:"enrolled_at"`
}

type EnrolledMachine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type EnrolledIdentity struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

type MachineEnrollmentService struct {
	Store  MachineCredentialStore
	Pepper []byte
	Random io.Reader
	Now    func() time.Time
}

func (service MachineEnrollmentService) Enroll(ctx context.Context, viewer Viewer, request MachineEnrollmentRequest) (MachineEnrollmentResponse, error) {
	if !viewer.Authenticated || strings.TrimSpace(viewer.IdentityID) == "" {
		return MachineEnrollmentResponse{}, ErrUnauthorized
	}
	if machinesnapshot.ValidateIdentity(request.Name) != nil {
		return MachineEnrollmentResponse{}, errors.New("machine enrollment name is invalid")
	}
	if service.Store == nil || len(service.Pepper) < minimumPepperBytes {
		return MachineEnrollmentResponse{}, ErrUnavailable
	}
	random := service.Random
	if random == nil {
		random = rand.Reader
	}
	raw := make([]byte, machineTokenBytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return MachineEnrollmentResponse{}, ErrUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := digestMachineToken(token, service.Pepper)
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	binding := MachineCredentialBinding{
		IdentityID: viewer.IdentityID, IdentityDisplayName: viewer.DisplayName,
		MachineName: request.Name, IssuedAt: now, Scopes: cloneEnrollmentScopes(),
	}
	stored, err := service.Store.RotateMachineCredential(ctx, binding, digest)
	if err != nil || !stored.valid() || stored.IdentityID != binding.IdentityID || stored.MachineName != binding.MachineName || !stored.IssuedAt.Equal(binding.IssuedAt) {
		return MachineEnrollmentResponse{}, ErrUnavailable
	}
	return MachineEnrollmentResponse{
		Machine:  EnrolledMachine{ID: stored.MachineID, Name: stored.MachineName},
		Identity: EnrolledIdentity{ID: stored.IdentityID, DisplayName: stored.IdentityDisplayName},
		Token:    token, EnrolledAt: stored.IssuedAt,
	}, nil
}

func DigestMachineToken(token string, pepper []byte) (MachineTokenDigest, error) {
	if len(pepper) < minimumPepperBytes || token == "" || len(token) > 1024 || strings.ContainsAny(token, " \t\r\n") {
		return MachineTokenDigest{}, ErrUnauthorized
	}
	return digestMachineToken(token, pepper), nil
}

func digestMachineToken(token string, pepper []byte) MachineTokenDigest {
	digest := sha256.Sum256(append(append(make([]byte, 0, len(pepper)+1+len(token)), pepper...), append([]byte{0}, token...)...))
	return MachineTokenDigest(digest)
}

type machineBearerResolver struct {
	credentials MachineCredentialResolver
	pepper      []byte
}

func NewMachineBearerResolver(credentials MachineCredentialResolver, pepper []byte) MachineBearerResolver {
	return machineBearerResolver{credentials: credentials, pepper: append([]byte(nil), pepper...)}
}

func (resolver machineBearerResolver) ResolveMachineBearer(request *http.Request) (Machine, error) {
	if request == nil || resolver.credentials == nil {
		return Machine{}, ErrUnauthorized
	}
	values := request.Header.Values("Authorization")
	if len(values) != 1 {
		return Machine{}, ErrUnauthorized
	}
	scheme, token, found := strings.Cut(strings.TrimSpace(values[0]), " ")
	token = strings.TrimSpace(token)
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || len(token) > 1024 || strings.ContainsAny(token, " \t\r\n") {
		return Machine{}, ErrUnauthorized
	}
	digest, err := DigestMachineToken(token, resolver.pepper)
	if err != nil {
		return Machine{}, ErrUnauthorized
	}
	binding, err := resolver.credentials.ResolveMachineCredential(request.Context(), digest)
	if err != nil || !binding.valid() {
		return Machine{}, ErrUnauthorized
	}
	return Machine{ID: binding.MachineID, Name: binding.MachineName, IdentityID: binding.IdentityID, Scopes: append([]MachineScope(nil), binding.Scopes...)}, nil
}

// PeerTrustResolver is the read the peer-aware bearer resolver needs: does a
// peer record exist for this MachineID, and is it blocked. It is the narrow
// slice of PeerTrustStore that authentication depends on.
type PeerTrustResolver interface {
	GetPeer(context.Context, string) (record PeerRecord, found bool, err error)
}

// peerAwareBearerResolver wraps an existing MachineBearerResolver with the
// one extra read peer-connectivity#req:peer-is-persistent-state requires:
// "After resolving a credential, the machine bearer resolver reads the peer
// record by MachineID and refuses a blocked peer." Applied uniformly to
// every authenticated call — the HTTP long poll and the snapshot routes
// included — because the wrapped resolver is the one every route already
// calls through h.machine(r). A credential with no peer record (an ordinary
// enrollment) is unaffected: found is false and the wrapped result passes
// straight through.
type peerAwareBearerResolver struct {
	inner MachineBearerResolver
	trust PeerTrustResolver
}

// NewPeerAwareBearerResolver returns a MachineBearerResolver that refuses a
// blocked peer's credential after delegating ordinary resolution to inner.
// A nil trust resolver makes this a pass-through, so a caller that has not
// wired peer storage yet (or a hosted deployment that never will) keeps
// exactly today's behaviour.
func NewPeerAwareBearerResolver(inner MachineBearerResolver, trust PeerTrustResolver) MachineBearerResolver {
	return peerAwareBearerResolver{inner: inner, trust: trust}
}

func (resolver peerAwareBearerResolver) ResolveMachineBearer(request *http.Request) (Machine, error) {
	if resolver.inner == nil {
		return Machine{}, ErrUnauthorized
	}
	machine, err := resolver.inner.ResolveMachineBearer(request)
	if err != nil {
		return Machine{}, err
	}
	if resolver.trust == nil {
		return machine, nil
	}
	record, found, err := resolver.trust.GetPeer(request.Context(), machine.ID)
	if err != nil {
		return Machine{}, ErrUnauthorized
	}
	if !found {
		if isPeerScopes(machine.Scopes) {
			// A peer-scoped credential with no trust document is an
			// impossible, fail-closed state under a correctly operating
			// Invite (which writes both inside one transaction — see
			// PeerAdminService.Backend). Refuse rather than pass it through
			// as an ordinary, untracked credential.
			return Machine{}, ErrUnauthorized
		}
		return machine, nil
	}
	if record.Trust == PeerTrustBlocked {
		return Machine{}, ErrUnauthorized
	}
	return machine, nil
}

var _ MachineBearerResolver = peerAwareBearerResolver{}
