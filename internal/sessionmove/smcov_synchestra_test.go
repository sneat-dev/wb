package sessionmove

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func smCovSynchRunnerRoute(request Request, digest Digest) Route {
	return Route{
		SchemaVersion: RouteSchemaVersion, HandoffID: request.HandoffID, RequestDigest: digest,
		TargetMachine: request.TargetMachine, Courier: CourierSynchestra,
		Synchestra: &SynchestraConfig{Runner: "hetzner-vm1"},
	}
}

func smCovSynchRunnerIdentity(request Request, digest Digest) SynchestraDispatch {
	return SynchestraDispatch{
		SchemaVersion: SynchestraDispatchSchemaVersion,
		HandoffID:     request.HandoffID, RequestDigest: digest, Runner: "hetzner-vm1",
		InvocationID: request.HandoffID, Handler: SynchestraSessionAcceptHandler, DispatchID: "dsp_smCov_1",
	}
}

// smCovSynchRunnerReady admits one request and publishes its exact synchestra route.
func smCovSynchRunnerReady(t *testing.T, request Request) (Store, Digest, Route) {
	t.Helper()
	store, digest := smCovRouteAdmit(t, request)
	route := smCovSynchRunnerRoute(request, digest)
	if _, replay, err := store.SaveRoute(route); err != nil || replay {
		t.Fatalf("SaveRoute = replay %t, error %v", replay, err)
	}
	return store, digest, route
}

func smCovSynchRunnerDispatchPath(store Store, request Request) string {
	return filepath.Join(store.Root, request.HandoffID, synchestraDispatchFileName)
}

func TestSmCovSynchSaveDispatchValidationAndReplay(t *testing.T) {
	t.Run("missing handoff", func(t *testing.T) {
		request := validRequest()
		raw, err := EncodeRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		digest := DigestBytes(raw)
		store := NewStore(filepath.Join(t.TempDir(), DirName))
		if _, _, err := store.SaveSynchestraDispatch(smCovSynchRunnerIdentity(request, digest)); err == nil {
			t.Fatal("SaveSynchestraDispatch accepted an absent handoff")
		}
	})

	t.Run("missing route", func(t *testing.T) {
		store, request, digest, _ := admittedRouteRequest(t, false)
		if _, _, err := store.SaveSynchestraDispatch(smCovSynchRunnerIdentity(request, digest)); err == nil {
			t.Fatal("SaveSynchestraDispatch accepted a handoff without a courier route")
		}
	})

	t.Run("route is not the synchestra courier", func(t *testing.T) {
		store, request, digest, _ := admittedRouteRequest(t, false)
		if _, _, err := store.SaveRoute(validRoute(request, digest)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SaveSynchestraDispatch(smCovSynchRunnerIdentity(request, digest)); err == nil || !strings.Contains(err.Error(), "does not use the synchestra courier") {
			t.Fatalf("SaveSynchestraDispatch(ssh route) error = %v", err)
		}
	})

	for _, test := range []struct {
		name    string
		mutate  func(*SynchestraDispatch)
		wantErr error
		want    string
	}{
		{"digest mismatch", func(identity *SynchestraDispatch) { identity.RequestDigest = DigestBytes([]byte("other request")) }, ErrHandoffConflict, ""},
		{"runner mismatch", func(identity *SynchestraDispatch) { identity.Runner = "other-runner" }, ErrHandoffConflict, ""},
		{"invocation mismatch", func(identity *SynchestraDispatch) { identity.InvocationID = "other-invocation" }, ErrHandoffConflict, ""},
		{"handler mismatch", func(identity *SynchestraDispatch) { identity.Handler = "wb.session.other.v1" }, ErrHandoffConflict, ""},
		{"dispatch id malformed", func(identity *SynchestraDispatch) { identity.DispatchID = "bad dispatch id" }, nil, "dispatch_id must be"},
		{"dispatch id empty", func(identity *SynchestraDispatch) { identity.DispatchID = "" }, nil, "dispatch_id must be"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, digest, _ := smCovSynchRunnerReady(t, validRequest())
			request := validRequest()
			identity := smCovSynchRunnerIdentity(request, digest)
			test.mutate(&identity)
			_, _, err := store.SaveSynchestraDispatch(identity)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("SaveSynchestraDispatch error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SaveSynchestraDispatch error = %v, want mention of %q", err, test.want)
			}
			if _, statErr := os.Stat(smCovSynchRunnerDispatchPath(store, request)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rejected identity wrote durable state: %v", statErr)
			}
		})
	}

	t.Run("schema version is forced", func(t *testing.T) {
		store, digest, _ := smCovSynchRunnerReady(t, validRequest())
		request := validRequest()
		identity := smCovSynchRunnerIdentity(request, digest)
		identity.SchemaVersion = 0
		first, replay, err := store.SaveSynchestraDispatch(identity)
		if err != nil || replay {
			t.Fatalf("SaveSynchestraDispatch(schema zero input) = replay %t, error %v", replay, err)
		}
		if first.SchemaVersion != SynchestraDispatchSchemaVersion {
			t.Fatalf("persisted schema version = %d, want %d", first.SchemaVersion, SynchestraDispatchSchemaVersion)
		}
	})

	t.Run("different existing dispatch", func(t *testing.T) {
		store, digest, _ := smCovSynchRunnerReady(t, validRequest())
		request := validRequest()
		if _, _, err := store.SaveSynchestraDispatch(smCovSynchRunnerIdentity(request, digest)); err != nil {
			t.Fatal(err)
		}
		conflict := smCovSynchRunnerIdentity(request, digest)
		conflict.DispatchID = "dsp_smCov_other"
		if _, _, err := store.SaveSynchestraDispatch(conflict); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("SaveSynchestraDispatch(conflict) error = %v, want ErrHandoffConflict", err)
		}
		loaded, err := store.LoadSynchestraDispatch(request.HandoffID)
		if err != nil || loaded.DispatchID != "dsp_smCov_1" {
			t.Fatalf("durable dispatch changed: %#v, error %v", loaded, err)
		}
	})

	t.Run("tampered existing dispatch file", func(t *testing.T) {
		store, digest, _ := smCovSynchRunnerReady(t, validRequest())
		request := validRequest()
		if _, _, err := store.SaveSynchestraDispatch(smCovSynchRunnerIdentity(request, digest)); err != nil {
			t.Fatal(err)
		}
		path := smCovSynchRunnerDispatchPath(store, request)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{not-json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SaveSynchestraDispatch(smCovSynchRunnerIdentity(request, digest)); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("SaveSynchestraDispatch(tampered) error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("identical replay", func(t *testing.T) {
		store, digest, _ := smCovSynchRunnerReady(t, validRequest())
		request := validRequest()
		identity := smCovSynchRunnerIdentity(request, digest)
		first, replay, err := store.SaveSynchestraDispatch(identity)
		if err != nil || replay {
			t.Fatalf("first dispatch = replay %t, error %v", replay, err)
		}
		if first.DispatchID != identity.DispatchID || first.Handler != SynchestraSessionAcceptHandler {
			t.Fatalf("first dispatch = %#v", first)
		}
		info, err := os.Stat(smCovSynchRunnerDispatchPath(store, request))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("dispatch file mode: info=%v error=%v", info, err)
		}
		again, replay, err := store.SaveSynchestraDispatch(identity)
		if err != nil || !replay || !reflect.DeepEqual(again, first) {
			t.Fatalf("replayed dispatch = %#v, replay %t, error %v", again, replay, err)
		}
	})

	t.Run("oversized identity", func(t *testing.T) {
		request := validRequest()
		store, digest := smCovRouteAdmit(t, request)
		route := smCovSynchRunnerRoute(request, digest)
		route.Synchestra.Runner = ""
		emptyRaw, err := marshalJSON(route)
		if err != nil {
			t.Fatal(err)
		}
		if len(emptyRaw) >= maxRouteBytes {
			t.Fatalf("route fixture overhead %d already reaches the route bound", len(emptyRaw))
		}
		route.Synchestra.Runner = strings.Repeat("r", maxRouteBytes-len(emptyRaw))
		routeRaw, err := marshalJSON(route)
		if err != nil {
			t.Fatal(err)
		}
		if len(routeRaw) != maxRouteBytes {
			t.Fatalf("route fixture = %d bytes, want exactly %d", len(routeRaw), maxRouteBytes)
		}
		if _, replay, err := store.SaveRoute(route); err != nil || replay {
			t.Fatalf("SaveRoute(long runner) = replay %t, error %v", replay, err)
		}
		identity := smCovSynchRunnerIdentity(request, digest)
		identity.Runner = route.Synchestra.Runner
		identityRaw, err := marshalJSON(identity)
		if err != nil {
			t.Fatal(err)
		}
		if len(identityRaw) <= maxSynchestraDispatchBytes {
			t.Fatalf("dispatch fixture = %d bytes, does not exceed %d", len(identityRaw), maxSynchestraDispatchBytes)
		}
		if _, _, err := store.SaveSynchestraDispatch(identity); err == nil || !strings.Contains(err.Error(), "synchestra dispatch identity exceeds") {
			t.Fatalf("SaveSynchestraDispatch(oversized) error = %v", err)
		}
		if _, statErr := os.Stat(smCovSynchRunnerDispatchPath(store, request)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("oversized identity wrote durable state: %v", statErr)
		}
	})

	t.Run("dispatch file is a symlink", func(t *testing.T) {
		store, digest, _ := smCovSynchRunnerReady(t, validRequest())
		request := validRequest()
		identity, _, err := store.SaveSynchestraDispatch(smCovSynchRunnerIdentity(request, digest))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := marshalJSON(identity)
		if err != nil {
			t.Fatal(err)
		}
		path := smCovSynchRunnerDispatchPath(store, request)
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
		if _, _, err := store.SaveSynchestraDispatch(smCovSynchRunnerIdentity(request, digest)); err == nil {
			t.Fatal("SaveSynchestraDispatch accepted a symlinked dispatch identity")
		}
	})
}

func TestSmCovSynchLoadDispatchArtifacts(t *testing.T) {
	t.Run("missing handoff", func(t *testing.T) {
		request := validRequest()
		store := NewStore(filepath.Join(t.TempDir(), DirName))
		if _, err := store.LoadSynchestraDispatch(request.HandoffID); err == nil {
			t.Fatal("LoadSynchestraDispatch accepted an absent handoff")
		}
	})

	t.Run("missing route", func(t *testing.T) {
		store, request, _, _ := admittedRouteRequest(t, false)
		if _, err := store.LoadSynchestraDispatch(request.HandoffID); err == nil {
			t.Fatal("LoadSynchestraDispatch accepted a handoff without a route")
		}
	})

	t.Run("missing dispatch file", func(t *testing.T) {
		store, _, _ := smCovSynchRunnerReady(t, validRequest())
		request := validRequest()
		if _, err := store.LoadSynchestraDispatch(request.HandoffID); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("LoadSynchestraDispatch error = %v, want os.ErrNotExist", err)
		}
	})

	t.Run("malformed dispatch file", func(t *testing.T) {
		store, digest, _ := smCovSynchRunnerReady(t, validRequest())
		request := validRequest()
		if _, _, err := store.SaveSynchestraDispatch(smCovSynchRunnerIdentity(request, digest)); err != nil {
			t.Fatal(err)
		}
		path := smCovSynchRunnerDispatchPath(store, request)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{not-json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadSynchestraDispatch(request.HandoffID); err == nil || !strings.Contains(err.Error(), "decode synchestra dispatch identity") {
			t.Fatalf("LoadSynchestraDispatch(malformed) error = %v", err)
		}
	})

	t.Run("tampered identity", func(t *testing.T) {
		store, digest, _ := smCovSynchRunnerReady(t, validRequest())
		request := validRequest()
		identity, _, err := store.SaveSynchestraDispatch(smCovSynchRunnerIdentity(request, digest))
		if err != nil {
			t.Fatal(err)
		}
		identity.Runner = "drifted-runner"
		raw, err := marshalJSON(identity)
		if err != nil {
			t.Fatal(err)
		}
		path := smCovSynchRunnerDispatchPath(store, request)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadSynchestraDispatch(request.HandoffID); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("LoadSynchestraDispatch(tampered) error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("happy path", func(t *testing.T) {
		store, digest, _ := smCovSynchRunnerReady(t, validRequest())
		request := validRequest()
		saved, _, err := store.SaveSynchestraDispatch(smCovSynchRunnerIdentity(request, digest))
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := store.LoadSynchestraDispatch(request.HandoffID)
		if err != nil || !reflect.DeepEqual(loaded, saved) {
			t.Fatalf("LoadSynchestraDispatch = %#v, error %v", loaded, err)
		}
	})
}

func TestSmCovSynchDecodeAndValidateDispatch(t *testing.T) {
	request := validRequest()
	requestRaw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(requestRaw)
	route := smCovSynchRunnerRoute(request, digest)
	identity := smCovSynchRunnerIdentity(request, digest)

	t.Run("malformed JSON", func(t *testing.T) {
		if _, err := decodeAndValidateSynchestraDispatch([]byte("{not-json\n"), request, digest, route); err == nil || !strings.Contains(err.Error(), "decode synchestra dispatch identity") {
			t.Fatalf("decodeAndValidateSynchestraDispatch error = %v", err)
		}
	})

	t.Run("schema zero", func(t *testing.T) {
		tampered := identity
		tampered.SchemaVersion = 0
		raw, err := marshalJSON(tampered)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeAndValidateSynchestraDispatch(raw, request, digest, route); err == nil || !strings.Contains(err.Error(), "schema_version") {
			t.Fatalf("decodeAndValidateSynchestraDispatch(schema zero) error = %v", err)
		}
	})

	t.Run("identity mismatch", func(t *testing.T) {
		tampered := identity
		tampered.Handler = "wb.session.other.v1"
		raw, err := marshalJSON(tampered)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeAndValidateSynchestraDispatch(raw, request, digest, route); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("decodeAndValidateSynchestraDispatch(handler mismatch) error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("valid", func(t *testing.T) {
		raw, err := marshalJSON(identity)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeAndValidateSynchestraDispatch(raw, request, digest, route)
		if err != nil || !reflect.DeepEqual(decoded, identity) {
			t.Fatalf("decodeAndValidateSynchestraDispatch = %#v, error %v", decoded, err)
		}
	})
}

func TestSmCovSynchValidateDispatchBranches(t *testing.T) {
	request := validRequest()
	requestRaw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(requestRaw)
	route := smCovSynchRunnerRoute(request, digest)
	base := smCovSynchRunnerIdentity(request, digest)
	if err := validateSynchestraDispatch(base, request, digest, route); err != nil {
		t.Fatalf("valid dispatch rejected: %v", err)
	}

	for _, test := range []struct {
		name    string
		mutate  func(*SynchestraDispatch)
		wantErr error
		want    string
	}{
		{"schema zero", func(identity *SynchestraDispatch) { identity.SchemaVersion = 0 }, nil, "schema_version"},
		{"schema two", func(identity *SynchestraDispatch) { identity.SchemaVersion = 2 }, nil, "schema_version"},
		{"handoff mismatch", func(identity *SynchestraDispatch) { identity.HandoffID = "handoff-other" }, ErrHandoffConflict, ""},
		{"digest mismatch", func(identity *SynchestraDispatch) { identity.RequestDigest = DigestBytes([]byte("other")) }, ErrHandoffConflict, ""},
		{"runner mismatch", func(identity *SynchestraDispatch) { identity.Runner = "other-runner" }, ErrHandoffConflict, ""},
		{"invocation mismatch", func(identity *SynchestraDispatch) { identity.InvocationID = "other-invocation" }, ErrHandoffConflict, ""},
		{"handler mismatch", func(identity *SynchestraDispatch) { identity.Handler = "other-handler" }, ErrHandoffConflict, ""},
		{"dispatch id empty", func(identity *SynchestraDispatch) { identity.DispatchID = "" }, nil, "dispatch_id must be"},
		{"dispatch id option-like", func(identity *SynchestraDispatch) { identity.DispatchID = "-leading-dash" }, nil, "dispatch_id must be"},
		{"dispatch id oversized", func(identity *SynchestraDispatch) { identity.DispatchID = "d" + strings.Repeat("x", 128) }, nil, "dispatch_id must be"},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity := base
			test.mutate(&identity)
			err := validateSynchestraDispatch(identity, request, digest, route)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("validateSynchestraDispatch error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateSynchestraDispatch error = %v, want mention of %q", err, test.want)
			}
		})
	}

	t.Run("route is not the synchestra courier", func(t *testing.T) {
		if err := validateSynchestraDispatch(base, request, digest, validRoute(request, digest)); err == nil || !strings.Contains(err.Error(), "does not use the synchestra courier") {
			t.Fatalf("validateSynchestraDispatch(ssh route) error = %v", err)
		}
	})

	t.Run("route has no runner address", func(t *testing.T) {
		nilRoute := smCovSynchRunnerRoute(request, digest)
		nilRoute.Synchestra = nil
		if err := validateSynchestraDispatch(base, request, digest, nilRoute); err == nil || !strings.Contains(err.Error(), "does not use the synchestra courier") {
			t.Fatalf("validateSynchestraDispatch(nil address) error = %v", err)
		}
	})

	t.Run("dispatch id boundary", func(t *testing.T) {
		identity := base
		identity.DispatchID = "d" + strings.Repeat("x", 127)
		if err := validateSynchestraDispatch(identity, request, digest, route); err != nil {
			t.Fatalf("128-character dispatch id rejected: %v", err)
		}
	})
}
