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

func TestUnfetchedRemoteObjectIsARefusalSignal(t *testing.T) {
	if !isUnfetchedGitObjectError(assertiveError("exit status 128: fatal: Not a valid commit name deadbeef")) {
		t.Fatal("missing remote object must be classified without aborting inventory")
	}
	if isUnfetchedGitObjectError(assertiveError("permission denied")) {
		t.Fatal("operational errors must remain fatal")
	}
}

type assertiveError string

func (e assertiveError) Error() string { return string(e) }
