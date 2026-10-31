//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

// This native Git/Work Log journey belongs only in the capped Linux runner.
// It deliberately leaves three different actor attempts across five members.
func TestE2ECurrentRecoveryRepairsFiveMembersAfterTwoInterruptedActors(t *testing.T) {
	fixture, first, source := newSessionCheckpointFixture(t, "current-five-0")
	useIdentityRemote(t, fixture, first)
	paths := []string{first}
	for index := 1; index < 5; index++ {
		created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: fmt.Sprintf("current-five-%d", index), WorkLog: WorkLogOptions{RunID: fmt.Sprintf("current-five-run-%d", index), Model: source.Model, AgentRuntime: source.Runtime, AgentID: source.NativeHarnessID, OriginalPrompt: writeWorkLogPromptFile(t, "current recovery member\n"), RequireOriginalPrompt: true}})
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, created[0].WorktreeDir)
	}
	members := make([]sessionpark.Worktree, 0, 5)
	for _, path := range paths {
		branch := gitTestOutput(t, path, "branch", "--show-current")
		gitTest(t, path, "push", "origin", branch)
		_, member := captureParkedWorktreeMember(t, fixture, path, source, branch)
		members = append(members, member)
	}
	base := time.Now().UTC().Add(time.Second)
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-current-five", Source: source, Continuation: "restore all five members", ParkedAt: base, Worktrees: members}
	store := sessionpark.NewStore(fixture.projectsRoot + "/.wb/parked-sessions")
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock, err := store.Acquire(context.Background(), bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	interrupted := errors.New("actor stopped after member")
	attempts := []string{}
	logicalWinner := ""
	for actor := 0; actor < 3; actor++ {
		caller := source
		caller.PID = 1000000 + actor
		caller.WBSessionID = fmt.Sprintf("wbs-actor-%d", actor)
		caller.NativeHarnessID = fmt.Sprintf("native-%d", actor)
		caller.ProcessStartedAt = base.Add(time.Duration(actor) * time.Second)
		caller.StartedAt = caller.ProcessStartedAt
		err = WithParkedCurrentResumeCustody(context.Background(), fixture.projectsRoot, bundle, attempts, func(custody *ParkedLocalCustody) error {
			var p sessionpark.CurrentPreparation
			var err error
			if actor == 0 {
				p, _, err = store.PrepareCurrentUnderLock(lock, caller, custody.ResolvedWorktreeDirs(), base.Add(10*time.Second))
			} else {
				p, _, err = store.PrepareCurrentRecoveryUnderLock(lock, caller, custody.ResolvedWorktreeDirs(), base.Add(time.Duration(10+actor)*time.Second), func(previous session.Record) error {
					if previous.PID != caller.PID-1 {
						return fmt.Errorf("wrong previous actor")
					}
					return nil
				})
			}
			if err != nil {
				return err
			}
			if logicalWinner == "" {
				logicalWinner = p.Successor.WBSessionID
			} else if logicalWinner != p.Successor.WBSessionID {
				t.Fatal("recovery changed source winner")
			}
			if _, err := store.AcknowledgeCurrentUnderLock(lock, caller, p.ContinuationDigest); err != nil {
				return err
			}
			attempts = append(attempts, string(p.Digest()))
			custody.beforeAttachMember = func(index int) error {
				if actor == 0 && index == 3 || actor == 1 && index == 2 {
					return interrupted
				}
				if actor == 2 && index == 1 {
					seen := map[string]bool{}
					for _, member := range custody.members {
						events, _, err := readLocalEventsForAppend(member.directory)
						if err != nil {
							return err
						}
						var latest LocalWorkLogEvent
						for _, event := range events {
							if event.Type == LocalEventOwner && event.Owner != nil {
								latest = event
							}
						}
						if id, ok := latest.Extra["attempt_id"].(string); ok {
							seen[id] = true
						}
					}
					if len(seen) != 3 || !seen[attempts[0]] || !seen[attempts[1]] || !seen[attempts[2]] {
						t.Fatal("mixed original/recovery1/recovery2 owners were not exercised")
					}

				}
				return nil
			}
			if err := custody.Attach(context.Background(), p.Successor, string(p.Digest()), 1); err != nil {
				return err
			}
			state, err := store.ResumeUnderLock(lock, p.Successor, base.Add(30*time.Second))
			if err != nil {
				return err
			}
			if state.Successor == nil || state.Successor.WBSessionID != logicalWinner {
				t.Fatal("final receipt changed winner")
			}
			return store.CompleteCurrentUnderLock(lock, p)
		})
		if actor < 2 {
			if !errors.Is(err, interrupted) {
				t.Fatalf("actor %d interruption: %v", actor, err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if err := WithParkedCurrentResumeCustody(context.Background(), fixture.projectsRoot, bundle, attempts, func(custody *ParkedLocalCustody) error {
		if len(custody.members) != 5 {
			t.Fatal("lost members")
		}
		for _, member := range custody.members {
			events, _, err := readLocalEventsForAppend(member.directory)
			if err != nil {
				return err
			}
			latest := ""
			for _, event := range events {
				if event.Type == LocalEventOwner && event.Owner != nil {
					latest = event.ID
				}
			}
			if latest != externalLocalEventID("park-local-owner", mustCurrentBundleDigest(t, bundle), attempts[2]+"-"+member.member.OwnerEventID) {
				t.Fatal("member did not reach final actor")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func mustCurrentBundleDigest(t *testing.T, bundle sessionpark.Bundle) sessionmove.Digest {
	t.Helper()
	raw, err := sessionpark.EncodeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return sessionmove.DigestBytes(raw)
}
