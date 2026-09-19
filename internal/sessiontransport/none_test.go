package sessiontransport

import (
	"context"
	"errors"
	"testing"
)

func TestNoneTransportSatisfiesContractSuite(t *testing.T) {
	Suite(t, func() Transport { return NoneTransport{} })
}

func TestNoneTransportKindAndCapabilities(t *testing.T) {
	t.Parallel()
	transport := NoneTransport{}
	if got := transport.Kind(); got != KindNone {
		t.Errorf("Kind() = %q, want %q", got, KindNone)
	}
	if got := transport.Capabilities(); got != NoneCapabilities {
		t.Errorf("Capabilities() = %#v, want %#v", got, NoneCapabilities)
	}
}

func TestNoneTransportResolvePaneNeverFabricatesATarget(t *testing.T) {
	t.Parallel()
	target, err := (NoneTransport{}).ResolvePane(context.Background(), Identity{Kind: KindNone, HarnessSessionID: "wbs-anything"})
	if !errors.Is(err, ErrNoUniqueTarget) {
		t.Fatalf("ResolvePane error = %v, want ErrNoUniqueTarget", err)
	}
	if !target.IsZero() {
		t.Fatalf("ResolvePane target = %#v, want zero value", target)
	}
}

func TestNoneTransportLaunchNeverErrors(t *testing.T) {
	t.Parallel()
	target, err := (NoneTransport{}).Launch(context.Background(), LaunchRequest{SuccessorWBSessionID: "wbs-successor"})
	if err != nil {
		t.Fatalf("Launch() error = %v, want nil (REQ:none-transport-is-first-class)", err)
	}
	if !target.IsZero() {
		t.Fatalf("Launch() target = %#v, want zero value: none starts no live terminal", target)
	}
}

// TestNoneTransportDeliverRecordsRatherThanErrors proves
// AC:none-transport-records-only end to end against the concrete
// implementation: whatever mode is requested, delivery is recorded and the
// calling command never fails.
func TestNoneTransportDeliverRecordsRatherThanErrors(t *testing.T) {
	t.Parallel()
	for _, mode := range []DeliveryMode{ModeRecordOnly, ModeAdvisory, ModeSubmit} {
		receipt, err := (NoneTransport{}).Deliver(context.Background(), Target{}, Delivery{
			Operation: OperationWake,
			Mode:      mode,
			Text:      "sneat-dev/wb#647: checks passed",
		})
		if err != nil {
			t.Fatalf("Deliver(mode=%s) error = %v, want nil", mode, err)
		}
		if receipt.Outcome != ModeRecordOnly {
			t.Fatalf("Deliver(mode=%s).Outcome = %q, want %q: none must always record, never claim to advise or submit", mode, receipt.Outcome, ModeRecordOnly)
		}
		if receipt.Operation != OperationWake {
			t.Fatalf("Deliver(mode=%s).Operation = %q, want %q", mode, receipt.Operation, OperationWake)
		}
		if receipt.At.IsZero() {
			t.Fatalf("Deliver(mode=%s).At is zero, want a recorded timestamp", mode)
		}
	}
}
