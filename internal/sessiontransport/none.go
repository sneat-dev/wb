package sessiontransport

import (
	"context"
	"time"
)

// NoneTransport is the first-class no-op transport
// REQ:none-transport-is-first-class requires: every method returns a plain,
// honest, non-error outcome, because there never was anywhere live to
// deliver to. It is the transport a session resolves to when neither herdr
// nor tmux identity is observable (REQ:identity-capture-outside-herdr,
// Task 5's to wire onto this type). The zero value is ready to use.
type NoneTransport struct{}

// Kind implements [Transport], returning [KindNone].
func (NoneTransport) Kind() Kind { return KindNone }

// Capabilities implements [Transport], returning [NoneCapabilities].
func (NoneTransport) Capabilities() Capabilities { return NoneCapabilities() }

// ResolvePane implements [Transport]. none never has a live pane, so it
// reports [ErrNoUniqueTarget] rather than fabricating one; every caller
// already treats that as record-only (REQ:no-live-owner-is-record-only).
func (NoneTransport) ResolvePane(context.Context, Identity) (Target, error) {
	return Target{}, ErrNoUniqueTarget
}

// Launch implements [Transport]. none has no terminal to start, so it
// returns a zero [Target] with no error: launching a successor onto none is
// a supported, expected outcome, not a failure.
func (NoneTransport) Launch(context.Context, LaunchRequest) (Target, error) {
	return Target{}, nil
}

// Inspect implements [Transport]. none never started anything, so nothing
// is ever live and nothing ever left terminal evidence behind either.
func (NoneTransport) Inspect(context.Context, Target) (Inspection, error) {
	return Inspection{}, nil
}

// MatchesReceiptIdentity implements [Transport]. none has no naming
// convention to enforce, so every name trivially matches: there is nothing
// to refute.
func (NoneTransport) MatchesReceiptIdentity(string, string) bool {
	return true
}

// Deliver implements [Transport]. [NoneCapabilities] declares every
// capability false, so [ResolveDeliveryMode] always resolves
// [ModeRecordOnly] for it regardless of delivery.Class — every requested
// delivery, including a caller mistake that assumes a submit or advisory
// path exists, resolves to a recorded outcome and no error
// (REQ:none-transport-is-first-class, AC:none-transport-records-only).
func (NoneTransport) Deliver(_ context.Context, target Target, delivery Delivery) (Receipt, error) {
	mode := ResolveDeliveryMode(NoneCapabilities(), delivery.Class)
	return Receipt{
		Operation: delivery.Operation,
		Outcome:   mode,
		Target:    target,
		At:        time.Now().UTC(),
	}, nil
}
