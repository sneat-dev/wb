package sessionpark

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func currentFixture(t *testing.T, sourceModel ...string) (Store, *SourceLock, session.Record) {
	t.Helper()
	s := NewStore(t.TempDir())
	bundle := testBundle(t)
	if len(sourceModel) != 0 {
		bundle.Source.Model = sourceModel[0]
	}
	if _, err := s.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock, err := s.Acquire(context.Background(), bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	caller := bundle.Source
	caller.NativeHarnessID = "native-chat"
	caller.ProcessStartedAt = time.Unix(5, 0).UTC()
	return s, lock, caller
}

func TestCurrentRecoveryRefusesLiveUnknownAndForeignActorsBeforeMutation(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"live", "unknown", "wrong-machine", "same-generation"} {
		t.Run(reason, func(t *testing.T) {
			s, lock, caller := currentFixture(t)
			original, _, err := s.PrepareCurrentUnderLock(lock, caller, nil, time.Unix(30, 0))
			if err != nil {
				t.Fatal(err)
			}
			fresh := caller
			fresh.PID++
			fresh.WBSessionID = "wbs-new-chat"
			fresh.NativeHarnessID = "new-native"
			fresh.ProcessStartedAt = fresh.ProcessStartedAt.Add(time.Second)
			proof := func(session.Record) error { return nil }
			if reason == "live" || reason == "unknown" {
				proof = func(session.Record) error { return errors.New(reason) }
			}
			if reason == "wrong-machine" {
				fresh.Machine = "other-machine"
			}
			if reason == "same-generation" {
				fresh.PID = caller.PID
				fresh.ProcessStartedAt = caller.ProcessStartedAt
				proof = func(session.Record) error { return errors.New("same generation still live") }
			}
			if _, _, err := s.PrepareCurrentRecoveryUnderLock(lock, fresh, nil, time.Unix(40, 0), proof); err == nil {
				t.Fatal("unsafe recovery admitted")
			}
			chain, err := s.CurrentPreparationsUnderLock(lock)
			if err != nil || len(chain) != 1 || chain[0].Digest() != original.Digest() {
				t.Fatalf("refusal changed original chain: %#v %v", chain, err)
			}
		})
	}
}

func TestCurrentRecoveryRetainsCompletedWinnerAndRequiresFreshActorAck(t *testing.T) {
	t.Parallel()
	s, lock, caller := currentFixture(t)
	p, _, err := s.PrepareCurrentUnderLock(lock, caller, nil, time.Unix(30, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcknowledgeCurrentUnderLock(lock, caller, p.ContinuationDigest); err != nil {
		t.Fatal(err)
	}
	first, err := s.ResumeUnderLock(lock, p.Successor, time.Unix(31, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteCurrentUnderLock(lock, p); err != nil {
		t.Fatal(err)
	}
	fresh := caller
	fresh.PID++
	fresh.WBSessionID = "wbs-recovery-chat"
	fresh.NativeHarnessID = "native-recovery"
	fresh.ProcessStartedAt = fresh.ProcessStartedAt.Add(time.Second)
	recovered, _, err := s.PrepareCurrentRecoveryUnderLock(lock, fresh, nil, time.Unix(40, 0), func(session.Record) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ParentDigest != p.Digest() || recovered.RecoveryIndex != 1 || recovered.Successor.WBSessionID != p.Successor.WBSessionID {
		t.Fatal("recovery rewrote logical winner")
	}
	if err := s.CompleteCurrentUnderLock(lock, recovered); err == nil {
		t.Fatal("old acknowledgment authorized new actor")
	}
	if _, err := s.AcknowledgeCurrentUnderLock(lock, fresh, recovered.ContinuationDigest); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteCurrentUnderLock(lock, recovered); err != nil {
		t.Fatal(err)
	}
	state, err := s.LoadUnderLock(lock)
	if err != nil || state.Successor == nil || *state.Successor != *first.Successor {
		t.Fatalf("historical winner receipt changed: %#v %v", state, err)
	}
	// A forged parent must not be admitted simply because all actor fields parse.
	raw, err := os.ReadFile(filepath.Join(s.Root, lock.parkID, currentRecoveryName(1)))
	if err != nil {
		t.Fatal(err)
	}
	var corrupt CurrentPreparation
	if err := json.Unmarshal(raw, &corrupt); err != nil {
		t.Fatal(err)
	}
	corrupt.ParentDigest = sessionmove.DigestBytes([]byte("foreign-parent"))
	raw, _ = json.Marshal(corrupt)
	if err := os.WriteFile(filepath.Join(s.Root, lock.parkID, currentRecoveryName(1)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CurrentPreparationsUnderLock(lock); err == nil {
		t.Fatal("forged recovery parent accepted")
	}
}

func TestCurrentRecoveryRejectsOutOfRangeLinksAndConflictingLaunchArtifacts(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"out-of-range", "launch-conflict"} {
		t.Run(kind, func(t *testing.T) {
			s, lock, caller := currentFixture(t)
			if _, _, err := s.PrepareCurrentUnderLock(lock, caller, nil, time.Unix(30, 0)); err != nil {
				t.Fatal(err)
			}
			if kind == "out-of-range" {
				if err := os.WriteFile(filepath.Join(s.Root, lock.parkID, "current-recovery-018.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := s.CurrentPreparationsUnderLock(lock); err == nil {
					t.Fatal("out-of-range recovery ignored")
				}
			} else {
				launch := filepath.Join(s.Root, lock.parkID, "launch")
				if err := os.Mkdir(launch, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(launch, "attempt.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				caller.PID++
				caller.WBSessionID = "wbs-fresh"
				caller.NativeHarnessID = "fresh"
				caller.ProcessStartedAt = caller.ProcessStartedAt.Add(time.Second)
				if _, _, err := s.PrepareCurrentRecoveryUnderLock(lock, caller, nil, time.Unix(40, 0), func(session.Record) error { return nil }); err == nil {
					t.Fatal("recovery crossed conflicting launch evidence")
				}
				chain, err := s.CurrentPreparationsUnderLock(lock)
				if err != nil || len(chain) != 1 {
					t.Fatal("launch refusal appended recovery")
				}
			}
		})
	}
}

func TestCurrentPreparationAcknowledgmentAndSamePIDRetry(t *testing.T) {
	t.Parallel()
	s, lock, caller := currentFixture(t)
	p, path, err := s.PrepareCurrentUnderLock(lock, caller, nil, time.Unix(30, 0))
	if err != nil {
		t.Fatal(err)
	}
	if p.Successor.PID != caller.PID || p.Successor.NativeHarnessID != caller.NativeHarnessID || p.Successor.WBSessionID == caller.WBSessionID {
		t.Fatalf("not one fresh identity in the same chat: %#v", p)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.ContinuationDigest != sessionmove.DigestBytes(raw) {
		t.Fatal("context digest differs")
	}
	if _, err := s.AcknowledgeCurrentUnderLock(lock, caller, sessionmove.DigestBytes([]byte("wrong"))); err == nil {
		t.Fatal("wrong acknowledgment accepted")
	}
	if _, err := s.AcknowledgeCurrentUnderLock(lock, caller, p.ContinuationDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcknowledgeCurrentUnderLock(lock, p.Successor, p.ContinuationDigest); err != nil {
		t.Fatal(err)
	}
	retry, retryPath, err := s.PrepareCurrentUnderLock(lock, p.Successor, nil, time.Unix(60, 0))
	if err != nil || retry.Digest() != p.Digest() || retryPath != path {
		t.Fatalf("retry changed immutable preparation: %#v %v", retry, err)
	}
	if _, _, err := s.PrepareLocalUnderLock(lock, time.Now()); err == nil {
		t.Fatal("launcher stole current route")
	}
	other := caller
	other.NativeHarnessID = "another-chat"
	if _, _, err := s.PrepareCurrentUnderLock(lock, other, nil, time.Now()); err == nil {
		t.Fatal("another caller stole route")
	}
}

func TestCurrentPickupRecoversOnlyProvenNeverStartedPlan(t *testing.T) {
	t.Parallel()
	for _, evidence := range []string{"plan-only", "attempt.json", "release.json", "started.json", "wrong-plan"} {
		t.Run(evidence, func(t *testing.T) {
			s, lock, caller := currentFixture(t)
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
			launchDir := filepath.Join(s.Root, lock.parkID, "launch")
			if err := os.Mkdir(launchDir, 0o700); err != nil {
				t.Fatal(err)
			}
			digest := authority.AggregateDigest
			if evidence == "wrong-plan" {
				digest = string(sessionmove.DigestBytes([]byte("foreign")))
			}
			plan, _ := json.Marshal(map[string]any{"handoff_id": lock.parkID, "request_digest": digest, "predecessor_wb_session_id": caller.WBSessionID, "successor_wb_session_id": authority.SuccessorWBSessionID})
			if err := os.WriteFile(filepath.Join(launchDir, "plan.json"), plan, 0o600); err != nil {
				t.Fatal(err)
			}
			if evidence != "plan-only" && evidence != "wrong-plan" {
				if err := os.WriteFile(filepath.Join(launchDir, evidence), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err = s.PrepareCurrentUnderLock(lock, caller, nil, time.Unix(30, 0))
			if (err == nil) != (evidence == "plan-only") {
				t.Fatalf("evidence %s: %v", evidence, err)
			}
			retained, readErr := os.ReadFile(filepath.Join(launchDir, "plan.json"))
			if readErr != nil || string(retained) != string(plan) {
				t.Fatal("immutable old launch plan changed")
			}
		})
	}
}
