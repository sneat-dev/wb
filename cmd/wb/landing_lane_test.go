package main

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestReleaseWorktreeMergeLaneReleasesARegisteredOwnersLane proves
// releaseWorktreeMergeLane reaches the real landing-lane store: a releasable
// receipt from a registered session owner must actually clear that owner's
// held lane, not merely no-op the way an unregistered process's call does.
func TestReleaseWorktreeMergeLaneReleasesARegisteredOwnersLane(t *testing.T) {
	root := t.TempDir()
	inv := &invocation{projectsRoot: root}

	sessionDir, err := sessionDirForRead(inv)
	if err != nil {
		t.Fatal(err)
	}
	record, err := session.Register(sessionDir, session.Record{PID: os.Getpid(), WBSessionID: "wbs-release-lane", Runtime: "test"})
	if err != nil {
		t.Fatal(err)
	}

	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := landinglane.Acquire(home, landinglane.AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self:       landinglane.Owner{WBSessionID: record.WBSessionID, PID: record.PID},
		SessionDir: sessionDir,
	}); err != nil {
		t.Fatalf("acquire lane: %v", err)
	}
	if _, held, err := landinglane.Read(home, "acme/app", "main"); err != nil || !held {
		t.Fatalf("lane before release: held=%v err=%v", held, err)
	}

	releaseWorktreeMergeLane(inv, orchestrate.WorktreeMergeReceipt{
		Repository: "acme/app", Target: "main", Status: orchestrate.WorktreeMergeLanded,
	})

	_, held, err := landinglane.Read(home, "acme/app", "main")
	if err != nil {
		t.Fatalf("lane after release: %v", err)
	}
	if held {
		t.Fatal("lane is still held after release")
	}
}

// TestReleaseWorktreeMergeLaneIsANoOpWithoutAnOwner proves an unregistered
// process's call is a deliberate no-op, exactly as landingLaneOwner documents.
func TestReleaseWorktreeMergeLaneIsANoOpWithoutAnOwner(t *testing.T) {
	inv := &invocation{projectsRoot: t.TempDir()}
	releaseWorktreeMergeLane(inv, orchestrate.WorktreeMergeReceipt{
		Repository: "acme/app", Target: "main", Status: orchestrate.WorktreeMergeLanded,
	})
}
