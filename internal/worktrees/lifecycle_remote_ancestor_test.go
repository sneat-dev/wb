package worktrees

import (
	"strings"
	"testing"
	"time"
)

func TestCleanupSafetyRejectsDivergedRemoteEvenWhenLocalIsIntegrated(t *testing.T) {
	eligible, reason := cleanupSafetyEligibility(ListResult{
		Clean: true, IntegratedAtOrigin: true,
		HeadSHA: "local", RemoteHeadSHA: "diverged",
	}, 0, time.Now(), false)
	if eligible || !strings.Contains(reason, "remote branch advanced") {
		t.Fatalf("eligible=%t reason=%q", eligible, reason)
	}
}
