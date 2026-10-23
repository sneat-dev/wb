//go:build !linux

package worktrees

import "testing"

func TestBusyProcessReasonIsUnsupportedOffLinux(t *testing.T) {
	t.Parallel()
	if BusyProcessCheckSupported {
		t.Fatal("non-Linux build unexpectedly supports process inspection")
	}
	if reason := BusyProcessReason([]string{"/a/checkout"}); reason != "" {
		t.Fatalf("unsupported process inspection reason = %q, want empty", reason)
	}
}
