package sessiontransport

import (
	"context"
	"errors"
	"testing"
)

func TestNoneTransportKindAndCapabilities(t *testing.T) {
	t.Parallel()
	transport := NoneTransport{}
	if got := transport.Kind(); got != KindNone {
		t.Errorf("Kind() = %q, want %q", got, KindNone)
	}
	if got := transport.Capabilities(); got != NoneCapabilities() {
		t.Errorf("Capabilities() = %#v, want %#v", got, NoneCapabilities())
	}
}

func TestNoneTransportResolvePaneNeverFabricatesATarget(t *testing.T) {
	t.Parallel()
	target, err := (NoneTransport{}).ResolvePane(context.Background(), Identity{Kind: KindNone, HarnessSessionID: "anything", WBSessionID: "wbs-anything"})
	if !errors.Is(err, ErrNoUniqueTarget) {
		t.Fatalf("ResolvePane error = %v, want ErrNoUniqueTarget", err)
	}
	if !target.IsZero() {
		t.Fatalf("ResolvePane target = %#v, want zero value", target)
	}
}

func TestNoneTransportLaunchNeverErrors(t *testing.T) {
	t.Parallel()
	target, err := (NoneTransport{}).Launch(context.Background(), LaunchRequest{
		SuccessorWBSessionID: "wbs-successor", Cwd: "/tmp", Executable: "/bin/true",
	})
	if err != nil {
		t.Fatalf("Launch() error = %v, want nil (REQ:none-transport-is-first-class)", err)
	}
	if !target.IsZero() {
		t.Fatalf("Launch() target = %#v, want zero value: none starts no live terminal", target)
	}
}

func TestNoneTransportInspectNeverErrors(t *testing.T) {
	t.Parallel()
	inspection, err := (NoneTransport{}).Inspect(context.Background(), Target{Kind: KindNone})
	if err != nil {
		t.Fatalf("Inspect() error = %v, want nil", err)
	}
	if inspection != (Inspection{}) {
		t.Fatalf("Inspect() = %#v, want the zero value: none never started anything, so nothing is live and nothing left terminal evidence", inspection)
	}
}

func TestNoneTransportAddressForReturnsEmpty(t *testing.T) {
	t.Parallel()
	if got := (NoneTransport{}).AddressFor("wbs-anything"); got != "" {
		t.Fatalf("AddressFor() = %q, want empty: none has no address derivable from a WB session ID", got)
	}
}

func TestMatchesReceiptIdentityAgainstNoneIsNeverTrueForANonEmptyName(t *testing.T) {
	t.Parallel()
	// MatchesReceiptIdentity is now name == AddressFor(id); none's
	// AddressFor is always "", so only an equally-empty name ever matches -
	// there is no name none's convention can ever legitimately produce.
	if MatchesReceiptIdentity(NoneTransport{}, "wbs-anything", "anything-at-all") {
		t.Fatal("MatchesReceiptIdentity(none, id, non-empty name) = true, want false")
	}
	if !MatchesReceiptIdentity(NoneTransport{}, "wbs-anything", "") {
		t.Fatal("MatchesReceiptIdentity(none, id, \"\") = false, want true: AddressFor(id) is also \"\"")
	}
}

// TestNoneTransportDeliverRecordsRatherThanErrors proves
// AC:none-transport-records-only end to end against the concrete
// implementation: whatever class is requested, delivery is recorded and the
// calling command never fails.
func TestNoneTransportDeliverRecordsRatherThanErrors(t *testing.T) {
	t.Parallel()
	for _, class := range []EventClass{EventOwnPROutcome, EventAdvisory, EventSuccessorMessage} {
		receipt, err := (NoneTransport{}).Deliver(context.Background(), Target{}, Delivery{
			Operation: OperationWake,
			Class:     class,
			Text:      "sneat-dev/wb#647: checks passed",
		})
		if err != nil {
			t.Fatalf("Deliver(class=%s) error = %v, want nil", class, err)
		}
		if receipt.Outcome != ModeRecordOnly {
			t.Fatalf("Deliver(class=%s).Outcome = %q, want %q: none must always record, never claim to advise or submit", class, receipt.Outcome, ModeRecordOnly)
		}
		if receipt.Operation != OperationWake {
			t.Fatalf("Deliver(class=%s).Operation = %q, want %q", class, receipt.Operation, OperationWake)
		}
		if receipt.At.IsZero() {
			t.Fatalf("Deliver(class=%s).At is zero, want a recorded timestamp", class)
		}
	}
}
