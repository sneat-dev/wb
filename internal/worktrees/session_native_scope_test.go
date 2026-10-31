//go:build e2e

package worktrees

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

// All Git execution happens only in the capped Linux verification worker.
// The shared physical PID/generation is real; independent native chat rows are
// explicit fixtures, avoiding any claim that each desktop chat has its own PID.
func TestE2ENativeChatPickupOrdinaryCommandAndReparkPreserveForeignCustody(t *testing.T) {
	neutralizeGitSigning(t)
	fixture := newGitFixture(t)
	directory := filepath.Join(fixture.home, session.DirName)
	newChat := func(native string) session.Record {
		scope, err := session.NativeScope("codex", native)
		if err != nil {
			t.Fatal(err)
		}
		record, err := session.Register(directory, session.Record{PID: os.Getpid(), Runtime: "codex", NativeHarnessID: native, Scope: scope, Model: session.Unknown, Machine: "test-host"})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	a, b, peer := newChat("native-a"), newChat("native-b"), newChat("native-peer")
	current := a
	SetSessionResolver(func() (AgentIdentity, bool) {
		return AgentIdentity{PID: current.PID, Runtime: current.Runtime, AgentID: current.NativeHarnessID, WBSessionID: current.WBSessionID, Model: current.Model, Registered: true}, true
	})
	t.Cleanup(func() { SetSessionResolver(nil) })
	restore := SetMutationInitiator("native-chat-journey")
	defer restore()
	SetInvokedCommand("worktree log checkpoint")
	t.Cleanup(func() { SetInvokedCommand("") })
	create := func(task string) string {
		made, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: task, WorkLog: WorkLogOptions{RunID: task + "-run", Model: current.Model, AgentRuntime: current.Runtime, AgentID: current.NativeHarnessID, OriginalPrompt: writeWorkLogPromptFile(t, "native chat custody journey\n"), RequireOriginalPrompt: true}})
		if err != nil {
			t.Fatal(err)
		}
		return made[0].WorktreeDir
	}
	worktree := create("native-scope-source")
	current = peer
	foreign := create("native-scope-foreign")
	peerEvents, err := os.ReadFile(filepath.Join(foreign, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogEventsName))
	if err != nil {
		t.Fatal(err)
	}
	peerRecordPath := filepath.Join(directory, ".scopes", peer.Scope, scopePIDFile(peer.PID))
	peerBytes, err := os.ReadFile(peerRecordPath)
	if err != nil {
		t.Fatal(err)
	}
	current = a
	branch := preparePushedParkedWorktree(t, fixture, worktree)
	claim, _, _, err := activeWorkLogClaim(fixture.home, worktree)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims", claim.ClaimID+".json")
	claimBefore, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	if claim.WBSessionID != a.WBSessionID {
		t.Fatal("initial claim was not attributed to chat A")
	}
	store := sessionpark.NewStore(filepath.Join(fixture.home, sessionpark.SourceDirName))
	var firstSuccessor session.Record
	for round := 0; round < 2; round++ {
		source := current
		_, member := captureParkedWorktreeMember(t, fixture, worktree, source, branch)
		parkID := []string{"park-native-first", "park-native-second"}[round]
		bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: parkID, Source: source, Continuation: "continue exact native chat work", ParkedAt: time.Now().UTC(), Worktrees: []sessionpark.Worktree{member}}
		if _, err := store.Create(bundle); err != nil {
			t.Fatal(err)
		}
		if _, err := session.MarkRecordParked(directory, source, parkID); err != nil {
			t.Fatal(err)
		}
		caller := b
		if round > 0 {
			caller = current
		}
		lock, err := store.Acquire(context.Background(), parkID)
		if err != nil {
			t.Fatal(err)
		}
		var successor session.Record
		err = WithParkedCurrentResumeCustody(context.Background(), fixture.projectsRoot, bundle, nil, func(custody *ParkedLocalCustody) error {
			prepared, _, err := store.PrepareCurrentUnderLock(lock, caller, custody.ResolvedWorktreeDirs(), time.Now().UTC())
			if err != nil {
				return err
			}
			if _, err := store.AcknowledgeCurrentUnderLock(lock, caller, prepared.ContinuationDigest); err != nil {
				return err
			}
			successor = prepared.Successor
			return session.WithCurrentTransition(directory, prepared.Caller, prepared.Successor, func(commit func() error) error {
				if err := custody.Attach(context.Background(), prepared.Successor, string(prepared.Digest()), 1); err != nil {
					return err
				}
				if err := commit(); err != nil {
					return err
				}
				if _, err := store.ResumeUnderLock(lock, prepared.Successor, time.Now().UTC()); err != nil {
					return err
				}
				if _, err := session.MarkSourceResumed(directory, source, parkID, prepared.Successor.WBSessionID); err != nil {
					return err
				}
				return store.CompleteCurrentUnderLock(lock, prepared)
			})
		})
		_ = lock.Close()
		if err != nil {
			t.Fatal(err)
		}
		if round == 0 {
			firstSuccessor = successor
		} else if err := session.RequireNoDeparture(directory, firstSuccessor); err == nil || !strings.Contains(err.Error(), "park-native-second") {
			t.Fatalf("old checkpoint departure was not fenced: %v", err)
		}
		current = successor
		// Exercise normal admission and the ordinary command's EnsureCustody path,
		// not a test-only owner append. The caller carries nativeID, while the
		// authoritative journal owner carries an explicitly bound logical WBID.
		if _, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce}); err != nil {
			t.Fatal(err)
		}
		if _, err := LogCheckpoint(context.Background(), LogCheckpointOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Message: "ordinary command after pickup", SkipRemote: true}); err != nil {
			t.Fatal(err)
		}
		events, err := readLocalEvents(worktree)
		if err != nil {
			t.Fatal(err)
		}
		evidence := effectiveParkOwner(claim.WBSessionID, events)
		if matched, err := evidence.MatchesSource(current); err != nil || !matched || evidence.Binding != "bound" {
			t.Fatalf("ordinary command lost bound custody: %#v %v", evidence, err)
		}
		observed, err := ObserveCollaborationLegacyOwnerReadOnly(worktree, directory)
		if err != nil || observed.Status != "live" || observed.SessionID != current.WBSessionID {
			t.Fatalf("normal collaboration owner does not recognize bound WBID: %#v %v", observed, err)
		}
		// The same physical PID cannot let B select the peer's worktree.
		foreignEvents, err := readLocalEvents(foreign)
		if err != nil {
			t.Fatal(err)
		}
		foreignClaim, _, _, err := activeWorkLogClaim(fixture.home, foreign)
		if err != nil {
			t.Fatal(err)
		}
		if owned, err := effectiveParkOwner(foreignClaim.WBSessionID, foreignEvents).MatchesSource(current); err != nil || owned {
			t.Fatalf("B acquired peer custody: %v %v", owned, err)
		}
	}
	claimAfter, err := os.ReadFile(claimPath)
	if err != nil || !bytes.Equal(claimBefore, claimAfter) {
		t.Fatalf("original claim attribution rewritten: %v", err)
	}
	if _, err := session.Prune(directory); err != nil {
		t.Fatal(err)
	}
	peerAfter, err := os.ReadFile(peerRecordPath)
	if err != nil || !bytes.Equal(peerBytes, peerAfter) {
		t.Fatalf("peer registry changed: %v", err)
	}
	eventsAfter, err := os.ReadFile(filepath.Join(foreign, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogEventsName))
	if err != nil || !bytes.Equal(peerEvents, eventsAfter) {
		t.Fatalf("peer journal changed: %v", err)
	}
}

func scopePIDFile(pid int) string { return fmt.Sprint(pid) + ".json" }
