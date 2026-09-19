package sessiontransport

import (
	"context"
	"errors"
	"testing"
)

// Suite is the shared contract test suite REQ:two-transport-implementations
// requires: Task 3's tmux transport and Task 4's herdr transport must both
// pass it, and neither needs a distinct suite of its own to prove it
// satisfies [Transport] (AC:two-implementations-satisfy-the-same-interface).
// factory returns a fresh [Transport] for each check so a stateful
// implementation is never accidentally shared across checks that assume
// independence.
//
// Suite only proves what every implementation — herdr, tmux, or none — must
// hold regardless of its own declared [Capabilities]. It never reaches a
// real herdr socket or tmux server; callers supply factory backed by fakes,
// following this same convention: no test call reaches a live transport.
//
// This mirrors internal/testenv's convention of a reusable *testing.T
// helper exported from an ordinary (non-_test.go) file, so other packages'
// test files can call it directly.
func Suite(t *testing.T, factory func() Transport) {
	t.Helper()

	t.Run("KindMatchesCapabilities", func(t *testing.T) {
		t.Parallel()
		transport := factory()
		if got, want := transport.Capabilities().Kind, transport.Kind(); got != want {
			t.Fatalf("Capabilities().Kind = %q, want %q (Kind())", got, want)
		}
	})

	t.Run("RecordOnlyNeverErrorsForAnyOperation", func(t *testing.T) {
		t.Parallel()
		transport := factory()
		ctx := context.Background()
		for _, op := range []Operation{OperationMessage, OperationMove, OperationPark, OperationReceive, OperationWake} {
			receipt, err := transport.Deliver(ctx, Target{}, Delivery{Operation: op, Mode: ModeRecordOnly, Text: "contract-suite-probe"})
			if err != nil {
				t.Fatalf("Deliver(%s, record-only) returned an error, want none: %v", op, err)
			}
			if receipt.Outcome != ModeRecordOnly {
				t.Fatalf("Deliver(%s, record-only).Outcome = %q, want %q", op, receipt.Outcome, ModeRecordOnly)
			}
			if receipt.Operation != op {
				t.Fatalf("Deliver(%s, record-only).Operation = %q, want %q", op, receipt.Operation, op)
			}
		}
	})

	t.Run("AdvisoryNeverExceedsDeclaredCapability", func(t *testing.T) {
		t.Parallel()
		transport := factory()
		caps := transport.Capabilities()
		receipt, err := transport.Deliver(context.Background(), Target{}, Delivery{Operation: OperationWake, Mode: ModeAdvisory, Text: "contract-suite-probe"})
		if err != nil {
			t.Fatalf("Deliver(wake, advisory) returned an error, want none: %v", err)
		}
		if receipt.Outcome == ModeSubmit {
			t.Fatalf("Deliver(wake, advisory) must never report Outcome = submit")
		}
		if !caps.AdvisoryDelivery && receipt.Outcome != ModeRecordOnly {
			t.Fatalf("a transport that does not declare AdvisoryDelivery must record rather than advise: Outcome = %q", receipt.Outcome)
		}
	})

	t.Run("SubmitNeverExceedsDeclaredCapability", func(t *testing.T) {
		t.Parallel()
		transport := factory()
		caps := transport.Capabilities()
		receipt, err := transport.Deliver(context.Background(), Target{}, Delivery{Operation: OperationWake, Mode: ModeSubmit, Text: "contract-suite-probe"})
		if err != nil {
			t.Fatalf("Deliver(wake, submit) returned an error, want none: %v", err)
		}
		if receipt.Outcome == ModeSubmit && (!caps.LiveStatus || !caps.EmptyInputEvidence) {
			t.Fatalf("a transport without both LiveStatus and EmptyInputEvidence must never report Outcome = submit")
		}
	})

	t.Run("ResolvePaneNeverGuessesWithNoIdentity", func(t *testing.T) {
		t.Parallel()
		transport := factory()
		target, err := transport.ResolvePane(context.Background(), Identity{Kind: transport.Kind()})
		if err == nil && target.IsZero() {
			t.Fatalf("ResolvePane with no identity coordinates resolved to a zero Target with no error; want ErrNoUniqueTarget or a genuinely resolved Target")
		}
		if err != nil && !errors.Is(err, ErrNoUniqueTarget) {
			t.Fatalf("ResolvePane error = %v, want ErrNoUniqueTarget or nil", err)
		}
	})
}
