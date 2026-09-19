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
func (NoneTransport) Capabilities() Capabilities { return NoneCapabilities }

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

// Deliver implements [Transport]. Every requested [DeliveryMode] —
// including a caller mistake that asks for [ModeAdvisory] or [ModeSubmit]
// against a transport that declares neither capability — resolves to
// [ModeRecordOnly] and no error: REQ:none-transport-is-first-class's rule,
// generalized by [Transport.Deliver]'s own contract
// (AC:none-transport-records-only).
func (NoneTransport) Deliver(_ context.Context, target Target, delivery Delivery) (Receipt, error) {
	return Receipt{
		Operation: delivery.Operation,
		Outcome:   ModeRecordOnly,
		Target:    target,
		At:        time.Now().UTC(),
	}, nil
}
