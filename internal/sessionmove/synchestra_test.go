package sessionmove

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSynchestraRouteAndDispatchIdentityAreImmutableAndReplayable(t *testing.T) {
	store, request, digest, _ := admittedRouteRequest(t, false)
	route := Route{
		HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "hetzner-vm1"},
	}
	if _, replay, err := store.SaveRoute(route); err != nil || replay {
		t.Fatalf("SaveRoute = replay %t err %v", replay, err)
	}
	identity := SynchestraDispatch{
		HandoffID: request.HandoffID, RequestDigest: digest, Runner: "hetzner-vm1",
		InvocationID: request.HandoffID, Handler: SynchestraSessionAcceptHandler, DispatchID: "dsp_handoff_123",
	}
	first, replay, err := store.SaveSynchestraDispatch(identity)
	if err != nil {
		t.Fatal(err)
	}
	identity.SchemaVersion = SynchestraDispatchSchemaVersion
	if replay || !reflect.DeepEqual(first, identity) {
		t.Fatalf("first identity = %#v replay=%t, want %#v", first, replay, identity)
	}
	loaded, err := store.LoadSynchestraDispatch(request.HandoffID)
	if err != nil || !reflect.DeepEqual(loaded, first) {
		t.Fatalf("LoadSynchestraDispatch = %#v err=%v", loaded, err)
	}
	again, replay, err := store.SaveSynchestraDispatch(identity)
	if err != nil || !replay || !reflect.DeepEqual(again, first) {
		t.Fatalf("replayed identity = %#v replay=%t err=%v", again, replay, err)
	}
	path := filepath.Join(store.Root, request.HandoffID, synchestraDispatchFileName)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("dispatch identity mode: info=%v err=%v", info, err)
	}
}

func TestSynchestraDispatchIdentityRefusesRouteOrReplayDrift(t *testing.T) {
	store, request, digest, _ := admittedRouteRequest(t, false)
	if _, _, err := store.SaveRoute(Route{
		HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "hetzner-vm1"},
	}); err != nil {
		t.Fatal(err)
	}
	valid := SynchestraDispatch{
		HandoffID: request.HandoffID, RequestDigest: digest, Runner: "hetzner-vm1",
		InvocationID: request.HandoffID, Handler: SynchestraSessionAcceptHandler, DispatchID: "dsp_first",
	}
	if _, _, err := store.SaveSynchestraDispatch(valid); err != nil {
		t.Fatal(err)
	}
	conflict := valid
	conflict.DispatchID = "dsp_other"
	if _, _, err := store.SaveSynchestraDispatch(conflict); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("dispatch conflict error = %v", err)
	}
	if loaded, err := store.LoadSynchestraDispatch(request.HandoffID); err != nil || loaded.DispatchID != valid.DispatchID {
		t.Fatalf("first identity was not preserved: %#v err=%v", loaded, err)
	}
}

func TestSynchestraDispatchIdentityReadsAreDescriptorSafeAndStrict(t *testing.T) {
	fixture := func(t *testing.T) (Store, Request, SynchestraDispatch, []byte, string) {
		t.Helper()
		store, request, digest, _ := admittedRouteRequest(t, false)
		if _, _, err := store.SaveRoute(Route{
			HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
			Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "hetzner-vm1"},
		}); err != nil {
			t.Fatal(err)
		}
		identity, _, err := store.SaveSynchestraDispatch(SynchestraDispatch{
			HandoffID: request.HandoffID, RequestDigest: digest, Runner: "hetzner-vm1",
			InvocationID: request.HandoffID, Handler: SynchestraSessionAcceptHandler, DispatchID: "dsp_safe",
		})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := marshalJSON(identity)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(store.Root, request.HandoffID, synchestraDispatchFileName)
		return store, request, identity, raw, path
	}

	t.Run("symlink", func(t *testing.T) {
		store, request, _, raw, path := fixture(t)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		external := filepath.Join(t.TempDir(), "dispatch.json")
		if err := os.WriteFile(external, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, path); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadSynchestraDispatch(request.HandoffID); err == nil {
			t.Fatal("LoadSynchestraDispatch followed a symlink")
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		store, request, _, _, path := fixture(t)
		if err := os.Link(path, filepath.Join(t.TempDir(), "dispatch-alias.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadSynchestraDispatch(request.HandoffID); err == nil {
			t.Fatal("LoadSynchestraDispatch accepted multiply-linked state")
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		store, request, _, raw, path := fixture(t)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		raw = bytes.Replace(raw, []byte("{\n"), []byte("{\n  \"unknown\": true,\n"), 1)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadSynchestraDispatch(request.HandoffID); err == nil {
			t.Fatal("LoadSynchestraDispatch accepted an unknown field")
		}
	})
}
