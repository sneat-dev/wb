package runner

import "testing"

// withGuardOverride sets *target to value for the duration of t, restoring
// the original at cleanup. It exists because isTesting and e2eBuild are
// package variables precisely so these tests can flip them, and every case
// below must undo that before the next test runs.
func withGuardOverride(t *testing.T, target *func() bool, value bool) {
	t.Helper()
	original := *target
	*target = func() bool { return value }
	t.Cleanup(func() { *target = original })
}

func TestGuardRealProcessAllowsOutsideATestBinary(t *testing.T) {
	t.Setenv(envStrictUnitTier, "1")
	withGuardOverride(t, &isTesting, false)
	if err := guardRealProcess(); err != nil {
		t.Fatalf("guardRealProcess() = %v, want nil outside a test binary", err)
	}
}

func TestGuardRealProcessAllowsAnE2EBuild(t *testing.T) {
	t.Setenv(envStrictUnitTier, "1")
	withGuardOverride(t, &e2eBuild, true)
	if err := guardRealProcess(); err != nil {
		t.Fatalf("guardRealProcess() = %v, want nil for an e2e build", err)
	}
}

func TestGuardRealProcessAllowsTheHelperProcessReexecMarker(t *testing.T) {
	t.Setenv(envStrictUnitTier, "1")
	t.Setenv(envHelperProcess, "1")
	if err := guardRealProcess(); err != nil {
		t.Fatalf("guardRealProcess() = %v, want nil when %s=1", err, envHelperProcess)
	}
}

func TestGuardRealProcessAllowsTheHelperProcessObserveMarker(t *testing.T) {
	t.Setenv(envStrictUnitTier, "1")
	t.Setenv(envHelperProcessObserve, "1")
	if err := guardRealProcess(); err != nil {
		t.Fatalf("guardRealProcess() = %v, want nil when %s=1", err, envHelperProcessObserve)
	}
}

func TestGuardRealProcessAllowsAllowRealProcess(t *testing.T) {
	t.Setenv(envStrictUnitTier, "1")
	t.Setenv(envAllowRealProcess, "1")
	if err := guardRealProcess(); err != nil {
		t.Fatalf("guardRealProcess() = %v, want nil when %s=1", err, envAllowRealProcess)
	}
}

func TestGuardRealProcessUnitTierPolicy(t *testing.T) {
	t.Setenv(envStrictUnitTier, "")
	if err := guardRealProcess(); err != nil {
		t.Fatalf("guardRealProcess() = %v, want nil without strict unit tier", err)
	}
	t.Setenv(envStrictUnitTier, "1")
	if err := guardRealProcess(); err != ErrRealProcessBlocked {
		t.Fatalf("guardRealProcess() = %v, want ErrRealProcessBlocked", err)
	}
}
