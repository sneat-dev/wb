package runqueue

import (
	"context"
	"testing"
)

// TestBudgetFloorsAtOneOnASingleCoreMachine drives Budget's numCPU<=1
// branch (queue.go): SetNumCPUForTest is the package's own test seam for
// this, exactly as its own doc comment describes.
//
// Not parallel: SetNumCPUForTest mutates a package-level variable.
func TestBudgetFloorsAtOneOnASingleCoreMachine(t *testing.T) {
	restore := SetNumCPUForTest(1)
	defer restore()
	if got := Budget(); got != 1 {
		t.Fatalf("Budget() on a 1-CPU machine = %d, want 1", got)
	}
}

// TestHasExplicitNxTargetSkipsLeadingFlags drives hasExplicitNxTarget's
// flag-skipping continue (queue.go): a flag before the nx verb must not
// itself count as the verb, so the project name after the verb is still
// recognized as the explicit target.
func TestHasExplicitNxTargetSkipsLeadingFlags(t *testing.T) {
	t.Parallel()
	if got := Classify([]string{"nx", "--verbose", "test", "app"}); got != KindFocused {
		t.Fatalf("Classify(nx --verbose test app) = %v, want KindFocused (explicit target after a skipped flag)", got)
	}
}

// TestClassifyNodePackageManagerWithNoArguments drives
// classifyNodePackageManager's own bare-invocation path (queue.go): a
// package manager name with nothing after it names no script and no
// workspace scope.
func TestClassifyNodePackageManagerWithNoArguments(t *testing.T) {
	t.Parallel()
	if got := Classify([]string{"pnpm"}); got != KindNone {
		t.Fatalf("Classify(pnpm) = %v, want KindNone", got)
	}
}

// TestClassifyNodePackageManagerRunsATestRunnerScript drives
// classifyNodePackageManager's script-token recognition for a bare
// "vitest"-named script (queue.go).
func TestClassifyNodePackageManagerRunsATestRunnerScript(t *testing.T) {
	t.Parallel()
	if got := Classify([]string{"pnpm", "vitest"}); got != KindFocused && got != KindBroad {
		t.Fatalf("Classify(pnpm vitest) = %v, want a governed kind, not KindNone", got)
	}
}

// TestClassifyNodePackageManagerFallsThroughToKindNone drives
// classifyNodePackageManager's final catch-all return (queue.go): a "run"
// of a script name this package does not recognize as broad, focused, or
// workspace-scoped falls through to KindNone.
func TestClassifyNodePackageManagerFallsThroughToKindNone(t *testing.T) {
	t.Parallel()
	if got := Classify([]string{"pnpm", "run", "foo"}); got != KindNone {
		t.Fatalf("Classify(pnpm run foo) = %v, want KindNone", got)
	}
}

// TestUnitsOnALargeMachineUsesFocusedShare drives Units' large-machine
// (numCPU >= smallMachineThreshold) branch, distinct from the small-machine
// table exercised elsewhere in this package's tests.
//
// Not parallel: SetNumCPUForTest mutates a package-level variable.
func TestUnitsOnALargeMachineUsesFocusedShare(t *testing.T) {
	restore := SetNumCPUForTest(32)
	defer restore()
	got := Units([]string{"go", "test", "./x"}, 8)
	if got != focusedShare() || got <= 0 {
		t.Fatalf("Units(focused, large machine) = %d, want focusedShare() = %d", got, focusedShare())
	}
}

// TestLeaseReleaseOnNilReceiverIsANoOp and
// TestLeaseHeartbeatOnNilReceiverIsANoOp drive (*Lease).Release and
// (*Lease).Heartbeat's nil-receiver guards (queue.go): both methods are
// documented safe to call on a nil *Lease.
func TestLeaseReleaseOnNilReceiverIsANoOp(t *testing.T) {
	t.Parallel()
	var lease *Lease
	lease.Release() // must not panic
}

func TestLeaseHeartbeatOnNilReceiverIsANoOp(t *testing.T) {
	t.Parallel()
	var lease *Lease
	lease.Heartbeat() // must not panic
}

// TestRegisterForAdmissionReturnsNilForAnUngovernedCommand drives
// RegisterForAdmission's KindNone branch (queue.go): a command Classify
// does not recognize (here, "ls") needs no admission ticket at all.
func TestRegisterForAdmissionReturnsNilForAnUngovernedCommand(t *testing.T) {
	t.Parallel()
	if ticket := RegisterForAdmission(t.TempDir(), []string{"ls"}, Participant{PID: 1}); ticket != nil {
		t.Fatalf("RegisterForAdmission(ls) = %#v, want nil (KindNone needs no ticket)", ticket)
	}
}

// TestAdmitReturnsEmptyLeaseForAnUngovernedCommand drives Admit's KindNone
// branch (queue.go): an ungoverned command is admitted immediately with an
// empty Lease and no wait, ticket or error.
func TestAdmitReturnsEmptyLeaseForAnUngovernedCommand(t *testing.T) {
	t.Parallel()
	admission, err := Admit(context.Background(), t.TempDir(), []string{"ls"}, Participant{PID: 1}, nil)
	if err != nil {
		t.Fatalf("Admit(ls) error = %v, want nil", err)
	}
	if admission.Units != 0 || admission.Waited != 0 {
		t.Fatalf("Admit(ls) = %#v, want zero Units and no wait", admission)
	}
}
