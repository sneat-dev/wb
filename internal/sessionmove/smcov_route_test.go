package sessionmove

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// smCovRouteAdmit admits one exact request into a fresh private store and
// returns the store plus the digest of the exact admitted bytes.
func smCovRouteAdmit(t *testing.T, request Request) (Store, Digest) {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), DirName))
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	digest := DigestBytes(raw)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	return store, digest
}

func smCovRouteOpenLock(t *testing.T, store Store, request Request, digest Digest) *ExecutionLock {
	t.Helper()
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatalf("AcquireExecutionLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return lock
}

func smCovRouteAddressPath(store Store, request Request) string {
	return filepath.Join(store.Root, successorAddressesDirName, successorAddressFileName(request.SuccessorWBSessionID))
}

// smCovRouteReadyForSuccessor publishes an exact route and completion receipt
// so the successor-address publication can be exercised from its first step.
func smCovRouteReadyForSuccessor(t *testing.T, request Request) (Store, Digest, *ExecutionLock, Receipt) {
	t.Helper()
	store, digest := smCovRouteAdmit(t, request)
	if _, _, err := store.SaveRoute(validRoute(request, digest)); err != nil {
		t.Fatalf("SaveRoute: %v", err)
	}
	lock := smCovRouteOpenLock(t, store, request, digest)
	receipt := validReceipt(request, digest)
	if _, _, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, receipt); err != nil {
		t.Fatalf("SaveReceiptUnderLock: %v", err)
	}
	return store, digest, lock, receipt
}

// smCovRoutePublishSuccessor runs the whole valid publish flow and returns the
// exact on-disk bytes of the published successor address.
func smCovRoutePublishSuccessor(t *testing.T, request Request) (Store, Request, Digest, *ExecutionLock, SuccessorAddress, []byte) {
	t.Helper()
	store, digest, lock, receipt := smCovRouteReadyForSuccessor(t, request)
	address, replay, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, receipt)
	if err != nil {
		t.Fatalf("SaveSuccessorAddressUnderLock: %v", err)
	}
	if replay {
		t.Fatal("first successor address publication reported a replay")
	}
	raw, err := os.ReadFile(smCovRouteAddressPath(store, request))
	if err != nil {
		t.Fatalf("read published successor address: %v", err)
	}
	decoded, err := decodeAndValidateSuccessorAddress(raw, request.SuccessorWBSessionID)
	if err != nil {
		t.Fatalf("decode published successor address: %v", err)
	}
	if !reflect.DeepEqual(decoded, address) {
		t.Fatalf("published successor address = %#v, want %#v", decoded, address)
	}
	return store, request, digest, lock, address, raw
}

// smCovRouteBaseAddress derives one fully valid successor address in memory.
func smCovRouteBaseAddress(t *testing.T) (Request, Digest, SuccessorAddress) {
	t.Helper()
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	digest := DigestBytes(raw)
	address := successorAddressFor(request, digest, validReceipt(request, digest), validRoute(request, digest))
	if err := validateSuccessorAddress(address, request.SuccessorWBSessionID); err != nil {
		t.Fatalf("base successor address is invalid: %v", err)
	}
	return request, digest, address
}

func TestSmCovRouteSaveRouteRejectionsAndLoopback(t *testing.T) {
	t.Parallel()
	t.Run("missing handoff", func(t *testing.T) {
		t.Parallel()
		request := validRequest()
		raw, err := EncodeRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		digest := DigestBytes(raw)
		store := NewStore(filepath.Join(t.TempDir(), DirName))
		if _, replay, err := store.SaveRoute(validRoute(request, digest)); err == nil || replay {
			t.Fatalf("SaveRoute for absent handoff = replay %t, error %v", replay, err)
		}
		if _, err := os.Stat(filepath.Join(store.Root, request.HandoffID, routeFileName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("absent handoff wrote durable route state: %v", err)
		}
	})

	for _, test := range []struct {
		name  string
		route func(request Request, digest Digest) Route
		want  string
	}{
		{"unknown courier", func(request Request, digest Digest) Route {
			return Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine, Courier: Courier("carrier-pigeon")}
		}, "unsupported"},
		{"ssh without address", func(request Request, digest Digest) Route {
			return Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine, Courier: CourierSSH}
		}, "only one configured ssh address"},
		{"ssh with synchestra too", func(request Request, digest Digest) Route {
			return Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine, Courier: CourierSSH,
				SSH: &SSHConfig{Host: "host"}, Synchestra: &SynchestraConfig{Runner: "runner"}}
		}, "only one configured ssh address"},
		{"ssh unsafe host", func(request Request, digest Digest) Route {
			return Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine, Courier: CourierSSH,
				SSH: &SSHConfig{Host: "bad host"}}
		}, "ssh.host"},
		{"synchestra without address", func(request Request, digest Digest) Route {
			return Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine, Courier: CourierSynchestra}
		}, "only one configured runner address"},
		{"synchestra with ssh too", func(request Request, digest Digest) Route {
			return Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine, Courier: CourierSynchestra,
				Synchestra: &SynchestraConfig{Runner: "runner"}, SSH: &SSHConfig{Host: "host"}}
		}, "only one configured runner address"},
		{"loopback with ssh", func(request Request, digest Digest) Route {
			return Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine, Courier: CourierLoopback,
				SSH: &SSHConfig{Host: "host"}}
		}, "must not carry a remote address"},
		{"loopback with synchestra", func(request Request, digest Digest) Route {
			return Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine, Courier: CourierLoopback,
				Synchestra: &SynchestraConfig{Runner: "runner"}}
		}, "must not carry a remote address"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, request, digest, _ := admittedRouteRequest(t, false)
			_, replay, err := store.SaveRoute(test.route(request, digest))
			if err == nil || replay {
				t.Fatalf("SaveRoute = replay %t, error %v", replay, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SaveRoute error = %v, want mention of %q", err, test.want)
			}
			if _, statErr := os.Stat(filepath.Join(store.Root, request.HandoffID, routeFileName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rejected courier route wrote durable state: %v", statErr)
			}
		})
	}

	t.Run("loopback is published and replayed", func(t *testing.T) {
		t.Parallel()
		store, request, digest, _ := admittedRouteRequest(t, false)
		route := Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine, Courier: CourierLoopback}
		first, replay, err := store.SaveRoute(route)
		if err != nil || replay {
			t.Fatalf("loopback SaveRoute = replay %t, error %v", replay, err)
		}
		if first.SchemaVersion != RouteSchemaVersion || first.Courier != CourierLoopback || first.SSH != nil || first.Synchestra != nil {
			t.Fatalf("loopback route = %#v", first)
		}
		loaded, err := store.LoadRoute(request.HandoffID)
		if err != nil || !reflect.DeepEqual(loaded, first) {
			t.Fatalf("LoadRoute = %#v, error %v", loaded, err)
		}
		again, replay, err := store.SaveRoute(route)
		if err != nil || !replay || !reflect.DeepEqual(again, first) {
			t.Fatalf("replayed loopback route = %#v, replay %t, error %v", again, replay, err)
		}
	})
}

func TestSmCovRouteSaveRouteRefusesTamperedAndUnreadableArtifacts(t *testing.T) {
	t.Parallel()
	t.Run("schema zero route on disk", func(t *testing.T) {
		t.Parallel()
		store, request, digest, _ := admittedRouteRequest(t, false)
		route := validRoute(request, digest)
		if _, _, err := store.SaveRoute(route); err != nil {
			t.Fatal(err)
		}
		tampered := route
		tampered.SchemaVersion = 0
		path := filepath.Join(store.Root, request.HandoffID, routeFileName)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, mustRouteJSON(t, tampered), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, replay, err := store.SaveRoute(route); !errors.Is(err, ErrHandoffConflict) || replay {
			t.Fatalf("SaveRoute over schema-zero route = replay %t, error %v", replay, err)
		}
		if _, err := store.LoadRoute(request.HandoffID); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("LoadRoute schema-zero error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("route symlink", func(t *testing.T) {
		t.Parallel()
		store, request, digest, _ := admittedRouteRequest(t, false)
		path := filepath.Join(store.Root, request.HandoffID, routeFileName)
		external := filepath.Join(t.TempDir(), "external-route.json")
		raw := validRouteBytes(t, request, digest)
		if err := os.WriteFile(external, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, path); err != nil {
			t.Fatal(err)
		}
		if _, replay, err := store.SaveRoute(validRoute(request, digest)); err == nil || replay {
			t.Fatalf("SaveRoute over route symlink = replay %t, error %v", replay, err)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("route symlink was replaced: info=%v error=%v", info, err)
		}
		got, err := os.ReadFile(external)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("external route changed: %q, error %v", got, err)
		}
	})
}

func TestSmCovRouteLoadRouteUnderLockAuthority(t *testing.T) {
	t.Parallel()
	store, request, digest, _ := admittedRouteRequest(t, false)
	if _, _, err := store.SaveRoute(validRoute(request, digest)); err != nil {
		t.Fatal(err)
	}
	lock := smCovRouteOpenLock(t, store, request, digest)

	t.Run("nil lock", func(t *testing.T) {
		t.Parallel()
		if _, err := store.LoadRouteUnderLock(nil, request.HandoffID, digest); err == nil || !strings.Contains(err.Error(), "exact admitted execution authority") {
			t.Fatalf("LoadRouteUnderLock(nil) error = %v", err)
		}
	})

	t.Run("wrong digest", func(t *testing.T) {
		t.Parallel()
		if _, err := store.LoadRouteUnderLock(lock, request.HandoffID, DigestBytes([]byte("other request"))); err == nil || !strings.Contains(err.Error(), "exact admitted execution authority") {
			t.Fatalf("LoadRouteUnderLock(wrong digest) error = %v", err)
		}
	})

	t.Run("another handoff", func(t *testing.T) {
		t.Parallel()
		if _, err := store.LoadRouteUnderLock(lock, "handoff-other", digest); err == nil || !strings.Contains(err.Error(), "exact admitted execution authority") {
			t.Fatalf("LoadRouteUnderLock(other handoff) error = %v", err)
		}
	})

	t.Run("exact authority", func(t *testing.T) {
		t.Parallel()
		loaded, err := store.LoadRouteUnderLock(lock, request.HandoffID, digest)
		if err != nil || !reflect.DeepEqual(loaded, validRoute(request, digest)) {
			t.Fatalf("LoadRouteUnderLock = %#v, error %v", loaded, err)
		}
	})

	t.Run("missing route", func(t *testing.T) {
		t.Parallel()
		if err := os.Remove(filepath.Join(store.Root, request.HandoffID, routeFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadRouteUnderLock(lock, request.HandoffID, digest); err == nil {
			t.Fatal("LoadRouteUnderLock accepted a missing durable route")
		}
	})
}

func TestSmCovRouteSaveSuccessorAddressUnderLockRejections(t *testing.T) {
	t.Run("nil lock", func(t *testing.T) {
		store, request, digest, _ := admittedRouteRequest(t, false)
		if _, _, err := store.SaveSuccessorAddressUnderLock(nil, request.HandoffID, digest, validReceipt(request, digest)); err == nil || !strings.Contains(err.Error(), "exact admitted execution authority") {
			t.Fatalf("SaveSuccessorAddressUnderLock(nil) error = %v", err)
		}
	})

	t.Run("receipt does not bind request", func(t *testing.T) {
		store, digest, lock, _ := smCovRouteReadyForSuccessor(t, validRequest())
		request := validRequest()
		unbound := validReceipt(request, digest)
		unbound.PID = 0
		if _, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, unbound); err == nil {
			t.Fatal("SaveSuccessorAddressUnderLock accepted a receipt that does not bind its request")
		}
		if _, err := os.Stat(filepath.Join(store.Root, successorAddressesDirName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected receipt created the successor index: %v", err)
		}
	})

	t.Run("corrupt durable receipt", func(t *testing.T) {
		store, digest, lock, _ := smCovRouteReadyForSuccessor(t, validRequest())
		request := validRequest()
		path := filepath.Join(store.Root, request.HandoffID, receiptFileName)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{not-json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, validReceipt(request, digest)); err == nil {
			t.Fatal("SaveSuccessorAddressUnderLock accepted a corrupt durable receipt")
		}
	})

	t.Run("no durable receipt", func(t *testing.T) {
		request := validRequest()
		store, digest := smCovRouteAdmit(t, request)
		if _, _, err := store.SaveRoute(validRoute(request, digest)); err != nil {
			t.Fatal(err)
		}
		lock := smCovRouteOpenLock(t, store, request, digest)
		_, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, validReceipt(request, digest))
		if err == nil || !strings.Contains(err.Error(), "durable completion receipt") {
			t.Fatalf("SaveSuccessorAddressUnderLock(no receipt) error = %v", err)
		}
	})

	t.Run("supplied receipt differs", func(t *testing.T) {
		store, digest, lock, receipt := smCovRouteReadyForSuccessor(t, validRequest())
		request := validRequest()
		different := receipt
		different.PID = receipt.PID + 1
		if err := ValidateReceiptForRequest(different, request, digest); err != nil {
			t.Fatalf("drifted receipt is not request-valid: %v", err)
		}
		if _, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, different); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("SaveSuccessorAddressUnderLock(drifted receipt) error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("no durable route", func(t *testing.T) {
		request := validRequest()
		store, digest := smCovRouteAdmit(t, request)
		lock := smCovRouteOpenLock(t, store, request, digest)
		if _, _, err := store.SaveReceiptUnderLock(lock, request.HandoffID, digest, validReceipt(request, digest)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, validReceipt(request, digest)); err == nil {
			t.Fatal("SaveSuccessorAddressUnderLock accepted a missing durable route")
		}
	})

	t.Run("successors entry is a regular file", func(t *testing.T) {
		store, digest, lock, receipt := smCovRouteReadyForSuccessor(t, validRequest())
		request := validRequest()
		if err := os.WriteFile(filepath.Join(store.Root, successorAddressesDirName), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, receipt); err == nil {
			t.Fatal("SaveSuccessorAddressUnderLock accepted a successor index that is a regular file")
		}
	})

	t.Run("derived address fails validation", func(t *testing.T) {
		request := validRequest()
		request.SourceRuntime = "first line\nsecond line"
		store, digest, lock, receipt := smCovRouteReadyForSuccessor(t, request)
		_, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, receipt)
		if err == nil || !strings.Contains(err.Error(), "runtime must be non-empty and single-line") {
			t.Fatalf("SaveSuccessorAddressUnderLock(multiline runtime) error = %v", err)
		}
	})

	t.Run("address exceeds the durable bound", func(t *testing.T) {
		request := validRequest()
		request.SourceRuntime = strings.Repeat("r", maxSuccessorAddressBytes+1)
		store, digest, lock, receipt := smCovRouteReadyForSuccessor(t, request)
		_, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, receipt)
		if err == nil || !strings.Contains(err.Error(), "successor address exceeds") {
			t.Fatalf("SaveSuccessorAddressUnderLock(oversized address) error = %v", err)
		}
		if _, err := os.Stat(smCovRouteAddressPath(store, request)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("oversized address wrote durable state: %v", err)
		}
	})

	t.Run("tampered published address", func(t *testing.T) {
		store, request, digest, lock, address, _ := smCovRoutePublishSuccessor(t, validRequest())
		path := smCovRouteAddressPath(store, request)
		tampered := address
		tampered.StartedAt = address.StartedAt.Add(time.Hour)
		raw, err := marshalJSON(tampered)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, validReceipt(request, digest)); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("SaveSuccessorAddressUnderLock(tampered) error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("published address is a symlink", func(t *testing.T) {
		store, request, digest, lock, _, raw := smCovRoutePublishSuccessor(t, validRequest())
		path := smCovRouteAddressPath(store, request)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		external := filepath.Join(t.TempDir(), "address.json")
		if err := os.WriteFile(external, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, path); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, validReceipt(request, digest)); err == nil {
			t.Fatal("SaveSuccessorAddressUnderLock accepted a symlinked successor address")
		}
	})

	t.Run("happy publication and replay", func(t *testing.T) {
		store, request, digest, lock, address, _ := smCovRoutePublishSuccessor(t, validRequest())
		if address.SuccessorWBSessionID != request.SuccessorWBSessionID || !reflect.DeepEqual(address.Route, validRoute(request, digest)) {
			t.Fatalf("published successor address = %#v", address)
		}
		info, err := os.Stat(smCovRouteAddressPath(store, request))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("successor address file mode: info=%v error=%v", info, err)
		}
		again, replay, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, validReceipt(request, digest))
		if err != nil || !replay || !reflect.DeepEqual(again, address) {
			t.Fatalf("replayed successor address = %#v, replay %t, error %v", again, replay, err)
		}
	})
}

func TestSmCovRouteLoadSuccessorAddressArtifacts(t *testing.T) {
	store, request, _, _, address, raw := smCovRoutePublishSuccessor(t, validRequest())

	t.Run("invalid successor id", func(t *testing.T) {
		t.Parallel()
		if _, err := store.LoadSuccessorAddress(""); err == nil {
			t.Fatal("LoadSuccessorAddress accepted an empty successor id")
		}
		if _, err := store.LoadSuccessorAddress("bad id"); err == nil {
			t.Fatal("LoadSuccessorAddress accepted an unsafe successor id")
		}
	})

	t.Run("missing store root", func(t *testing.T) {
		t.Parallel()
		missing := NewStore(filepath.Join(t.TempDir(), "absent", DirName))
		if _, err := missing.LoadSuccessorAddress(request.SuccessorWBSessionID); err == nil {
			t.Fatal("LoadSuccessorAddress accepted an absent store root")
		}
	})

	t.Run("missing successors directory", func(t *testing.T) {
		fresh, freshRequest, _, _, _, _ := smCovRoutePublishSuccessor(t, validRequest())
		if err := os.RemoveAll(filepath.Join(fresh.Root, successorAddressesDirName)); err != nil {
			t.Fatal(err)
		}
		if _, err := fresh.LoadSuccessorAddress(freshRequest.SuccessorWBSessionID); err == nil {
			t.Fatal("LoadSuccessorAddress accepted a missing successor index")
		}
	})

	t.Run("missing address file", func(t *testing.T) {
		fresh, freshRequest, _, _, _, _ := smCovRoutePublishSuccessor(t, validRequest())
		if err := os.Remove(smCovRouteAddressPath(fresh, freshRequest)); err != nil {
			t.Fatal(err)
		}
		if _, err := fresh.LoadSuccessorAddress(freshRequest.SuccessorWBSessionID); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("LoadSuccessorAddress(missing file) error = %v, want os.ErrNotExist", err)
		}
	})

	t.Run("undecodable address file", func(t *testing.T) {
		fresh, freshRequest, _, _, _, _ := smCovRoutePublishSuccessor(t, validRequest())
		path := smCovRouteAddressPath(fresh, freshRequest)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{not-json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fresh.LoadSuccessorAddress(freshRequest.SuccessorWBSessionID); err == nil || !strings.Contains(err.Error(), "decode successor address") {
			t.Fatalf("LoadSuccessorAddress(undecodable) error = %v", err)
		}
	})

	t.Run("handoff is absent", func(t *testing.T) {
		t.Parallel()
		other := NewStore(filepath.Join(t.TempDir(), DirName))
		if err := os.MkdirAll(filepath.Join(other.Root, successorAddressesDirName), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other.Root, successorAddressesDirName, successorAddressFileName(request.SuccessorWBSessionID)), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := other.LoadSuccessorAddress(request.SuccessorWBSessionID); err == nil || !strings.Contains(err.Error(), "corroborate successor address handoff") {
			t.Fatalf("LoadSuccessorAddress(absent handoff) error = %v", err)
		}
	})

	t.Run("handoff has no admitted request", func(t *testing.T) {
		t.Parallel()
		other := NewStore(filepath.Join(t.TempDir(), DirName))
		if err := os.MkdirAll(filepath.Join(other.Root, successorAddressesDirName), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other.Root, successorAddressesDirName, successorAddressFileName(request.SuccessorWBSessionID)), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(other.Root, request.HandoffID), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := other.LoadSuccessorAddress(request.SuccessorWBSessionID); err == nil {
			t.Fatal("LoadSuccessorAddress accepted a handoff without an admitted request")
		}
	})

	t.Run("address digest does not match admitted request", func(t *testing.T) {
		fresh, freshRequest, _, _, _, _ := smCovRoutePublishSuccessor(t, validRequest())
		path := smCovRouteAddressPath(fresh, freshRequest)
		forged := address
		forged.RequestDigest = DigestBytes([]byte("forged successor request digest"))
		forged.Route.RequestDigest = forged.RequestDigest
		sourceReference, err := ParseWorkLogReference(forged.SourceWorkLogReference)
		if err != nil {
			t.Fatal(err)
		}
		claimID, err := ExternalHandoffClaimID(forged.RequestDigest, forged.SuccessorWBSessionID)
		if err != nil {
			t.Fatal(err)
		}
		forged.TargetWorkLogReference = (WorkLogReference{EffortID: sourceReference.EffortID, RunID: sourceReference.RunID, ClaimID: claimID}).String()
		replacement, err := marshalJSON(forged)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, replacement, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fresh.LoadSuccessorAddress(freshRequest.SuccessorWBSessionID); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("LoadSuccessorAddress(forged digest) error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("happy path", func(t *testing.T) {
		t.Parallel()
		loaded, err := store.LoadSuccessorAddress(request.SuccessorWBSessionID)
		if err != nil || !reflect.DeepEqual(loaded, address) {
			t.Fatalf("LoadSuccessorAddress = %#v, error %v", loaded, err)
		}
	})
}

func TestSmCovRouteLoadSuccessorAddressUnderLockArtifacts(t *testing.T) {
	t.Run("nil lock", func(t *testing.T) {
		t.Parallel()
		store, request, digest, _ := admittedRouteRequest(t, false)
		if _, err := store.LoadSuccessorAddressUnderLock(nil, request.HandoffID, digest); err == nil || !strings.Contains(err.Error(), "exact admitted execution authority") {
			t.Fatalf("LoadSuccessorAddressUnderLock(nil) error = %v", err)
		}
	})

	t.Run("successors directory is absent", func(t *testing.T) {
		t.Parallel()
		request := validRequest()
		store, digest := smCovRouteAdmit(t, request)
		if _, _, err := store.SaveRoute(validRoute(request, digest)); err != nil {
			t.Fatal(err)
		}
		lock := smCovRouteOpenLock(t, store, request, digest)
		if _, err := store.LoadSuccessorAddressUnderLock(lock, request.HandoffID, digest); err == nil {
			t.Fatal("LoadSuccessorAddressUnderLock accepted a missing successor index")
		}
	})

	t.Run("address file is absent", func(t *testing.T) {
		t.Parallel()
		request := validRequest()
		store, digest := smCovRouteAdmit(t, request)
		if _, _, err := store.SaveRoute(validRoute(request, digest)); err != nil {
			t.Fatal(err)
		}
		lock := smCovRouteOpenLock(t, store, request, digest)
		if err := os.Mkdir(filepath.Join(store.Root, successorAddressesDirName), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadSuccessorAddressUnderLock(lock, request.HandoffID, digest); err == nil {
			t.Fatal("LoadSuccessorAddressUnderLock accepted a missing successor address")
		}
	})

	t.Run("lock is for another handoff", func(t *testing.T) {
		store, request, digest, _, _, _ := smCovRoutePublishSuccessor(t, validRequest())
		other := validRequest()
		other.HandoffID = "handoff-other"
		otherRaw, err := EncodeRequest(other)
		if err != nil {
			t.Fatal(err)
		}
		otherDigest := DigestBytes(otherRaw)
		if _, err := store.Admit(otherRaw, otherDigest); err != nil {
			t.Fatal(err)
		}
		otherLock := smCovRouteOpenLock(t, store, other, otherDigest)
		if _, err := store.LoadSuccessorAddressUnderLock(otherLock, request.HandoffID, digest); err == nil || !strings.Contains(err.Error(), "exact admitted execution authority") {
			t.Fatalf("LoadSuccessorAddressUnderLock(wrong lock) error = %v", err)
		}
	})

	t.Run("tampered address bytes", func(t *testing.T) {
		store, request, digest, lock, address, _ := smCovRoutePublishSuccessor(t, validRequest())
		path := smCovRouteAddressPath(store, request)
		tampered := address
		tampered.StartedAt = address.StartedAt.Add(time.Minute)
		replacement, err := marshalJSON(tampered)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, replacement, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadSuccessorAddressUnderLock(lock, request.HandoffID, digest); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("LoadSuccessorAddressUnderLock(tampered) error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("address names another handoff", func(t *testing.T) {
		store, request, digest, lock, address, _ := smCovRoutePublishSuccessor(t, validRequest())
		path := smCovRouteAddressPath(store, request)
		tampered := address
		tampered.HandoffID = "handoff-other"
		tampered.Route.HandoffID = "handoff-other"
		replacement, err := marshalJSON(tampered)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, replacement, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadSuccessorAddressUnderLock(lock, request.HandoffID, digest); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("LoadSuccessorAddressUnderLock(other handoff) error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("happy path", func(t *testing.T) {
		store, request, digest, lock, address, _ := smCovRoutePublishSuccessor(t, validRequest())
		loaded, err := store.LoadSuccessorAddressUnderLock(lock, request.HandoffID, digest)
		if err != nil || !reflect.DeepEqual(loaded, address) {
			t.Fatalf("LoadSuccessorAddressUnderLock = %#v, error %v", loaded, err)
		}
	})
}

func TestSmCovRouteCorroborateSuccessorAddressAt(t *testing.T) {
	store, request, digest, _, address, raw := smCovRoutePublishSuccessor(t, validRequest())

	openHandoff := func(t *testing.T, s Store, handoffID string) *os.File {
		t.Helper()
		handoff, err := s.openHandoff(handoffID, false)
		if err != nil {
			t.Fatalf("openHandoff: %v", err)
		}
		t.Cleanup(func() { _ = handoff.Close() })
		return handoff
	}

	t.Run("undecodable address", func(t *testing.T) {
		t.Parallel()
		if _, err := corroborateSuccessorAddressAt(openHandoff(t, store, request.HandoffID), request, digest, request.SuccessorWBSessionID, []byte("{not-json\n")); err == nil {
			t.Fatal("corroborateSuccessorAddressAt accepted an undecodable address")
		}
	})

	t.Run("address names another handoff", func(t *testing.T) {
		t.Parallel()
		tampered := address
		tampered.HandoffID = "handoff-other"
		tampered.Route.HandoffID = "handoff-other"
		mutated, err := marshalJSON(tampered)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := corroborateSuccessorAddressAt(openHandoff(t, store, request.HandoffID), request, digest, request.SuccessorWBSessionID, mutated); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("corroborate(other handoff) error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("no durable receipt", func(t *testing.T) {
		fresh, freshRequest, freshDigest, _, _, freshRaw := smCovRoutePublishSuccessor(t, validRequest())
		if err := os.Remove(filepath.Join(fresh.Root, freshRequest.HandoffID, receiptFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := corroborateSuccessorAddressAt(openHandoff(t, fresh, freshRequest.HandoffID), freshRequest, freshDigest, freshRequest.SuccessorWBSessionID, freshRaw); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("corroborate(no receipt) error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("corrupt durable receipt", func(t *testing.T) {
		fresh, freshRequest, freshDigest, _, _, freshRaw := smCovRoutePublishSuccessor(t, validRequest())
		path := filepath.Join(fresh.Root, freshRequest.HandoffID, receiptFileName)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{not-json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := corroborateSuccessorAddressAt(openHandoff(t, fresh, freshRequest.HandoffID), freshRequest, freshDigest, freshRequest.SuccessorWBSessionID, freshRaw); err == nil {
			t.Fatal("corroborateSuccessorAddressAt accepted a corrupt durable receipt")
		}
	})

	t.Run("no durable route", func(t *testing.T) {
		fresh, freshRequest, freshDigest, _, _, freshRaw := smCovRoutePublishSuccessor(t, validRequest())
		if err := os.Remove(filepath.Join(fresh.Root, freshRequest.HandoffID, routeFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := corroborateSuccessorAddressAt(openHandoff(t, fresh, freshRequest.HandoffID), freshRequest, freshDigest, freshRequest.SuccessorWBSessionID, freshRaw); err == nil {
			t.Fatal("corroborateSuccessorAddressAt accepted a missing durable route")
		}
	})

	t.Run("address bytes differ from the derived address", func(t *testing.T) {
		t.Parallel()
		tampered := address
		tampered.StartedAt = address.StartedAt.Add(time.Hour)
		mutated, err := marshalJSON(tampered)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := corroborateSuccessorAddressAt(openHandoff(t, store, request.HandoffID), request, digest, request.SuccessorWBSessionID, mutated); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("corroborate(drifted bytes) error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		got, err := corroborateSuccessorAddressAt(openHandoff(t, store, request.HandoffID), request, digest, request.SuccessorWBSessionID, raw)
		if err != nil || !reflect.DeepEqual(got, address) {
			t.Fatalf("corroborateSuccessorAddressAt = %#v, error %v", got, err)
		}
	})
}

func TestSmCovRouteValidateCourierRoute(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		route Route
		want  string
	}{
		{"ssh", Route{Courier: CourierSSH, SSH: &SSHConfig{Host: "host", WBPath: "/opt/wb"}}, ""},
		{"ssh without address", Route{Courier: CourierSSH}, "only one configured ssh address"},
		{"ssh with synchestra", Route{Courier: CourierSSH, SSH: &SSHConfig{Host: "host"}, Synchestra: &SynchestraConfig{Runner: "runner"}}, "only one configured ssh address"},
		{"ssh unsafe", Route{Courier: CourierSSH, SSH: &SSHConfig{Host: "bad host"}}, "ssh.host"},
		{"synchestra", Route{Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "runner"}}, ""},
		{"synchestra without address", Route{Courier: CourierSynchestra}, "only one configured runner address"},
		{"synchestra with ssh", Route{Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "runner"}, SSH: &SSHConfig{Host: "host"}}, "only one configured runner address"},
		{"synchestra unsafe runner", Route{Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "-oops"}}, "synchestra.runner"},
		{"loopback", Route{Courier: CourierLoopback}, ""},
		{"loopback with ssh", Route{Courier: CourierLoopback, SSH: &SSHConfig{Host: "host"}}, "must not carry a remote address"},
		{"loopback with synchestra", Route{Courier: CourierLoopback, Synchestra: &SynchestraConfig{Runner: "runner"}}, "must not carry a remote address"},
		{"unknown", Route{Courier: Courier("carrier-pigeon")}, "unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateCourierRoute(test.route)
			if test.want == "" {
				if err != nil {
					t.Fatalf("validateCourierRoute(%#v) error = %v", test.route, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateCourierRoute(%#v) error = %v, want mention of %q", test.route, err, test.want)
			}
		})
	}
}

func TestSmCovRouteOpenSuccessorAddressesAt(t *testing.T) {
	t.Parallel()
	t.Run("nil root", func(t *testing.T) {
		t.Parallel()
		if _, err := openSuccessorAddressesAt(nil, false); err == nil {
			t.Fatal("openSuccessorAddressesAt accepted a nil root")
		}
	})

	t.Run("create false on missing directory", func(t *testing.T) {
		t.Parallel()
		root, err := os.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		if _, err := openSuccessorAddressesAt(root, false); err == nil {
			t.Fatal("openSuccessorAddressesAt accepted a missing index while not creating it")
		}
	})

	t.Run("create is idempotent", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		root, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		first, err := openSuccessorAddressesAt(root, true)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = first.Close() }()
		second, err := openSuccessorAddressesAt(root, true)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = second.Close() }()
		firstInfo, err := first.Stat()
		if err != nil {
			t.Fatal(err)
		}
		secondInfo, err := second.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(firstInfo, secondInfo) {
			t.Fatal("second create opened a different successor index inode")
		}
		diskInfo, err := os.Stat(filepath.Join(dir, successorAddressesDirName))
		if err != nil || diskInfo.Mode().Perm() != 0o700 {
			t.Fatalf("successor index mode: info=%v error=%v", diskInfo, err)
		}
	})

	t.Run("unsafe mode", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		index := filepath.Join(dir, successorAddressesDirName)
		if err := os.Mkdir(index, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(index, 0o755); err != nil {
			t.Fatal(err)
		}
		root, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		if _, err := openSuccessorAddressesAt(root, false); err == nil || !strings.Contains(err.Error(), "is not mode 0700") {
			t.Fatalf("openSuccessorAddressesAt(unsafe mode) error = %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		external := filepath.Join(t.TempDir(), "external-index")
		if err := os.Mkdir(external, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(dir, successorAddressesDirName)); err != nil {
			t.Fatal(err)
		}
		root, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		for _, create := range []bool{false, true} {
			if _, err := openSuccessorAddressesAt(root, create); err == nil {
				t.Fatalf("openSuccessorAddressesAt(create=%t) followed a symlink", create)
			}
		}
	})

	t.Run("regular file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, successorAddressesDirName), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		root, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		for _, create := range []bool{false, true} {
			if _, err := openSuccessorAddressesAt(root, create); err == nil {
				t.Fatalf("openSuccessorAddressesAt(create=%t) accepted a regular file", create)
			}
		}
	})

	t.Run("root is not a directory", func(t *testing.T) {
		t.Parallel()
		file, err := os.CreateTemp(t.TempDir(), "not-a-directory")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		if _, err := openSuccessorAddressesAt(file, true); err == nil {
			t.Fatal("openSuccessorAddressesAt created an index beneath a regular file")
		}
	})
}

func TestSmCovRouteDecodeAndValidateSuccessorAddress(t *testing.T) {
	request, _, address := smCovRouteBaseAddress(t)

	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()
		if _, err := decodeAndValidateSuccessorAddress([]byte("{not-json\n"), request.SuccessorWBSessionID); err == nil || !strings.Contains(err.Error(), "decode successor address") {
			t.Fatalf("decodeAndValidateSuccessorAddress error = %v", err)
		}
	})

	t.Run("schema zero", func(t *testing.T) {
		t.Parallel()
		tampered := address
		tampered.SchemaVersion = 0
		raw, err := marshalJSON(tampered)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeAndValidateSuccessorAddress(raw, request.SuccessorWBSessionID); err == nil {
			t.Fatal("decodeAndValidateSuccessorAddress accepted schema zero")
		}
	})

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		raw, err := marshalJSON(address)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeAndValidateSuccessorAddress(raw, request.SuccessorWBSessionID)
		if err != nil || !reflect.DeepEqual(decoded, address) {
			t.Fatalf("decodeAndValidateSuccessorAddress = %#v, error %v", decoded, err)
		}
	})
}

func TestSmCovRouteValidateSuccessorAddressBranches(t *testing.T) {
	request, _, base := smCovRouteBaseAddress(t)
	if err := validateSuccessorAddress(base, request.SuccessorWBSessionID); err != nil {
		t.Fatalf("valid successor address rejected: %v", err)
	}
	sourceReference, err := ParseWorkLogReference(base.SourceWorkLogReference)
	if err != nil {
		t.Fatal(err)
	}
	foreignClaim := (WorkLogReference{EffortID: sourceReference.EffortID, RunID: sourceReference.RunID, ClaimID: strings.Repeat("d", 64)}).String()

	for _, test := range []struct {
		name         string
		mutate       func(*SuccessorAddress)
		wantErr      error
		wantContains string
	}{
		{"schema zero", func(a *SuccessorAddress) { a.SchemaVersion = 0 }, nil, "schema_version"},
		{"schema two", func(a *SuccessorAddress) { a.SchemaVersion = 2 }, nil, "schema_version"},
		{"successor id invalid", func(a *SuccessorAddress) { a.SuccessorWBSessionID = "bad id" }, nil, "successor_wb_session_id"},
		{"predecessor id invalid", func(a *SuccessorAddress) { a.PredecessorWBSessionID = "" }, nil, "predecessor_wb_session_id"},
		{"handoff id invalid", func(a *SuccessorAddress) { a.HandoffID = "" }, nil, "handoff_id"},
		{"source machine invalid", func(a *SuccessorAddress) { a.SourceMachine = "" }, nil, "source_machine"},
		{"target machine invalid", func(a *SuccessorAddress) { a.TargetMachine = "-bad" }, nil, "target_machine"},
		{"tmux name invalid", func(a *SuccessorAddress) { a.TmuxName = "" }, nil, "tmux_name"},
		{"request digest invalid", func(a *SuccessorAddress) { a.RequestDigest = "nonsense" }, nil, "digest"},
		{"source reference invalid", func(a *SuccessorAddress) { a.SourceWorkLogReference = "not-a-reference" }, nil, "work log reference"},
		{"target reference invalid", func(a *SuccessorAddress) { a.TargetWorkLogReference = "worklog:effort/run" }, nil, "work log reference"},
		{"target claim not deterministic", func(a *SuccessorAddress) { a.TargetWorkLogReference = foreignClaim }, ErrHandoffConflict, ""},
		{"tmux name not deterministic", func(a *SuccessorAddress) { a.TmuxName = "wb-session-elsewhere" }, ErrHandoffConflict, ""},
		{"runtime empty", func(a *SuccessorAddress) { a.Runtime = "  " }, nil, "runtime must be non-empty"},
		{"runtime multiline", func(a *SuccessorAddress) { a.Runtime = "one\ntwo" }, nil, "runtime must be non-empty"},
		{"attempt id malformed", func(a *SuccessorAddress) { a.AttemptID = "attempt" }, nil, "attempt_id"},
		{"attempt index mismatch", func(a *SuccessorAddress) { a.AttemptID = "000007-" + strings.Repeat("a", 32) }, nil, "attempt_index"},
		{"pid not positive", func(a *SuccessorAddress) { a.PID = 0 }, nil, "pid must be positive"},
		{"pinned commit invalid", func(a *SuccessorAddress) { a.PinnedCommit = "short" }, nil, "launch identity is incomplete"},
		{"started at zero", func(a *SuccessorAddress) { a.StartedAt = time.Time{} }, nil, "launch identity is incomplete"},
		{"route schema mismatch", func(a *SuccessorAddress) { a.Route.SchemaVersion = 0 }, ErrHandoffConflict, ""},
		{"route handoff mismatch", func(a *SuccessorAddress) { a.Route.HandoffID = "handoff-elsewhere" }, ErrHandoffConflict, ""},
		{"route digest mismatch", func(a *SuccessorAddress) { a.Route.RequestDigest = DigestBytes([]byte("other")) }, ErrHandoffConflict, ""},
		{"route machine mismatch", func(a *SuccessorAddress) { a.Route.TargetMachine = "other-machine" }, ErrHandoffConflict, ""},
		{"route courier unsupported", func(a *SuccessorAddress) {
			a.Route.Courier = Courier("carrier-pigeon")
			a.Route.SSH = nil
			a.Route.Synchestra = nil
		}, nil, "unsupported"},
		{"route ssh missing", func(a *SuccessorAddress) { a.Route.SSH = nil }, nil, "only one configured ssh address"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			address := base
			test.mutate(&address)
			err := validateSuccessorAddress(address, request.SuccessorWBSessionID)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("validateSuccessorAddress error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantContains) {
				t.Fatalf("validateSuccessorAddress error = %v, want mention of %q", err, test.wantContains)
			}
		})
	}

	t.Run("key mismatch", func(t *testing.T) {
		t.Parallel()
		if err := validateSuccessorAddress(base, "wbs-different"); !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("validateSuccessorAddress(key mismatch) error = %v, want ErrHandoffConflict", err)
		}
	})
}

func TestSmCovRoutePublishAndReadRouteArtifacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	t.Run("read missing route file", func(t *testing.T) {
		t.Parallel()
		handle, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = handle.Close() }()
		if _, err := readRouteFileAt(handle, routeFileName, maxRouteBytes, "durable courier route"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("readRouteFileAt error = %v, want os.ErrNotExist", err)
		}
	})

	t.Run("oversized route", func(t *testing.T) {
		t.Parallel()
		handle, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = handle.Close() }()
		if _, err := publishRouteImmutableAt(handle, bytes.Repeat([]byte("x"), maxRouteBytes+1)); err == nil || !strings.Contains(err.Error(), "courier route exceeds") {
			t.Fatalf("publishRouteImmutableAt(oversized) error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, routeFileName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("oversized route wrote durable state: %v", err)
		}
	})

	t.Run("immutable publication replays identical bytes", func(t *testing.T) {
		t.Parallel()
		handle, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = handle.Close() }()
		raw := []byte("{\n  \"exact\": true\n}\n")
		created, err := publishRouteImmutableAt(handle, raw)
		if err != nil || !created {
			t.Fatalf("first publishRouteImmutableAt = created %t, error %v", created, err)
		}
		created, err = publishRouteImmutableAt(handle, raw)
		if err != nil || created {
			t.Fatalf("second publishRouteImmutableAt = created %t, error %v", created, err)
		}
		got, err := readRouteFileAt(handle, routeFileName, maxRouteBytes, "durable courier route")
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("readRouteFileAt = %q, error %v", got, err)
		}
	})
}

func TestSmCovRouteLoadRouteArtifacts(t *testing.T) {
	t.Parallel()
	t.Run("missing handoff", func(t *testing.T) {
		t.Parallel()
		request := validRequest()
		store := NewStore(filepath.Join(t.TempDir(), DirName))
		if _, err := store.LoadRoute(request.HandoffID); err == nil {
			t.Fatal("LoadRoute accepted an absent handoff")
		}
	})

	t.Run("missing route file", func(t *testing.T) {
		t.Parallel()
		store, request, _, _ := admittedRouteRequest(t, false)
		if _, err := store.LoadRoute(request.HandoffID); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("LoadRoute error = %v, want os.ErrNotExist", err)
		}
	})

	t.Run("malformed route file", func(t *testing.T) {
		t.Parallel()
		store, request, _, _ := admittedRouteRequest(t, false)
		if err := os.WriteFile(filepath.Join(store.Root, request.HandoffID, routeFileName), []byte("{not-json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadRoute(request.HandoffID); err == nil {
			t.Fatal("LoadRoute accepted malformed durable state")
		}
	})

	t.Run("corrupt request file", func(t *testing.T) {
		t.Parallel()
		store, request, _, _ := admittedRouteRequest(t, false)
		if err := os.WriteFile(filepath.Join(store.Root, request.HandoffID, requestFileName), []byte("{not-json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadRoute(request.HandoffID); err == nil {
			t.Fatal("LoadRoute accepted a corrupt durable request")
		}
	})
}

func TestSmCovRouteSaveRouteRejectsUnadmittedIdentity(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*Route)
	}{
		{"request digest", func(route *Route) { route.RequestDigest = DigestBytes([]byte("other request")) }},
		{"target machine", func(route *Route) { route.TargetMachine = "other-machine" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, request, digest, _ := admittedRouteRequest(t, false)
			route := validRoute(request, digest)
			test.mutate(&route)
			if _, replay, err := store.SaveRoute(route); !errors.Is(err, ErrHandoffConflict) || replay {
				t.Fatalf("SaveRoute = replay %t, error %v, want ErrHandoffConflict", replay, err)
			}
			if _, err := os.Stat(filepath.Join(store.Root, request.HandoffID, routeFileName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("mismatched route wrote durable state: %v", err)
			}
		})
	}
}

func TestSmCovRouteRequestBytesRejectsUnusableHandoffs(t *testing.T) {
	t.Parallel()
	t.Run("missing handoff", func(t *testing.T) {
		t.Parallel()
		request := validRequest()
		store := NewStore(filepath.Join(t.TempDir(), DirName))
		if _, _, _, err := store.RequestBytes(request.HandoffID); err == nil {
			t.Fatal("RequestBytes accepted an absent handoff")
		}
	})

	t.Run("corrupt request file", func(t *testing.T) {
		t.Parallel()
		store, request, _, _ := admittedRouteRequest(t, false)
		if err := os.WriteFile(filepath.Join(store.Root, request.HandoffID, requestFileName), []byte("{not-json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := store.RequestBytes(request.HandoffID); err == nil {
			t.Fatal("RequestBytes accepted a corrupt durable request")
		}
	})
}

func TestSmCovRouteDecodeAndValidateRoute(t *testing.T) {
	t.Parallel()
	_, request, digest, _ := admittedRouteRequest(t, false)
	valid := validRoute(request, digest)
	decoded, err := decodeAndValidateRoute(mustRouteJSON(t, valid), request, digest)
	if err != nil || !reflect.DeepEqual(decoded, valid) {
		t.Fatalf("decodeAndValidateRoute = %#v, error %v", decoded, err)
	}
	unroutable := valid
	unroutable.Courier = Courier("carrier-pigeon")
	unroutable.SSH = nil
	if _, err := decodeAndValidateRoute(mustRouteJSON(t, unroutable), request, digest); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("decodeAndValidateRoute(unsupported courier) error = %v", err)
	}
}
