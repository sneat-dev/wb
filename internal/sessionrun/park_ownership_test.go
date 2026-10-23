package sessionrun

import (
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/worktrees"
	"strings"
	"testing"
	"time"
)

func TestSessionParkCapturesAllOwnedWorktrees(t *testing.T) {
	t.Parallel()
	source := session.Record{PID: 41, WBSessionID: "wbs-source", StartedAt: time.Unix(10, 0)}
	results := []worktrees.ListResult{{WorktreeDir: "/tmp/a", Owners: []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: 41, At: time.Unix(11, 0)}}}}, {WorktreeDir: "/tmp/b", Owners: []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: 41, At: time.Unix(12, 0)}}}}, {WorktreeDir: "/tmp/old", Owners: []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: 41, At: time.Unix(9, 0)}}}}}
	count := 0
	for _, result := range results {
		if ownedBySession(result, source) {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("owned worktrees = %d, want 2", count)
	}
}
func TestSessionParkDoesNotTreatDifferentPIDAsOwned(t *testing.T) {
	t.Parallel()
	source := session.Record{PID: 41, WBSessionID: "wbs-source", StartedAt: time.Unix(10, 0)}
	result := worktrees.ListResult{WorktreeDir: "/tmp/other", Owners: []worktrees.OwnerView{{OwnerRegistration: worktrees.OwnerRegistration{PID: 42, At: time.Unix(11, 0)}}}}
	if ownedBySession(result, source) {
		t.Fatal("different session worktree attributed")
	}
}
func TestSessionParkUsesImmutableClaimSessionLink(t *testing.T) {
	t.Parallel()
	source := session.Record{PID: 41, WBSessionID: "wbs-source", StartedAt: time.Unix(10, 0)}
	result := worktrees.ListResult{WorktreeDir: "/tmp/unowned-projection", WorkLogSessionID: "wbs-source"}
	if !ownedBySession(result, source) {
		t.Fatal("claim linked to live source session was not selected for park")
	}
}
func TestSessionParkDoesNotUseDifferentClaimSessionLink(t *testing.T) {
	t.Parallel()
	source := session.Record{PID: 41, WBSessionID: "wbs-source", StartedAt: time.Unix(10, 0)}
	result := worktrees.ListResult{WorktreeDir: "/tmp/other-session", WorkLogSessionID: "wbs-other"}
	if ownedBySession(result, source) {
		t.Fatal("claim linked to another session was attributed to source")
	}
}
func TestSessionParkRemoteReconstructabilityRefusesDirtyEvidence(t *testing.T) {
	t.Parallel()
	bundle := sessionpark.Bundle{ParkedSessionID: "park-dirty", Worktrees: []sessionpark.Worktree{{WorktreeDir: "/tmp/dirty", Head: "a", RemoteHead: "a", Dirty: true}}}
	err := validateParkedRemoteBundle(bundle, "hetzner-vm1")
	if err == nil || !strings.Contains(err.Error(), "/tmp/dirty") || !strings.Contains(err.Error(), "dirty=true") {
		t.Fatalf("error = %v", err)
	}
}
