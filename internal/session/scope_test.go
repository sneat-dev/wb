package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionterminal"
)

func sharedScopeFixture(t *testing.T) (string, Record, Record) {
	t.Helper()
	root := t.TempDir()
	peer, err := Register(root, Record{PID: os.Getpid(), Runtime: "codex", NativeHarnessID: "native-peer", Model: Unknown})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := registerCallerWithObservation(root, Record{PID: os.Getpid(), Runtime: "codex", NativeHarnessID: "native-caller", Model: Unknown}, func(int, string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if caller.Scope == "" || caller.WBSessionID == peer.WBSessionID || caller.ProcessStartedAt.IsZero() || !caller.ProcessStartedAt.Equal(peer.ProcessStartedAt) {
		t.Fatalf("native chats not isolated: %#v %#v", caller, peer)
	}
	return root, caller, peer
}

func TestNativeScopesKeepSharedProcessChatsIndependentThroughPickup(t *testing.T) {
	t.Parallel()
	root, caller, peer := sharedScopeFixture(t)
	peerPath := recordPath(root, peer.PID)
	before, err := os.ReadFile(peerPath)
	if err != nil {
		t.Fatal(err)
	}
	parked, err := MarkRecordParked(root, caller, "park-scoped-source")
	if err != nil {
		t.Fatal(err)
	}
	if parked.Scope != caller.Scope {
		t.Fatal("park lost chat namespace")
	}
	if view := sessionView(root, peer); view.State != StateLive {
		t.Fatalf("peer lifecycle changed: %#v", view)
	}
	successor := caller
	successor.WBSessionID = "wbs-scoped-successor"
	successor.PredecessorWBSessionID = caller.WBSessionID
	successor.HandoffID = "park-scoped-source"
	successor.StartedAt = caller.StartedAt.Add(time.Second)
	if err := WithCurrentTransition(root, caller, successor, func(commit func() error) error {
		if err := commit(); err != nil {
			return err
		}
		_, err := MarkSourceResumed(root, caller, "park-scoped-source", successor.WBSessionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := WithCurrentTransition(root, caller, successor, func(commit func() error) error { return commit() }); err != nil {
		t.Fatal(err)
	}
	for _, record := range []Record{successor, peer} {
		resolved, ok, err := LookupCallerIdentity(root, record.PID, record.Runtime, record.NativeHarnessID)
		if err != nil || !ok || resolved.WBSessionID != record.WBSessionID {
			t.Fatalf("qualified lookup: %#v %v", resolved, err)
		}
	}
	successor.Model = "updated-model"
	updated, err := registerCallerWithObservation(root, successor, func(int, string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if updated.WBSessionID != successor.WBSessionID || updated.Scope != successor.Scope {
		t.Fatal("metadata registration changed custody identity")
	}
	if _, err := Prune(root); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(peerPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("peer bytes changed: %v", err)
	}
	views, err := List(root)
	if err != nil || len(views) != 2 {
		t.Fatalf("native rows missing: %#v %v", views, err)
	}
	for _, view := range views {
		if view.State != StateLive {
			t.Fatalf("successor inherited source lifecycle: %#v", view)
		}
	}
	// Departing again fences replay of the old completed checkpoint before any
	// registry or worktree attachment can be repeated.
	if _, err := MarkRecordParked(root, updated, "park-scoped-newer"); err != nil {
		t.Fatal(err)
	}
	if err := RequireNoDeparture(root, updated); err == nil || !strings.Contains(err.Error(), "park-scoped-newer") {
		t.Fatalf("old checkpoint did not point to newer departure: %v", err)
	}
	if err := RequireNoDeparture(root, peer); err != nil {
		t.Fatalf("unrelated peer departure: %v", err)
	}
	after, _ = os.ReadFile(peerPath)
	if !bytes.Equal(before, after) {
		t.Fatal("departure touched peer")
	}
}

func TestSameNativeLegacyRegistrationStaysInOriginalNamespace(t *testing.T) {
	t.Parallel()
	root, _, peer := sharedScopeFixture(t)
	before := peer.WBSessionID
	updated, err := registerCallerWithObservation(root, Record{PID: peer.PID, Runtime: peer.Runtime, NativeHarnessID: peer.NativeHarnessID, Model: "new-model"}, func(int, string) bool { return true })
	if err != nil || updated.Scope != "" || updated.WBSessionID != before {
		t.Fatalf("own legacy migration/custody change: %#v %v", updated, err)
	}
	views, err := List(root)
	if err != nil || len(views) != 2 {
		t.Fatalf("duplicate legacy row: %d %v", len(views), err)
	}
	if _, err := registerCallerWithObservation(root, Record{PID: peer.PID, Runtime: "codex", NativeHarnessID: "foreign", WBSessionID: before}, func(int, string) bool { return true }); err == nil {
		t.Fatal("foreign chat inherited legacy WB identity")
	}
}

func TestConcurrentFirstNativeRegistrationHasOneLogicalWinner(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const count = 8
	results := make(chan Record, count)
	errs := make(chan error, count)
	var wait sync.WaitGroup
	for range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			record, err := registerCallerWithObservation(root, Record{PID: os.Getpid(), Runtime: "codex", NativeHarnessID: "same-native", Model: Unknown}, func(int, string) bool { return true })
			results <- record
			errs <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	winner := ""
	for record := range results {
		if winner == "" {
			winner = record.WBSessionID
		}
		if record.WBSessionID != winner {
			t.Fatal("concurrent native registrations created different identities")
		}
	}
	views, err := List(root)
	if err != nil || len(views) != 1 {
		t.Fatalf("duplicate live chat rows: %#v %v", views, err)
	}
}

func TestRecordQualifiedLegacyLookupUsesEffectiveNativeAlias(t *testing.T) {
	t.Parallel()
	root, _, peer := sharedScopeFixture(t)
	expected := peer
	expected.NativeHarnessID = ""
	expected.AgentID = "old-native"
	if _, _, err := LookupRecordExact(root, expected); err == nil {
		t.Fatal("legacy AgentID mismatch accepted same PID/WBID")
	}
	expected = peer
	expected.AgentID = "contradictory"
	if _, _, err := LookupRecordExact(root, expected); err == nil {
		t.Fatal("contradictory expected aliases accepted")
	}
}

func TestNativeScopeRejectsForgedNamespaceAndLinkedDirectories(t *testing.T) {
	t.Parallel()
	root, caller, _ := sharedScopeFixture(t)
	forged := caller
	forged.NativeHarnessID = "another-native"
	if _, _, err := LookupRecordExact(root, forged); err == nil {
		t.Fatal("forged native scope accepted")
	}
	for _, which := range []string{"index", "namespace", "record", "lifecycle"} {
		t.Run(which, func(t *testing.T) {
			root, caller, _ := sharedScopeFixture(t)
			var path string
			switch which {
			case "index":
				path = filepath.Join(root, scopeIndexName)
			case "namespace":
				path = filepath.Join(root, scopeIndexName, caller.Scope)
			case "record":
				path = filepath.Join(root, scopeIndexName, caller.Scope, scopeRecordName(caller.PID))
			case "lifecycle":
				if _, err := MarkRecordParked(root, caller, "park-linked"); err != nil {
					t.Fatal(err)
				}
				name, _ := scopedLifecycleName(caller, "parked")
				path = filepath.Join(root, scopeIndexName, caller.Scope, "lifecycle", name)
			}
			target := filepath.Join(t.TempDir(), "saved")
			if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if which == "lifecycle" {
				if err := RequireNoDeparture(root, caller); err == nil {
					t.Fatal("linked lifecycle accepted")
				}
			} else if _, _, err := LookupRecordExact(root, caller); err == nil {
				t.Fatal("linked native namespace accepted")
			}
		})
	}
}

func TestNativeScopeRoundTripAndLegacyCanonicalOmission(t *testing.T) {
	t.Parallel()
	_, caller, peer := sharedScopeFixture(t)
	for _, record := range []Record{caller, peer} {
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		var restored Record
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		if restored != record {
			t.Fatal("record identity changed across serialization")
		}
		if record.Scope == "" && bytes.Contains(raw, []byte(`"scope"`)) {
			t.Fatal("legacy canonical scope was added")
		}
	}
	for _, native := range []string{"", " spaced", "bad\ntext", "bad\x00text", string([]byte{0xff})} {
		if _, err := NativeScope("codex", native); err == nil {
			t.Fatalf("unsafe native scope accepted %q", native)
		}
	}
}

func TestSharedHarnessRejectsMissingNativeOrWrongRuntimeBeforePeerMutation(t *testing.T) {
	t.Parallel()
	root, _, peer := sharedScopeFixture(t)
	path := recordPath(root, peer.PID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []Record{{PID: peer.PID, Runtime: "codex"}, {PID: peer.PID, Runtime: "claude-code", NativeHarnessID: "foreign"}} {
		if _, err := registerCallerWithObservation(root, record, func(int, string) bool { return true }); err == nil {
			t.Fatalf("unbound shared harness accepted: %#v", record)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refusal changed peer record: %v", err)
	}
}

func TestExistingLiveScopedChatWinsOverLaterLegacyRegistrationWithoutRewritingIt(t *testing.T) {
	t.Parallel()
	root, caller, peer := sharedScopeFixture(t)
	legacy := caller
	legacy.Scope = ""
	legacy.WBSessionID = "wbs-later-legacy"
	// An older WB can still publish the physical-PID row during an upgrade.
	if _, err := Register(root, legacy); err != nil {
		t.Fatal(err)
	}
	path := recordPath(root, peer.PID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []string{"", caller.WBSessionID} {
		updated, err := registerCallerWithObservation(root, Record{PID: caller.PID, Runtime: caller.Runtime, NativeHarnessID: caller.NativeHarnessID, WBSessionID: explicit, Model: "updated-model"}, func(int, string) bool { return true })
		if err != nil || updated.Scope != caller.Scope || updated.WBSessionID != caller.WBSessionID {
			t.Fatalf("scoped authority replaced by later legacy row: %#v %v", updated, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("registration rewrote legacy bytes: %v", err)
	}
	resolved, live, err := LookupCallerIdentity(root, caller.PID, caller.Runtime, caller.NativeHarnessID)
	if err != nil || !live || resolved.Scope != caller.Scope || resolved.WBSessionID != caller.WBSessionID {
		t.Fatalf("current caller did not retain scoped identity: %#v %v", resolved, err)
	}
	if _, err := registerCallerWithObservation(root, Record{PID: caller.PID, Runtime: caller.Runtime, NativeHarnessID: caller.NativeHarnessID, WBSessionID: legacy.WBSessionID}, func(int, string) bool { return true }); err == nil {
		t.Fatal("public re-registration replaced live logical scope")
	}
}

func TestUnboundCodexProcessDefersEveryRootAndChildUntilOwnedNativeBinding(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	terminal := sessionterminal.Identity{Kind: "herdr", Socket: filepath.Join(root, "owned.sock"), WorkspaceID: "w1", PaneID: "w1:p1"}
	pending, err := Register(root, Record{PID: os.Getpid(), Runtime: "codex", Model: Unknown, Terminal: terminal, WBSessionID: "wbs-owned-preallocated", PredecessorWBSessionID: "wbs-owned-source", HandoffID: "handoff-owned"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(recordPath(root, pending.PID))
	if err != nil {
		t.Fatal(err)
	}
	observed := func(int, string) bool { return true }
	for _, native := range []string{"native-root", "native-child", "native-second-root"} {
		for _, declaredWBID := range []string{"", pending.WBSessionID, "wbs-forged-preallocation"} {
			candidate := pending
			candidate.NativeHarnessID = native
			candidate.WBSessionID = declaredWBID
			// Public lineage/terminal metadata is not proof that this native root
			// owns the unknown-native preallocated physical child.
			if _, err := registerCallerWithObservation(root, candidate, observed); !errors.Is(err, ErrNativeBindingPending) {
				t.Fatalf("unbound actor %s adopted or allocated identity: %v", native, err)
			}
			if after, err := os.ReadFile(recordPath(root, pending.PID)); err != nil || !bytes.Equal(before, after) {
				t.Fatal("pending refusal rewrote the original registration")
			}
			views, err := List(root)
			if err != nil || len(views) != 1 {
				t.Fatalf("pending refusal allocated a peer row: %#v %v", views, err)
			}
		}
	}
	// This is the existing plan-owned observation boundary, not a caller flag.
	bound, err := BindNativeHarness(root, pending, "native-root")
	if err != nil {
		t.Fatal(err)
	}
	candidate := bound
	candidate.Model = "observed-known-model"
	registered, err := registerCallerWithObservation(root, candidate, observed)
	if err != nil || registered.WBSessionID != pending.WBSessionID || registered.Scope != "" {
		t.Fatalf("post-observation startup retry changed preallocation: %#v %v", registered, err)
	}
	rootBytes, err := os.ReadFile(recordPath(root, pending.PID))
	if err != nil {
		t.Fatal(err)
	}
	for _, native := range []string{"native-child", "native-second-root"} {
		child := Record{PID: pending.PID, Runtime: "codex", Model: Unknown, NativeHarnessID: native}
		inherited := child
		inherited.WBSessionID = pending.WBSessionID
		if _, err := registerCallerWithObservation(root, inherited, observed); err == nil {
			t.Fatal("foreign root inherited preallocated identity")
		}
		child, err = registerCallerWithObservation(root, child, observed)
		if err != nil || child.Scope == "" || child.WBSessionID == pending.WBSessionID {
			t.Fatalf("bound peer did not get independent native scope: %#v %v", child, err)
		}
	}
	if after, err := os.ReadFile(recordPath(root, pending.PID)); err != nil || !bytes.Equal(rootBytes, after) {
		t.Fatal("child/root scope registration mutated original preallocated row")
	}
}

func TestCurrentCallerDepartureExceptionRequiresExactSourceCheckpointAndWinner(t *testing.T) {
	root, source, peer := sharedScopeFixture(t)
	if _, err := MarkRecordParked(root, source, "park-source-exception"); err != nil {
		t.Fatal(err)
	}
	if err := RequireCurrentCallerAvailable(root, source, source, "park-source-exception", "wbs-own-successor"); err != nil {
		t.Fatalf("exact parked source refused: %v", err)
	}
	for _, test := range []struct {
		name             string
		caller, expected Record
		park, successor  string
	}{
		{"different checkpoint", source, source, "park-other", "wbs-own-successor"},
		{"foreign source", source, peer, "park-source-exception", "wbs-own-successor"},
		{"changed metadata", source, func() Record { r := source; r.Model += "-changed"; return r }(), "park-source-exception", "wbs-own-successor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := RequireCurrentCallerAvailable(root, test.caller, test.expected, test.park, test.successor); err == nil {
				t.Fatal("departure exception accepted a different immutable source")
			}
		})
	}
	if _, err := MarkSourceResumed(root, source, "park-source-exception", "wbs-own-successor"); err != nil {
		t.Fatal(err)
	}
	if err := RequireCurrentCallerAvailable(root, source, source, "park-source-exception", "wbs-own-successor"); err != nil {
		t.Fatalf("exact completion retry refused: %v", err)
	}
	if err := RequireCurrentCallerAvailable(root, source, source, "park-source-exception", "wbs-foreign-successor"); err == nil {
		t.Fatal("different completed winner received source exception")
	}
}
