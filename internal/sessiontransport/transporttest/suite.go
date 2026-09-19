// Package transporttest is the shared contract test suite
// REQ:two-transport-implementations requires: Task 3's tmux transport and
// Task 4's herdr transport must both pass it, and neither needs a distinct
// suite of its own to prove it satisfies sessiontransport.Transport
// (AC:two-implementations-satisfy-the-same-interface).
//
// It lives in its own package, sibling to internal/sessiontransport, so
// that package's own build never carries "testing" as a dependency —
// unlike internal/testenv's Isolate helper, which is small enough to live
// directly in the package it instruments, this suite is large enough, and
// specific enough to *testing other implementations* rather than to
// exercising its own package's logic, that a separate package is the
// better fit. Every check is a plain function so it can be run directly,
// against a deliberately broken fake, without going through [Suite] or a
// *testing.T at all — see suite_test.go for exactly that.
package transporttest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessiontransport"
)

// Check is one probe against a sessiontransport.Transport implementation.
// It returns a descriptive error on failure and nil on success, so it can
// be tested directly (see suite_test.go) without a *testing.T at all.
type Check func(context.Context, sessiontransport.Transport) error

// checkEntry names one Check for reporting.
type checkEntry struct {
	Name  string
	Check Check
}

// Checks is the fixed table [Suite] runs, exported so a caller can also run
// one check directly in isolation.
var Checks = []checkEntry{
	{"KindMatchesCapabilities", CheckKindMatchesCapabilities},
	{"DeliverNeverErrors", CheckDeliverNeverErrors},
	{"AdvisoryNeverExceedsDeclaredCapability", CheckAdvisoryNeverExceedsCapability},
	{"SuccessorMessageNeverExceedsDeclaredCapability", CheckSuccessorMessageNeverExceedsCapability},
	{"DaemonClassNeverUsesLineageSubmit", CheckDaemonClassNeverUsesLineageSubmit},
	{"ResolvePaneRequiresErrNoUniqueTargetWithEmptyIdentity", CheckResolvePaneRequiresNoUniqueTarget},
}

// Suite runs every registered [Check] against a fresh Transport from
// factory and fails the test at most once, naming every failing check
// rather than stopping at the first (so one broken behavior does not hide
// a second, unrelated one in the same run). factory is called once per
// Check so a stateful implementation is never accidentally shared across
// checks that assume independence.
//
// Suite never reaches a real herdr socket or tmux server; callers supply
// factory backed by fakes, following this same convention: no test call
// reaches a live transport.
func Suite(t *testing.T, factory func() sessiontransport.Transport) {
	t.Helper()
	if message := run(context.Background(), factory); message != "" {
		t.Fatal(message)
	}
}

// run is Suite's pure aggregation step, factored out so it can be tested
// directly (see suite_test.go) without needing to make a *testing.T
// actually fail to prove the "collect every failure, not just the first"
// behavior.
func run(ctx context.Context, factory func() sessiontransport.Transport) string {
	var failures []string
	for _, entry := range Checks {
		if err := entry.Check(ctx, factory()); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", entry.Name, err))
		}
	}
	if len(failures) == 0 {
		return ""
	}
	return "contract suite failed:\n" + strings.Join(failures, "\n")
}

// CheckKindMatchesCapabilities verifies Capabilities().Kind agrees with
// Kind().
func CheckKindMatchesCapabilities(_ context.Context, transport sessiontransport.Transport) error {
	if got, want := transport.Capabilities().Kind, transport.Kind(); got != want {
		return fmt.Errorf("Capabilities().Kind = %q, want %q (Kind())", got, want)
	}
	return nil
}

// CheckDeliverNeverErrors verifies Deliver never returns an error, for
// either Operation and every EventClass — REQ:none-transport-is-first-class
// generalized: a transport records rather than fails when it cannot
// deliver live.
func CheckDeliverNeverErrors(ctx context.Context, transport sessiontransport.Transport) error {
	classes := []sessiontransport.EventClass{
		sessiontransport.EventOwnPROutcome, sessiontransport.EventAdvisory, sessiontransport.EventSuccessorMessage,
	}
	for _, class := range classes {
		for _, op := range []sessiontransport.Operation{sessiontransport.OperationMessage, sessiontransport.OperationWake} {
			if _, err := transport.Deliver(ctx, sessiontransport.Target{}, sessiontransport.Delivery{
				Operation: op, Class: class, Key: "contract-suite-probe", Text: "contract-suite-probe",
			}); err != nil {
				return fmt.Errorf("Deliver(operation=%s, class=%s) returned an error, want none: %w", op, class, err)
			}
		}
	}
	return nil
}

// CheckAdvisoryNeverExceedsCapability verifies an EventAdvisory delivery
// never reports Outcome = submit, and never reports Outcome = advisory
// unless the transport declares AdvisoryDelivery
// (AC:tmux-never-delivers-unguarded, AC:none-transport-records-only).
func CheckAdvisoryNeverExceedsCapability(ctx context.Context, transport sessiontransport.Transport) error {
	caps := transport.Capabilities()
	receipt, err := transport.Deliver(ctx, sessiontransport.Target{}, sessiontransport.Delivery{
		Operation: sessiontransport.OperationWake, Class: sessiontransport.EventAdvisory, Text: "contract-suite-probe",
	})
	if err != nil {
		return fmt.Errorf("Deliver(wake, advisory) returned an error, want none: %w", err)
	}
	if receipt.Outcome == sessiontransport.ModeSubmit {
		return fmt.Errorf("Deliver(wake, advisory) reported Outcome = submit; advisory events must never submit")
	}
	if !caps.AdvisoryDelivery && receipt.Outcome != sessiontransport.ModeRecordOnly {
		return fmt.Errorf("a transport without AdvisoryDelivery must record rather than advise: Outcome = %q", receipt.Outcome)
	}
	return nil
}

// CheckSuccessorMessageNeverExceedsCapability verifies an
// EventSuccessorMessage delivery never reports Outcome = advisory (this
// path has always been record-or-submit, never an unsubmitted paste), and
// never reports Outcome = submit unless the transport declares
// LineageSubmit.
func CheckSuccessorMessageNeverExceedsCapability(ctx context.Context, transport sessiontransport.Transport) error {
	caps := transport.Capabilities()
	receipt, err := transport.Deliver(ctx, sessiontransport.Target{}, sessiontransport.Delivery{
		Operation: sessiontransport.OperationMessage, Class: sessiontransport.EventSuccessorMessage,
		Key: "contract-suite-probe", Text: "contract-suite-probe\n",
	})
	if err != nil {
		return fmt.Errorf("Deliver(message, successor-message) returned an error, want none: %w", err)
	}
	if receipt.Outcome == sessiontransport.ModeAdvisory {
		return fmt.Errorf("Deliver(message, successor-message) reported Outcome = advisory; this path is never advisory")
	}
	if !caps.LineageSubmit && receipt.Outcome != sessiontransport.ModeRecordOnly {
		return fmt.Errorf("a transport without LineageSubmit must record rather than submit: Outcome = %q", receipt.Outcome)
	}
	return nil
}

// CheckDaemonClassNeverUsesLineageSubmit verifies that a transport claiming
// LineageSubmit still never submits a daemon-originated event
// (EventOwnPROutcome or EventAdvisory): LineageSubmit is consulted only for
// EventSuccessorMessage (AC:tmux-never-delivers-unguarded's guarantee holds
// regardless of LineageSubmit — this is the probe the B1 review round asked
// for by name).
func CheckDaemonClassNeverUsesLineageSubmit(ctx context.Context, transport sessiontransport.Transport) error {
	for _, class := range []sessiontransport.EventClass{sessiontransport.EventOwnPROutcome, sessiontransport.EventAdvisory} {
		receipt, err := transport.Deliver(ctx, sessiontransport.Target{}, sessiontransport.Delivery{
			Operation: sessiontransport.OperationWake, Class: class, Text: "contract-suite-probe",
		})
		if err != nil {
			return fmt.Errorf("Deliver(wake, %s) returned an error, want none: %w", class, err)
		}
		if receipt.Outcome == sessiontransport.ModeSubmit {
			return fmt.Errorf("Deliver(wake, %s) reported Outcome = submit; LineageSubmit must never ground a daemon-originated submit", class)
		}
	}
	return nil
}

// CheckResolvePaneRequiresNoUniqueTarget verifies ResolvePane refuses with
// sessiontransport.ErrNoUniqueTarget, rather than resolving to an arbitrary
// target, when the given Identity carries no session identifier at all.
func CheckResolvePaneRequiresNoUniqueTarget(ctx context.Context, transport sessiontransport.Transport) error {
	target, err := transport.ResolvePane(ctx, sessiontransport.Identity{Kind: transport.Kind()})
	if err == nil {
		return fmt.Errorf("ResolvePane(empty identity) resolved to %#v with no error, want ErrNoUniqueTarget", target)
	}
	if !errors.Is(err, sessiontransport.ErrNoUniqueTarget) {
		return fmt.Errorf("ResolvePane(empty identity) error = %v, want ErrNoUniqueTarget", err)
	}
	return nil
}
