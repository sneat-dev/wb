package sessionmove

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSaveSuccessorAddressUnderLockPublishesExactReplayableIndex(t *testing.T) {
	store, request, digest, _ := admittedRouteRequest(t, false)
	if _, _, err := store.SaveRoute(validRoute(request, digest)); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	receipt := validReceipt(request, digest)
	if _, _, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, receipt); err != nil {
		t.Fatal(err)
	}
	first, replay, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if replay || first.SchemaVersion != SuccessorAddressSchemaVersion ||
		first.SuccessorWBSessionID != request.SuccessorWBSessionID ||
		first.HandoffID != request.HandoffID || first.RequestDigest != digest ||
		first.TargetWorkLogReference != receipt.TargetWorkLogReference || !reflect.DeepEqual(first.Route, validRoute(request, digest)) {
		t.Fatalf("first successor address = (%#v, replay=%t)", first, replay)
	}
	loaded, err := store.LoadSuccessorAddress(request.SuccessorWBSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, first) {
		t.Fatalf("loaded address = %#v, want %#v", loaded, first)
	}
	underLock, err := store.LoadSuccessorAddressUnderLock(lock, request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(underLock, first) {
		t.Fatalf("under-lock address = %#v, want %#v", underLock, first)
	}
	again, replay, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, receipt)
	if err != nil || !replay || !reflect.DeepEqual(again, first) {
		t.Fatalf("replayed successor address = (%#v, replay=%t, err=%v)", again, replay, err)
	}
	path := filepath.Join(store.Root, successorAddressesDirName, request.SuccessorWBSessionID+".json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("successor address mode: info=%v err=%v", info, err)
	}
}

func TestSuccessorAddressIndexRefusesLinksUnsafeModesAndMismatches(t *testing.T) {
	fixture := func(t *testing.T) (Store, Request, SuccessorAddress, []byte) {
		t.Helper()
		store, request, digest, _ := admittedRouteRequest(t, false)
		if _, _, err := store.SaveRoute(validRoute(request, digest)); err != nil {
			t.Fatal(err)
		}
		lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, validReceipt(request, digest)); err != nil {
			_ = lock.Close()
			t.Fatal(err)
		}
		address, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, validReceipt(request, digest))
		_ = lock.Close()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := marshalJSON(address)
		if err != nil {
			t.Fatal(err)
		}
		return store, request, address, raw
	}

	for _, test := range []struct {
		name    string
		install func(*testing.T, string, []byte)
	}{
		{"symlink", func(t *testing.T, path string, raw []byte) {
			external := filepath.Join(t.TempDir(), "address.json")
			if err := os.WriteFile(external, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink", func(t *testing.T, path string, raw []byte) {
			external := filepath.Join(t.TempDir(), "address.json")
			if err := os.WriteFile(external, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(external, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"unsafe-mode", func(t *testing.T, path string, raw []byte) {
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"oversized", func(t *testing.T, path string, _ []byte) {
			if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxSuccessorAddressBytes+1), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, request, _, raw := fixture(t)
			path := filepath.Join(store.Root, successorAddressesDirName, request.SuccessorWBSessionID+".json")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			test.install(t, path, raw)
			if _, err := store.LoadSuccessorAddress(request.SuccessorWBSessionID); err == nil {
				t.Fatal("LoadSuccessorAddress accepted unsafe address artifact")
			}
		})
	}

	t.Run("mismatched contents", func(t *testing.T) {
		store, request, address, _ := fixture(t)
		path := filepath.Join(store.Root, successorAddressesDirName, request.SuccessorWBSessionID+".json")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		address.TmuxName = "wb-session-other"
		raw, err := marshalJSON(address)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadSuccessorAddress(request.SuccessorWBSessionID); err == nil {
			t.Fatal("LoadSuccessorAddress accepted mismatched deterministic tmux name")
		}
	})

	t.Run("self-consistent forged pointer", func(t *testing.T) {
		store, request, address, _ := fixture(t)
		path := filepath.Join(store.Root, successorAddressesDirName, request.SuccessorWBSessionID+".json")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		address.RequestDigest = DigestBytes([]byte("forged exact request"))
		address.Route.RequestDigest = address.RequestDigest
		sourceReference, err := ParseWorkLogReference(address.SourceWorkLogReference)
		if err != nil {
			t.Fatal(err)
		}
		claimID, err := ExternalHandoffClaimID(address.RequestDigest, address.SuccessorWBSessionID)
		if err != nil {
			t.Fatal(err)
		}
		address.TargetWorkLogReference = (WorkLogReference{EffortID: sourceReference.EffortID, RunID: sourceReference.RunID, ClaimID: claimID}).String()
		raw, err := marshalJSON(address)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadSuccessorAddress(request.SuccessorWBSessionID); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("LoadSuccessorAddress forged-pointer error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("store root symlink", func(t *testing.T) {
		store, request, _, _ := fixture(t)
		alias := filepath.Join(t.TempDir(), "handoffs-link")
		if err := os.Symlink(store.Root, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(alias).LoadSuccessorAddress(request.SuccessorWBSessionID); err == nil {
			t.Fatal("LoadSuccessorAddress followed a store-root symlink")
		}
	})

	t.Run("index directory symlink", func(t *testing.T) {
		store, request, _, _ := fixture(t)
		wrapper := filepath.Join(t.TempDir(), DirName)
		if err := os.Mkdir(wrapper, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(store.Root, successorAddressesDirName), filepath.Join(wrapper, successorAddressesDirName)); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(wrapper).LoadSuccessorAddress(request.SuccessorWBSessionID); err == nil {
			t.Fatal("LoadSuccessorAddress followed an index-directory symlink")
		}
	})
}

func TestSaveSuccessorAddressUnderLockRefusesRootAndHandoffPathSwaps(t *testing.T) {
	for _, swapRoot := range []bool{false, true} {
		name := "handoff"
		if swapRoot {
			name = "root"
		}
		t.Run(name, func(t *testing.T) {
			store, request, digest, raw := admittedRouteRequest(t, false)
			if _, _, err := store.SaveRoute(validRoute(request, digest)); err != nil {
				t.Fatal(err)
			}
			lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lock.Close() }()
			if _, _, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, validReceipt(request, digest)); err != nil {
				t.Fatal(err)
			}
			retained := ""
			if swapRoot {
				retained = store.Root + ".retained"
				if err := os.Rename(store.Root, retained); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(store.Root, request.HandoffID), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				handoff := filepath.Join(store.Root, request.HandoffID)
				retained = handoff + ".retained"
				if err := os.Rename(handoff, retained); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(handoff, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(store.Root, request.HandoffID, requestFileName), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, validReceipt(request, digest)); err == nil || !strings.Contains(err.Error(), "exact admitted") {
				t.Fatalf("SaveSuccessorAddressUnderLock error = %v", err)
			}
			for _, root := range []string{store.Root, retained} {
				if _, err := os.Stat(filepath.Join(root, successorAddressesDirName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("path swap published successor index under %s: %v", root, err)
				}
			}
		})
	}
}

func TestLoadSuccessorAddressUnderLockRefusesRootAndHandoffPathSwaps(t *testing.T) {
	for _, swapRoot := range []bool{false, true} {
		name := "handoff"
		if swapRoot {
			name = "root"
		}
		t.Run(name, func(t *testing.T) {
			store, request, digest, raw := admittedRouteRequest(t, false)
			if _, _, err := store.SaveRoute(validRoute(request, digest)); err != nil {
				t.Fatal(err)
			}
			lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lock.Close() }()
			receipt := validReceipt(request, digest)
			if _, _, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, receipt); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, receipt); err != nil {
				t.Fatal(err)
			}

			if swapRoot {
				if err := os.Rename(store.Root, store.Root+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(store.Root, request.HandoffID), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				handoff := filepath.Join(store.Root, request.HandoffID)
				if err := os.Rename(handoff, handoff+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(handoff, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(store.Root, request.HandoffID, requestFileName), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadSuccessorAddressUnderLock(lock, request.HandoffID, digest); err == nil || !strings.Contains(err.Error(), "exact admitted") {
				t.Fatalf("LoadSuccessorAddressUnderLock error = %v", err)
			}
		})
	}
}

func TestSaveRoutePublishesExactImmutableSSHRouteAndReplays(t *testing.T) {
	store, request, digest, _ := admittedRouteRequest(t, false)
	route := Route{
		HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: CourierSSH, SSH: &SSHConfig{Host: "hetzner-vm1", WBPath: "/home/ai/go/bin/wb"},
	}

	first, replay, err := store.SaveRoute(route)
	if err != nil {
		t.Fatal(err)
	}
	if replay || first.SchemaVersion != RouteSchemaVersion || !reflect.DeepEqual(first, Route{
		SchemaVersion: RouteSchemaVersion, HandoffID: request.HandoffID, RequestDigest: digest,
		TargetMachine: request.TargetMachine, Courier: CourierSSH,
		SSH: &SSHConfig{Host: "hetzner-vm1", WBPath: "/home/ai/go/bin/wb"},
	}) {
		t.Fatalf("first SaveRoute = (%#v, replay=%t)", first, replay)
	}
	wantRaw, err := marshalJSON(first)
	if err != nil {
		t.Fatal(err)
	}
	routePath := filepath.Join(store.Root, request.HandoffID, routeFileName)
	gotRaw, err := os.ReadFile(routePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotRaw, wantRaw) {
		t.Fatalf("route bytes changed:\n got: %q\nwant: %q", gotRaw, wantRaw)
	}
	if info, err := os.Stat(routePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("route mode: info=%v error=%v", info, err)
	}

	again, replay, err := store.SaveRoute(Route{
		HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: CourierSSH, SSH: &SSHConfig{Host: "hetzner-vm1", WBPath: "/home/ai/go/bin/wb"},
	})
	if err != nil || !replay || !reflect.DeepEqual(again, first) {
		t.Fatalf("replayed SaveRoute = (%#v, replay=%t, error=%v)", again, replay, err)
	}
}

func TestSaveRouteRejectsConflictAndPreservesFirstRoute(t *testing.T) {
	store, request, digest, _ := admittedRouteRequest(t, false)
	first := Route{
		HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: CourierSSH, SSH: &SSHConfig{Host: "first-host", WBPath: "/opt/wb"},
	}
	if _, _, err := store.SaveRoute(first); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.SSH = &SSHConfig{Host: "second-host", WBPath: "/opt/wb"}
	if _, replay, err := store.SaveRoute(conflict); !errors.Is(err, ErrHandoffConflict) || replay {
		t.Fatalf("conflicting SaveRoute = replay %t, error %v", replay, err)
	}
	loaded, err := store.LoadRoute(request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SSH.Host != "first-host" || loaded.SSH.WBPath != "/opt/wb" {
		t.Fatalf("route changed after conflict: %#v", loaded)
	}
}

func TestConcurrentIdenticalRoutesPublishOnce(t *testing.T) {
	store, request, digest, _ := admittedRouteRequest(t, false)
	route := validRoute(request, digest)
	const callers = 12
	start := make(chan struct{})
	replays := make(chan bool, callers)
	errorsFound := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, replay, err := store.SaveRoute(route)
			if err != nil {
				errorsFound <- err
				return
			}
			replays <- replay
		}()
	}
	close(start)
	group.Wait()
	close(replays)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("SaveRoute: %v", err)
	}
	created, replayed := 0, 0
	for replay := range replays {
		if replay {
			replayed++
		} else {
			created++
		}
	}
	if created != 1 || replayed != callers-1 {
		t.Fatalf("routes = %d created, %d replayed; want 1 and %d", created, replayed, callers-1)
	}
}

func TestLoadRouteKeepsPersistedAddressAfterConfigChanges(t *testing.T) {
	store, request, digest, _ := admittedRouteRequest(t, false)
	configured := SSHConfig{Host: "old-host", WBPath: "/old/bin/wb"}
	if _, _, err := store.SaveRoute(Route{
		HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: CourierSSH, SSH: &configured,
	}); err != nil {
		t.Fatal(err)
	}

	// This represents wb.yaml being edited after an ambiguous first attempt.
	configured.Host = "new-host"
	configured.WBPath = "/new/bin/wb"
	loaded, err := store.LoadRoute(request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SSH.Host != "old-host" || loaded.SSH.WBPath != "/old/bin/wb" {
		t.Fatalf("persisted route followed changed config: %#v", loaded)
	}
}

func TestRequestBytesReturnsExactAdmittedEncoding(t *testing.T) {
	store, request, digest, admittedRaw := admittedRouteRequest(t, true)
	loaded, loadedDigest, loadedRaw, err := store.RequestBytes(request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != request || loadedDigest != digest || !bytes.Equal(loadedRaw, admittedRaw) {
		t.Fatalf("RequestBytes = request %#v, digest %q, raw %q; want request %#v, digest %q, raw %q",
			loaded, loadedDigest, loadedRaw, request, digest, admittedRaw)
	}
	canonical, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(loadedRaw, canonical) {
		t.Fatal("test fixture did not prove preservation of a noncanonical valid encoding")
	}
}

func TestSaveRouteRefusesRoutesThatDoNotMatchAdmittedRequest(t *testing.T) {
	store, request, digest, _ := admittedRouteRequest(t, false)
	valid := Route{
		HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: CourierSSH, SSH: &SSHConfig{Host: "target"},
	}
	tests := map[string]func(Route) Route{
		"digest": func(route Route) Route {
			route.RequestDigest = DigestBytes([]byte("other request"))
			return route
		},
		"machine":     func(route Route) Route { route.TargetMachine = "other-machine"; return route },
		"courier":     func(route Route) Route { route.Courier = CourierSynchestra; return route },
		"missing ssh": func(route Route) Route { route.SSH = nil; return route },
		"unsafe ssh":  func(route Route) Route { route.SSH = &SSHConfig{Host: "target;touch"}; return route },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := store.SaveRoute(mutate(valid)); err == nil {
				t.Fatal("SaveRoute accepted a route that did not match its admitted request")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(store.Root, request.HandoffID, routeFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid route created durable state: %v", err)
	}
}

func TestLoadRouteRefusesMalformedOrMismatchedArtifacts(t *testing.T) {
	tests := map[string]func(Request, Digest) []byte{
		"malformed": func(Request, Digest) []byte { return []byte("{not-json\n") },
		"unknown field": func(request Request, digest Digest) []byte {
			raw := validRouteBytes(t, request, digest)
			return bytes.Replace(raw, []byte("{\n"), []byte("{\n  \"unknown\": true,\n"), 1)
		},
		"schema": func(request Request, digest Digest) []byte {
			route := validRoute(request, digest)
			route.SchemaVersion++
			return mustRouteJSON(t, route)
		},
		"handoff": func(request Request, digest Digest) []byte {
			route := validRoute(request, digest)
			route.HandoffID = "handoff-other"
			return mustRouteJSON(t, route)
		},
		"digest": func(request Request, _ Digest) []byte {
			route := validRoute(request, DigestBytes([]byte("other")))
			return mustRouteJSON(t, route)
		},
		"machine": func(request Request, digest Digest) []byte {
			route := validRoute(request, digest)
			route.TargetMachine = "other-machine"
			return mustRouteJSON(t, route)
		},
		"unsafe ssh": func(request Request, digest Digest) []byte {
			route := validRoute(request, digest)
			route.SSH.Host = "target;touch"
			return mustRouteJSON(t, route)
		},
		"unsupported courier": func(request Request, digest Digest) []byte {
			route := validRoute(request, digest)
			route.Courier = CourierSynchestra
			route.SSH = nil
			return mustRouteJSON(t, route)
		},
		"missing ssh": func(request Request, digest Digest) []byte {
			route := validRoute(request, digest)
			route.SSH = nil
			return mustRouteJSON(t, route)
		},
	}
	for name, fixture := range tests {
		t.Run(name, func(t *testing.T) {
			store, request, digest, _ := admittedRouteRequest(t, false)
			path := filepath.Join(store.Root, request.HandoffID, routeFileName)
			if err := os.WriteFile(path, fixture(request, digest), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadRoute(request.HandoffID); err == nil {
				t.Fatal("LoadRoute accepted malformed or mismatched durable state")
			}
		})
	}
}

func TestRouteReadsAreBoundedAndRefuseLinks(t *testing.T) {
	t.Run("store root symlink", func(t *testing.T) {
		realStore, request, digest, _ := admittedRouteRequest(t, false)
		if _, _, err := realStore.SaveRoute(validRoute(request, digest)); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(t.TempDir(), "handoffs-link")
		if err := os.Symlink(realStore.Root, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(alias).LoadRoute(request.HandoffID); err == nil {
			t.Fatal("LoadRoute followed a store-root symlink")
		}
	})
	t.Run("handoff directory symlink", func(t *testing.T) {
		realStore, request, digest, _ := admittedRouteRequest(t, false)
		if _, _, err := realStore.SaveRoute(validRoute(request, digest)); err != nil {
			t.Fatal(err)
		}
		wrapperRoot := filepath.Join(t.TempDir(), DirName)
		if err := os.MkdirAll(wrapperRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(realStore.Root, request.HandoffID), filepath.Join(wrapperRoot, request.HandoffID)); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(wrapperRoot).LoadRoute(request.HandoffID); err == nil {
			t.Fatal("LoadRoute followed a handoff-directory symlink")
		}
	})
	t.Run("oversized route", func(t *testing.T) {
		store, request, _, _ := admittedRouteRequest(t, false)
		path := filepath.Join(store.Root, request.HandoffID, routeFileName)
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxRouteBytes+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadRoute(request.HandoffID); err == nil || !strings.Contains(err.Error(), "bounded") {
			t.Fatalf("LoadRoute error = %v, want bounded-file refusal", err)
		}
	})
	t.Run("route symlink", func(t *testing.T) {
		store, request, digest, _ := admittedRouteRequest(t, false)
		external := filepath.Join(t.TempDir(), "external-route.json")
		if err := os.WriteFile(external, validRouteBytes(t, request, digest), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(store.Root, request.HandoffID, routeFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadRoute(request.HandoffID); err == nil {
			t.Fatal("LoadRoute followed a route symlink")
		}
	})
	t.Run("request symlink", func(t *testing.T) {
		store, request, _, admittedRaw := admittedRouteRequest(t, false)
		requestPath := filepath.Join(store.Root, request.HandoffID, requestFileName)
		external := filepath.Join(t.TempDir(), "external-request.json")
		if err := os.WriteFile(external, admittedRaw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(requestPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, requestPath); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := store.RequestBytes(request.HandoffID); err == nil {
			t.Fatal("RequestBytes followed a request symlink")
		}
	})
	t.Run("route hard link", func(t *testing.T) {
		store, request, digest, _ := admittedRouteRequest(t, false)
		if _, _, err := store.SaveRoute(validRoute(request, digest)); err != nil {
			t.Fatal(err)
		}
		routePath := filepath.Join(store.Root, request.HandoffID, routeFileName)
		if err := os.Link(routePath, filepath.Join(t.TempDir(), "route-alias.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadRoute(request.HandoffID); err == nil || !strings.Contains(err.Error(), "single-link") {
			t.Fatalf("LoadRoute error = %v, want hard-link refusal", err)
		}
	})
	t.Run("request hard link", func(t *testing.T) {
		store, request, _, _ := admittedRouteRequest(t, false)
		requestPath := filepath.Join(store.Root, request.HandoffID, requestFileName)
		if err := os.Link(requestPath, filepath.Join(t.TempDir(), "request-alias.json")); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := store.RequestBytes(request.HandoffID); err == nil || !strings.Contains(err.Error(), "single-link") {
			t.Fatalf("RequestBytes error = %v, want hard-link refusal", err)
		}
	})
	t.Run("oversized request", func(t *testing.T) {
		store, request, _, _ := admittedRouteRequest(t, false)
		requestPath := filepath.Join(store.Root, request.HandoffID, requestFileName)
		if err := os.WriteFile(requestPath, bytes.Repeat([]byte("x"), maxExecutionLockRequestBytes+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := store.RequestBytes(request.HandoffID); err == nil || !strings.Contains(err.Error(), "bounded") {
			t.Fatalf("RequestBytes error = %v, want bounded-file refusal", err)
		}
	})
}

func admittedRouteRequest(t *testing.T, compact bool) (Store, Request, Digest, []byte) {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), DirName))
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if compact {
		var output bytes.Buffer
		if err := json.Compact(&output, raw); err != nil {
			t.Fatal(err)
		}
		raw = output.Bytes()
	}
	digest := DigestBytes(raw)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	return store, request, digest, raw
}

func validRoute(request Request, digest Digest) Route {
	return Route{
		SchemaVersion: RouteSchemaVersion, HandoffID: request.HandoffID, RequestDigest: digest,
		TargetMachine: request.TargetMachine, Courier: CourierSSH,
		SSH: &SSHConfig{Host: "hetzner-vm1", WBPath: "/home/ai/go/bin/wb"},
	}
}

func validRouteBytes(t *testing.T, request Request, digest Digest) []byte {
	t.Helper()
	return mustRouteJSON(t, validRoute(request, digest))
}

func mustRouteJSON(t *testing.T, route Route) []byte {
	t.Helper()
	raw, err := marshalJSON(route)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
