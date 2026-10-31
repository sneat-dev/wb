package sessionpark

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func legacyEmptyAttemptFixture(t *testing.T, sourceModel ...string) (Store, *SourceLock, session.Record, *UnstartedLaunchAttestation, string) {
	t.Helper()
	s, lock, caller := currentFixture(t, sourceModel...)
	if _, _, err := s.PrepareLocalUnderLock(lock, time.Unix(25, 0)); err != nil {
		t.Fatal(err)
	}
	path, continuation, err := s.EnsureLocalSuccessorContextUnderLock(lock, nil)
	if err != nil {
		t.Fatal(err)
	}
	bundleRaw, _ := EncodeBundle(lock.bundle)
	authority, err := LocalLaunchAuthority(lock.bundle, sessionmove.DigestBytes(bundleRaw), path, continuation)
	if err != nil {
		t.Fatal(err)
	}
	worktree := lock.bundle.Worktrees[0].WorktreeDir
	plan := legacyUnstartedPlan{SchemaVersion: 1, HandoffID: lock.parkID, RequestDigest: sessionmove.DigestBytes(bundleRaw), Successor: authority.SuccessorWBSessionID, Predecessor: lock.bundle.Source.WBSessionID, Machine: authority.TargetMachine, TmuxName: "wb-session-" + authority.SuccessorWBSessionID, Runtime: authority.SourceRuntime, Model: authority.SourceModel, StoreRoot: s.Root, WorktreeDir: worktree, PinnedCommit: authority.PinnedCommit, PinnedBranch: authority.PinnedBranch, RootMode: string(authority.RootMode), HandoverPath: path, AuthorityFile: BundleFileName, ContinuationKind: string(authority.ContinuationKind), ContinuationDigest: sessionmove.DigestBytes(continuation), WBExecutable: filepath.Join(t.TempDir(), "wb"), HarnessExecutable: filepath.Join(t.TempDir(), "codex"), HarnessArgs: []string{"-C", worktree, fmt.Sprintf("Continue WB parked session %s as session %s from predecessor %s. Read the file named by WB_SESSION_CONTINUATION_FILE before acting.", authority.AggregateID, authority.SuccessorWBSessionID, authority.PredecessorWBSessionID)}}
	if plan.Model != "" {
		plan.HarnessArgs = []string{"-C", worktree, "-m", plan.Model, plan.HarnessArgs[len(plan.HarnessArgs)-1]}
	}
	launch := filepath.Join(s.Root, lock.parkID, "launch")
	attempt := filepath.Join(launch, "attempts", "000001-03eb86a41283d3a7c9dd251542d79f66")
	for _, child := range []string{"ready", "exec"} {
		if err := os.MkdirAll(filepath.Join(attempt, child), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launch, "plan.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := s.inspectUnstartedLauncherUnderLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	state.ActorDigest = unstartedActorDigest(caller)
	return s, lock, caller, state, attempt
}

func TestCurrentPickupRequiresExactOperatorAssertionForLegacyEmptyAttempt(t *testing.T) {
	s, lock, caller, statement, attempt := legacyEmptyAttemptFixture(t)
	planPath := filepath.Join(s.Root, lock.parkID, "launch", "plan.json")
	routePath := filepath.Join(s.Root, lock.parkID, sourceResumeRouteFileName)
	planBefore, _ := os.ReadFile(planPath)
	routeBefore, _ := os.ReadFile(routePath)
	if _, _, err := s.PrepareCurrentUnderLock(lock, caller, nil, time.Unix(30, 0)); err == nil || !strings.Contains(err.Error(), string(statement.Digest())) || !strings.Contains(err.Error(), "empty directories alone do not prove absence") {
		t.Fatalf("default did not require explicit external evidence: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, lock.parkID, currentPreparationName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default refusal published preparation: %v", err)
	}
	if _, _, err := s.PrepareCurrentWithUnstartedAcknowledgmentUnderLock(lock, caller, nil, time.Unix(30, 0), sessionmove.DigestBytes([]byte("wrong"))); err == nil {
		t.Fatal("wrong statement acknowledged")
	}
	foreign := caller
	foreign.Model = "changed-actor"
	if _, _, err := s.PrepareCurrentWithUnstartedAcknowledgmentUnderLock(lock, foreign, nil, time.Unix(30, 0), statement.Digest()); err == nil {
		t.Fatal("acknowledgment transferred to a different actor snapshot")
	}
	p, _, err := s.PrepareCurrentWithUnstartedAcknowledgmentUnderLock(lock, caller, nil, time.Unix(30, 0), statement.Digest())
	if err != nil {
		t.Fatal(err)
	}
	if p.UnstartedLaunch == nil || p.UnstartedLaunch.Digest() != statement.Digest() || p.Successor.WBSessionID != localSuccessorID(lock.parkID) {
		t.Fatal("preparation lost assertion or changed logical winner")
	}
	if _, err := s.AcknowledgeCurrentUnderLock(lock, caller, p.ContinuationDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResumeUnderLock(lock, p.Successor, time.Unix(35, 0)); err != nil {
		t.Fatal(err)
	}
	replay, _, err := s.PrepareCurrentUnderLock(lock, p.Successor, nil, time.Unix(40, 0))
	if err != nil || replay.Digest() != p.Digest() {
		t.Fatalf("committed-successor retry lost immutable original actor: %#v %v", replay, err)
	}
	fresh := caller
	fresh.PID++
	fresh.ProcessStartedAt = fresh.ProcessStartedAt.Add(time.Hour)
	fresh.WBSessionID = "wbs-recovery-actor"
	recovered, _, err := s.PrepareCurrentRecoveryUnderLock(lock, fresh, nil, time.Unix(45, 0), func(old session.Record) error {
		if old.WBSessionID != caller.WBSessionID {
			return errors.New("wrong original actor")
		}
		return nil
	})
	if err != nil || recovered.UnstartedLaunch != nil || recovered.Successor.WBSessionID != p.Successor.WBSessionID {
		t.Fatalf("dead-actor recovery lost historical assertion: %#v %v", recovered, err)
	}
	if _, err := s.AcknowledgeCurrentUnderLock(lock, fresh, recovered.ContinuationDigest); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{planPath: planBefore, routePath: routeBefore} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("historical artifact changed: %s %v", path, err)
		}
	}
	for _, child := range []string{"ready", "exec"} {
		entries, err := os.ReadDir(filepath.Join(attempt, child))
		if err != nil || len(entries) != 0 {
			t.Fatalf("historical attempt changed: %v", err)
		}
	}
}

func TestLegacyEmptyAttemptAssertionNeverOverridesExecutionOrUnknownEvidence(t *testing.T) {
	for _, evidence := range []string{"ready/123.json", "exec/123.lock", "exec/123.failure.json", "release.json", "abandoned.json", "submitted.json", "child.json", "unknown.json", "launch-started", "launch-winner", "extra-attempt", "symlink-ready", "nonprivate-exec", "partial", "wrong-plan", "changed-plan-bytes", "changed-route-bytes", "replaced-exec"} {
		t.Run(evidence, func(t *testing.T) {
			s, lock, caller, statement, attempt := legacyEmptyAttemptFixture(t)
			launch := filepath.Dir(filepath.Dir(attempt))
			switch evidence {
			case "launch-started", "launch-winner":
				if err := os.WriteFile(filepath.Join(launch, strings.TrimPrefix(evidence, "launch-")+".json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "extra-attempt":
				if err := os.Mkdir(filepath.Join(filepath.Dir(attempt), "000002-11111111111111111111111111111111"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink-ready":
				if err := os.Remove(filepath.Join(attempt, "ready")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(attempt, "ready")); err != nil {
					t.Fatal(err)
				}
			case "nonprivate-exec":
				if err := os.Chmod(filepath.Join(attempt, "exec"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "partial":
				if err := os.Remove(filepath.Join(attempt, "ready")); err != nil {
					t.Fatal(err)
				}
			case "wrong-plan", "changed-plan-bytes":
				path := filepath.Join(launch, "plan.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if evidence == "wrong-plan" {
					raw = bytes.Replace(raw, []byte(`"machine": "laptop"`), []byte(`"machine": "foreign"`), 1)
				} else {
					raw = append(raw, '\n')
				}
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "changed-route-bytes":
				path := filepath.Join(s.Root, lock.parkID, sourceResumeRouteFileName)
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			case "replaced-exec":
				if err := os.Rename(filepath.Join(attempt, "exec"), filepath.Join(t.TempDir(), "old-exec")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(attempt, "exec"), 0o700); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(filepath.Join(attempt, evidence), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := s.PrepareCurrentWithUnstartedAcknowledgmentUnderLock(lock, caller, nil, time.Unix(30, 0), statement.Digest()); err == nil {
				t.Fatal("explicit assertion overrode changed/ambiguous execution state")
			}
			if _, err := os.Stat(filepath.Join(s.Root, lock.parkID, currentPreparationName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refusal published preparation: %v", err)
			}
		})
	}
}

func TestAttestedLegacyAttemptStateMustRemainExactAcrossRetryAndRecovery(t *testing.T) {
	s, lock, caller, statement, attempt := legacyEmptyAttemptFixture(t)
	p, _, err := s.PrepareCurrentWithUnstartedAcknowledgmentUnderLock(lock, caller, nil, time.Unix(30, 0), statement.Digest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attempt, "exec", "123.lock"), []byte("late"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PrepareCurrentUnderLock(lock, p.Successor, nil, time.Unix(35, 0)); err == nil {
		t.Fatal("late execution evidence ignored on successor retry")
	}
	fresh := caller
	fresh.PID++
	fresh.ProcessStartedAt = fresh.ProcessStartedAt.Add(time.Hour)
	fresh.WBSessionID = "wbs-fresh-actor"
	if _, _, err := s.PrepareCurrentRecoveryUnderLock(lock, fresh, nil, time.Unix(40, 0), func(session.Record) error { t.Fatal("ambiguous attempt reached actor recovery"); return nil }); err == nil {
		t.Fatal("late execution evidence ignored on dead-actor recovery")
	}
}

func TestExistingCurrentPreparationCannotAcquireOrReattributeLegacyAssertion(t *testing.T) {
	for _, mutation := range []string{"remove attestation", "change actor", "recovery attestation"} {
		t.Run(mutation, func(t *testing.T) {
			s, lock, caller, statement, _ := legacyEmptyAttemptFixture(t)
			p, _, err := s.PrepareCurrentWithUnstartedAcknowledgmentUnderLock(lock, caller, nil, time.Unix(30, 0), statement.Digest())
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "remove attestation":
				p.UnstartedLaunch = nil
			case "change actor":
				p.UnstartedLaunch.ActorDigest = sessionmove.DigestBytes([]byte("foreign"))
			case "recovery attestation":
				p.RecoveryIndex = 1
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.Root, lock.parkID, currentPreparationName), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.PrepareCurrentWithUnstartedAcknowledgmentUnderLock(lock, caller, nil, time.Unix(35, 0), statement.Digest()); err == nil {
				t.Fatal("existing preparation gained or reattributed assertion authority")
			}
		})
	}
}

func TestOriginalLegacyUnknownModelArgvRemainsValidOnlyAsHistoricalEvidence(t *testing.T) {
	s, lock, caller, statement, _ := legacyEmptyAttemptFixture(t, "unknown")
	if caller.Model != "unknown" {
		t.Fatal("fixture lost original unknown model")
	}
	if _, _, err := s.PrepareCurrentWithUnstartedAcknowledgmentUnderLock(lock, caller, nil, time.Unix(30, 0), statement.Digest()); err != nil {
		t.Fatalf("original literal -m unknown legacy plan refused: %v", err)
	}
}
