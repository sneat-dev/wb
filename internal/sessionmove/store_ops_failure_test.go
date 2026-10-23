package sessionmove

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

var errStoreStep = errors.New("store step failed")

func failingPublish(name string) *storeOps {
	return &storeOps{publish: func(directory *os.File, gotName string, raw []byte, mode os.FileMode) (bool, error) {
		if gotName == name {
			return false, errStoreStep
		}
		return publishImmutableAt(directory, gotName, raw, mode, nil)
	}}
}

func failingRead(name string) *storeOps {
	return &storeOps{read: func(directory *os.File, gotName string, limit int64, label string) ([]byte, error) {
		if gotName == name {
			return nil, errStoreStep
		}
		return readImmutableAt(directory, gotName, limit, label)
	}}
}

func failingMarshal() *storeOps {
	return &storeOps{marshal: func(any) ([]byte, error) { return nil, errStoreStep }}
}

func TestCourierStoresPropagatePublicationPipelineErrors(t *testing.T) {
	t.Parallel()
	t.Run("route marshal", func(t *testing.T) {
		t.Parallel()
		store, request, digest, _ := admittedRouteRequest(t, false)
		store.ops = failingMarshal()
		route := Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
			Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "runner-1"}}
		if _, _, err := store.SaveRoute(route); !errors.Is(err, errStoreStep) {
			t.Fatalf("SaveRoute marshal failure = %v", err)
		}
	})
	for _, failure := range []string{"route replay read", "synchestra marshal", "synchestra publish", "synchestra read"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			store, request, digest, _ := admittedRouteRequest(t, false)
			route := Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
				Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "runner-1"}}
			if _, _, err := store.SaveRoute(route); err != nil {
				t.Fatal(err)
			}
			identity := SynchestraDispatch{HandoffID: request.HandoffID, RequestDigest: digest, Runner: "runner-1",
				InvocationID: request.HandoffID, Handler: SynchestraSessionAcceptHandler, DispatchID: "dispatch-1"}
			switch failure {
			case "route replay read":
				store.ops = failingRead(routeFileName)
				_, _, err := store.SaveRoute(route)
				if !errors.Is(err, errStoreStep) {
					t.Fatalf("SaveRoute replay read failure = %v", err)
				}
				return
			case "synchestra marshal":
				store.ops = failingMarshal()
			case "synchestra publish":
				store.ops = failingPublish(synchestraDispatchFileName)
			case "synchestra read":
				store.ops = failingRead(synchestraDispatchFileName)
			}
			if _, _, err := store.SaveSynchestraDispatch(identity); !errors.Is(err, errStoreStep) {
				t.Fatalf("SaveSynchestraDispatch %s failure = %v", failure, err)
			}
		})
	}
}

func TestMessageStoresPropagatePublicationPipelineErrors(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"payload publish", "payload read", "record publish"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			fixture := smCovMsgNewFixture(t, true, false, time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC))
			switch failure {
			case "payload publish":
				fixture.store.ops = failingPublish(messagePayloadFileName)
			case "payload read":
				fixture.store.ops = failingRead(messagePayloadFileName)
			case "record publish":
				fixture.store.ops = failingPublish(messageRecordFileName)
			}
			_, err := fixture.store.AdmitIncomingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.incomingRecordedAt)
			if !errors.Is(err, errStoreStep) {
				t.Fatalf("AdmitIncomingMessageUnderLock %s failure = %v", failure, err)
			}
		})
	}
	for _, failure := range []string{"intent publish", "intent read", "receipt publish", "receipt read"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			fixture := smCovMsgNewFixture(t, true, true, time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC))
			intent := fixture.smCovMsgDefaultIntent()
			switch failure {
			case "intent publish":
				fixture.store.ops = failingPublish(messageIntentFileName)
			case "intent read":
				fixture.store.ops = failingRead(messageIntentFileName)
			case "receipt publish", "receipt read":
				fixture.smCovMsgSaveIntent(t, intent)
				if failure == "receipt publish" {
					fixture.store.ops = failingPublish(messageReceiptFileName)
				} else {
					fixture.store.ops = failingRead(messageReceiptFileName)
				}
			}
			if failure == "intent publish" || failure == "intent read" {
				_, _, err := fixture.store.SaveIncomingPasteIntentUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, intent)
				if !errors.Is(err, errStoreStep) {
					t.Fatalf("SaveIncomingPasteIntentUnderLock %s failure = %v", failure, err)
				}
				return
			}
			receipt := validMessageReceipt(fixture.message, fixture.messageDigest)
			receipt.RecordedAt = fixture.incomingRecordedAt
			receipt.PastedAt = intent.IntendedAt.Add(time.Second)
			_, _, err := fixture.store.SaveIncomingMessageReceiptUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, receipt)
			if !errors.Is(err, errStoreStep) {
				t.Fatalf("SaveIncomingMessageReceiptUnderLock %s failure = %v", failure, err)
			}
		})
	}
}

func TestMessageSynchestraStorePropagatesPublicationPipelineErrors(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"marshal", "publish", "read"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			store, request, digest, lock, message, raw := smCovSynchMessageFixture(t, true, true, true)
			t.Cleanup(func() { _ = lock.Close() })
			identity := smCovSynchIdentity(request, digest, DigestBytes(raw), message.MessageID)
			switch failure {
			case "marshal":
				store.ops = failingMarshal()
			case "publish":
				store.ops = failingPublish(messageSynchestraDispatchFileName)
			case "read":
				store.ops = failingRead(messageSynchestraDispatchFileName)
			}
			if _, _, err := store.SaveOutgoingMessageSynchestraDispatchUnderLock(lock, request.HandoffID, digest, identity); !errors.Is(err, errStoreStep) {
				t.Fatalf("SaveOutgoingMessageSynchestraDispatchUnderLock %s failure = %v", failure, err)
			}
		})
	}
}

func TestSuccessorAddressStorePropagatesPublicationPipelineErrors(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"marshal", "publish", "read", "retain root", "load retain root", "durable encode", "supplied encode"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			store, request, digest, _ := admittedRouteRequest(t, false)
			route := Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
				Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "runner-1"}}
			if _, _, err := store.SaveRoute(route); err != nil {
				t.Fatal(err)
			}
			receipt := validReceipt(request, digest)
			if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); err != nil {
				t.Fatal(err)
			}
			lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = lock.Close() })
			switch failure {
			case "marshal":
				store.ops = failingMarshal()
			case "publish":
				store.ops = failingPublish(successorAddressFileName(receipt.SuccessorWBSessionID))
			case "read":
				store.ops = failingRead(successorAddressFileName(receipt.SuccessorWBSessionID))
			case "retain root", "load retain root":
				store.ops = &storeOps{retainRoot: func(*ExecutionLock, string, Request, Digest) (*os.File, error) {
					return nil, errStoreStep
				}}
			case "durable encode", "supplied encode":
				calls := 0
				failOn := 1
				if failure == "supplied encode" {
					failOn = 2
				}
				store.ops = &storeOps{encodeReceipt: func(value Receipt) ([]byte, error) {
					calls++
					if calls == failOn {
						return nil, errStoreStep
					}
					return EncodeReceipt(value)
				}}
			}
			if failure == "load retain root" {
				store.ops = nil
				if _, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, receipt); err != nil {
					t.Fatal(err)
				}
				store.ops = &storeOps{retainRoot: func(*ExecutionLock, string, Request, Digest) (*os.File, error) {
					return nil, errStoreStep
				}}
				if _, err := store.LoadSuccessorAddressUnderLock(lock, request.HandoffID, digest); !errors.Is(err, errStoreStep) {
					t.Fatalf("LoadSuccessorAddressUnderLock retain root failure = %v", err)
				}
				return
			}
			if _, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, receipt); !errors.Is(err, errStoreStep) {
				t.Fatalf("SaveSuccessorAddressUnderLock %s failure = %v", failure, err)
			}
		})
	}
}

func TestOutgoingMessageRepairPropagatesEncodingAndPublishFailures(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"marshal", "publish"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			fixture := smCovMsgNewFixture(t, true, false, time.Time{})
			path := filepath.Join(fixture.smCovMsgEntryDir(MessageDirectionOutgoing, fixture.message.MessageID), messagePayloadFileName)
			smCovMsgWrite(t, path, fixture.messageRaw, 0o600)
			if failure == "marshal" {
				fixture.store.ops = failingMarshal()
			} else {
				fixture.store.ops = failingPublish(messageRecordFileName)
			}
			if _, err := fixture.store.ResumeOutgoingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.message.MessageID); !errors.Is(err, errStoreStep) {
				t.Fatalf("ResumeOutgoingMessageUnderLock %s failure = %v", failure, err)
			}
		})
	}
}

func TestSuccessorAddressValidationRefusesInvalidNestedCourier(t *testing.T) {
	t.Parallel()
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	receipt := validReceipt(request, digest)
	route := Route{SchemaVersion: RouteSchemaVersion, HandoffID: request.HandoffID,
		RequestDigest: digest, TargetMachine: request.TargetMachine, Courier: CourierSynchestra,
		Synchestra: &SynchestraConfig{Runner: "runner-1"}}
	address := successorAddressFor(request, digest, receipt, route)
	address.Route.Synchestra = nil
	if err := validateSuccessorAddress(address, request.SuccessorWBSessionID); err == nil {
		t.Fatal("successor address accepted a courier without its required runner")
	}
}

func TestRetainedRequestRaceRefusesChangedDurableBytes(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"before retain validation", "before retain changed", "readmit malformed", "readmit changed"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			fixture := smCovNewLockFixture(t)
			lock := fixture.smCovAcquire(t)
			t.Cleanup(func() { _ = lock.Close() })
			requestPath := filepath.Join(smCovHandoffDir(fixture), requestFileName)
			change := func() {
				var raw []byte
				if failure == "readmit changed" || failure == "before retain changed" {
					changed := fixture.request
					changed.Branch += "-changed"
					var err error
					raw, err = EncodeRequest(changed)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					raw = []byte("{broken-json")
				}
				if err := os.WriteFile(requestPath, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "before retain validation" || failure == "before retain changed" {
				fixture.store.ops = &storeOps{afterHandoffRetain: change}
				if _, handoff, err := fixture.store.retainHandoffUnderLock(lock, fixture.request.HandoffID, fixture.digest); err == nil {
					_ = handoff.Close()
					t.Fatal("retained a request changed after the lock proof")
				}
				return
			}
			fixture.store.ops = &storeOps{afterReadmitRetain: change}
			if _, err := fixture.store.ReadmitUnderLock(lock, fixture.request.HandoffID, fixture.digest, fixture.raw); err == nil {
				t.Fatal("readmitted request bytes changed after retaining the aggregate")
			}
		})
	}
}

func TestReceiptAndEventPublishBoundaryPropagatesRaceAndWriteFailures(t *testing.T) {
	t.Parallel()
	t.Run("receipt unlinked after publication", func(t *testing.T) {
		t.Parallel()
		fixture := smCovNewLockFixture(t)
		handoff, err := fixture.store.openHandoff(fixture.request.HandoffID, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = handoff.Close() })
		publish := func(directory *os.File, name string, raw []byte, mode os.FileMode) (bool, error) {
			created, err := publishImmutableAt(directory, name, raw, mode, nil)
			if err != nil {
				return false, err
			}
			if err := unix.Unlinkat(int(directory.Fd()), name, 0); err != nil {
				t.Fatal(err)
			}
			return created, nil
		}
		if _, _, err := saveReceiptAtWithPublish(handoff, fixture.request, fixture.digest, validReceipt(fixture.request, fixture.digest), publish); err == nil {
			t.Fatal("saveReceiptAt accepted a receipt removed after publication")
		}
	})
	t.Run("event publisher failure", func(t *testing.T) {
		t.Parallel()
		fixture := smCovNewLockFixture(t)
		handoff, err := fixture.store.openHandoff(fixture.request.HandoffID, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = handoff.Close() })
		publish := func(*os.File, string, []byte, os.FileMode) (bool, error) { return false, errStoreStep }
		_, err = appendEventAtWithPublish(handoff, fixture.request.HandoffID, fixture.digest, HandoffEvent{Phase: PhaseOffered, At: time.Now()}, publish)
		if !errors.Is(err, errStoreStep) {
			t.Fatalf("appendEventAt publisher failure = %v", err)
		}
	})
}

func TestPendingPublicationRepairPropagatesEachFilesystemFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"initial stat", "scan open", "directory read", "pending unlink", "directory sync", "final stat"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			path := t.TempDir()
			finalPath := filepath.Join(path, "artifact")
			if err := os.WriteFile(finalPath, []byte("exact"), 0o600); err != nil {
				t.Fatal(err)
			}
			pending := ".pending-" + strings.Repeat("a", 32)
			if err := os.Link(finalPath, filepath.Join(path, pending)); err != nil {
				t.Fatal(err)
			}
			directory, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = directory.Close() })
			ops := repairOps{
				open: openatWithIntFlags, fstat: unix.Fstat,
				readDir: func(file *os.File) ([]os.DirEntry, error) { return file.ReadDir(-1) },
				unlink:  unix.Unlinkat, fsync: unix.Fsync,
			}
			switch failure {
			case "initial stat":
				ops.fstat = func(int, *unix.Stat_t) error { return errStoreStep }
			case "scan open":
				ops.open = func(fd int, name string, flags int, mode uint32) (int, error) {
					if name == "." {
						return -1, errStoreStep
					}
					return openatWithIntFlags(fd, name, flags, mode)
				}
			case "directory read":
				ops.readDir = func(*os.File) ([]os.DirEntry, error) { return nil, errStoreStep }
			case "pending unlink":
				ops.unlink = func(int, string, int) error { return errStoreStep }
			case "directory sync":
				ops.fsync = func(int) error { return errStoreStep }
			case "final stat":
				calls := 0
				ops.fstat = func(fd int, stat *unix.Stat_t) error {
					calls++
					if calls == 2 {
						return errStoreStep
					}
					return unix.Fstat(fd, stat)
				}
			}
			if err := repairPendingLinkAtWithOps(directory, "artifact", ops); !errors.Is(err, errStoreStep) {
				t.Fatalf("repairPendingLinkAtWithOps %s failure = %v", failure, err)
			}
		})
	}
}

func TestImmutablePublisherReportsPublishedDescriptorFailures(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"fstat", "close"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			directory, err := os.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = directory.Close() })
			ops := publishOps{fstat: unix.Fstat, close: (*os.File).Close}
			if failure == "fstat" {
				ops.fstat = func(int, *unix.Stat_t) error { return errStoreStep }
			} else {
				ops.close = func(file *os.File) error {
					_ = file.Close()
					return errStoreStep
				}
			}
			read := func(buf []byte) (int, error) {
				for index := range buf {
					buf[index] = byte(index)
				}
				return len(buf), nil
			}
			if _, err := publishImmutableAtWithOps(directory, "artifact", []byte("exact"), 0o600, nil, read, ops); !errors.Is(err, errStoreStep) {
				t.Fatalf("publishImmutableAtWithOps %s failure = %v", failure, err)
			}
		})
	}
}

func TestLocalResolversAndCanonicalEncoderPropagateFailures(t *testing.T) {
	t.Parallel()
	t.Run("execution lock root resolution", func(t *testing.T) {
		t.Parallel()
		fixture := smCovNewLockFixture(t)
		lock, err := fixture.store.acquireExecutionLockWithDeps(context.Background(), fixture.request.HandoffID, fixture.digest,
			unix.Fchmod, unix.Flock, unix.Fstat, func(string) (string, error) { return "", errStoreStep })
		if lock != nil {
			_ = lock.Close()
			t.Fatal("lock returned after root resolution failure")
		}
		if !errors.Is(err, errStoreStep) {
			t.Fatalf("lock root resolution error = %v", err)
		}
	})
	t.Run("store root resolution", func(t *testing.T) {
		t.Parallel()
		store := NewStore(t.TempDir())
		root, err := store.openRootWithAbs(false, func(string) (string, error) { return "", errStoreStep })
		if root != nil {
			_ = root.Close()
			t.Fatal("store root returned after path resolution failure")
		}
		if !errors.Is(err, errStoreStep) {
			t.Fatalf("store root resolution error = %v", err)
		}
	})
	t.Run("canonical message encoder", func(t *testing.T) {
		t.Parallel()
		fixture := smCovMsgNewFixture(t, true, false, time.Date(2026, 8, 25, 12, 0, 1, 0, time.UTC))
		fixture.store.ops = &storeOps{encodeMessage: func(Message) ([]byte, error) { return nil, errStoreStep }}
		if _, err := fixture.store.AdmitIncomingMessageUnderLock(fixture.lock, fixture.request.HandoffID, fixture.digest, fixture.messageRaw, fixture.incomingRecordedAt); !errors.Is(err, errStoreStep) {
			t.Fatalf("message canonical encoding error = %v", err)
		}
	})
}

func TestCorroborationPreservesCanonicalMarshalFailure(t *testing.T) {
	t.Parallel()
	store, request, digest, _ := admittedRouteRequest(t, false)
	route := Route{HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "runner-1"}}
	if _, _, err := store.SaveRoute(route); err != nil {
		t.Fatal(err)
	}
	receipt := validReceipt(request, digest)
	if _, _, err := store.SaveReceipt(request.HandoffID, digest, receipt); err != nil {
		t.Fatal(err)
	}
	handoff, err := store.openHandoff(request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handoff.Close() })
	route.SchemaVersion = RouteSchemaVersion
	address := successorAddressFor(request, digest, receipt, route)
	raw, err := marshalJSON(address)
	if err != nil {
		t.Fatal(err)
	}
	_, err = corroborateSuccessorAddressAtWithMarshal(handoff, request, digest, receipt.SuccessorWBSessionID, raw,
		func(any) ([]byte, error) { return nil, errStoreStep })
	if !errors.Is(err, errStoreStep) {
		t.Fatalf("corroboration marshal failure = %v", err)
	}
}
