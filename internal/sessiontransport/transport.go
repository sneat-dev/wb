package sessiontransport

import (
	"context"
	"errors"
)

// Target is an exact live endpoint. Herdr pane IDs are scoped to Socket.
// Name remains available for generic contract compatibility, not active tmux execution.
type Target struct {
	Kind   Kind
	ID     string
	Name   string
	PID    int
	Socket string
}

// IsZero reports that no live endpoint was resolved.
func (t Target) IsZero() bool {
	return t == Target{}
}

// Identity binds a logical WB session and native harness to its recorded server.
// Resolution never guesses among zero or multiple candidates.
type Identity struct {
	Kind             Kind
	WBSessionID      string
	HarnessSessionID string
	Socket           string
}

// LaunchRequest describes a fixed executable and ordered argv, never shell source.
// Production Herdr launcher custody is enforced by its bounded typed client.
type LaunchRequest struct {
	SuccessorWBSessionID string
	Cwd                  string
	Executable           string
	Args                 []string
}

// Inspection separates live process evidence from retained terminal failure evidence.
type Inspection struct {
	Live       bool
	PID        int
	Terminated bool
	ExitStatus int
	Diagnostic string
}

// ErrNoUniqueTarget permits record-only daemon policy, never fabricated delivery.
// Receipt-bound successor messages instead refuse missing exact identity.
var ErrNoUniqueTarget = errors.New("sessiontransport: no unique live target for this identity")

// Transport is the generic capability-policy contract, with NoneTransport as its
// record-only implementation. Production launcher/message Herdr clients retain
// their own typed custody boundaries; they do not all implement this interface.
type Transport interface {
	Kind() Kind
	Capabilities() Capabilities
	// ResolvePane returns exactly one corroborated live endpoint or ErrNoUniqueTarget.
	ResolvePane(ctx context.Context, identity Identity) (Target, error)
	// Launch must derive its owned endpoint rather than trusting an arbitrary terminal name.
	Launch(ctx context.Context, request LaunchRequest) (Target, error)
	Inspect(ctx context.Context, target Target) (Inspection, error)
	// AddressFor is empty when no address can be derived solely from a logical WB ID.
	AddressFor(wbSessionID string) string
	// Deliver recomputes ResolveDeliveryMode from capabilities and operation/class.
	// Record-only never touches a pane; advisory never submits. LineageSubmit is
	// restricted to explicit successor messages, never a daemon wake permission.
	// The returned mode cannot exceed that authorized decision; no target means
	// record-only. A receipt never claims the agent processed a submitted message.
	Deliver(ctx context.Context, target Target, delivery Delivery) (Receipt, error)
}

// MatchesReceiptIdentity checks the contract address for the exact logical successor.
func MatchesReceiptIdentity(transport Transport, successorWBSessionID, name string) bool {
	return name == transport.AddressFor(successorWBSessionID)
}
