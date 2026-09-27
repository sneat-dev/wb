package sessionmove

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/unixcompat"
)

func TestProtocolHelpersPreserveCanonicalIdentity(t *testing.T) {
	t.Parallel()
	raw := []byte("exact admitted bytes")
	digest := DigestBytes(raw)
	if !digest.Matches(raw) || digest.Matches([]byte("different bytes")) || Digest("invalid").Matches(raw) {
		t.Fatal("digest match did not distinguish exact bytes and canonical spelling")
	}
	for _, field := range []struct{ message, action, wantMessage, wantAction string }{
		{" message \n", " next action\t", "message", "next action"},
		{"", "  ", "", ""},
	} {
		message, action := NormalizeSourceOfferContent(field.message, field.action)
		if message != field.wantMessage || action != field.wantAction {
			t.Fatalf("normalized offer = (%q, %q), want (%q, %q)", message, action, field.wantMessage, field.wantAction)
		}
		if DigestSourceOffer(field.message, field.action) != DigestSourceOffer(message, action) {
			t.Fatal("offer digest changed after canonical normalization")
		}
	}
	for _, value := range []string{
		"worklog:./run/" + strings.Repeat("a", 64),
		"worklog:effort/../" + strings.Repeat("a", 64),
	} {
		if _, err := ParseWorkLogReference(value); err == nil {
			t.Fatalf("unsafe work log reference %q was accepted", value)
		}
	}
	reference := WorkLogReference{EffortID: "effort-123", RunID: "run-456", ClaimID: strings.Repeat("a", 64)}
	parsed, err := ParseWorkLogReference(reference.String())
	if err != nil || parsed != reference {
		t.Fatalf("work log reference round trip = (%#v, %v), want %#v", parsed, err, reference)
	}
}

func TestExecutionLockCloseJoinsDescriptorFailuresAndRevokesAuthority(t *testing.T) {
	for _, which := range []string{"lock", "request", "handoff", "root"} {
		t.Run(which, func(t *testing.T) {
			fixture := smCovNewLockFixture(t)
			lock := fixture.smCovAcquire(t)
			var file *os.File
			switch which {
			case "lock":
				file = lock.file
			case "request":
				file = lock.requestFile
			case "handoff":
				file = lock.handoff
			case "root":
				file = lock.root
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err == nil {
				t.Fatalf("Close after %s descriptor failure discarded the error", which)
			}
			if lock.HeldForStore(fixture.root, fixture.request, fixture.digest) {
				t.Fatal("closed lock retained store authority")
			}
			if err := lock.Close(); err != nil {
				t.Fatalf("idempotent Close = %v", err)
			}
		})
	}
}

func TestAdmittedRequestDescriptorRejectsOversizeCorruptionAndDigestDrift(t *testing.T) {
	t.Parallel()
	fixture := smCovNewLockFixture(t)
	for _, test := range []struct {
		name string
		raw  []byte
		want string
	}{
		{"oversized", []byte(strings.Repeat("x", maxExecutionLockRequestBytes+1)), "bounded immutable file"},
		{"malformed", []byte("{"), "decode admitted handoff request"},
		{"different bytes", append(append([]byte(nil), fixture.raw...), ' '), "digest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, requestFileName)
			if err := os.WriteFile(path, test.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			if _, err := readAdmittedRequestFile(file, fixture.request.HandoffID, fixture.digest); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("admitted request error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAdmittedRequestDescriptorPropagatesSeekReadAndBoundFailures(t *testing.T) {
	t.Parallel()
	fixture := smCovNewLockFixture(t)
	path := filepath.Join(smCovHandoffDir(fixture), requestFileName)
	for _, test := range []struct {
		name string
		seek func(int64, int) (int64, error)
		read func(io.Reader) ([]byte, error)
		want string
	}{
		{"seek", func(int64, int) (int64, error) { return 0, io.ErrUnexpectedEOF }, io.ReadAll, "seek admitted handoff request"},
		{"read", func(int64, int) (int64, error) { return 0, nil }, func(io.Reader) ([]byte, error) { return nil, io.ErrUnexpectedEOF }, "read admitted handoff request"},
		{"overflow", func(int64, int) (int64, error) { return 0, nil }, func(io.Reader) ([]byte, error) {
			return []byte(strings.Repeat("x", maxExecutionLockRequestBytes+1)), nil
		}, "exceeds"},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			if _, err := readAdmittedRequestFileWithIO(file, fixture.request.HandoffID, fixture.digest, test.seek, test.read); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readAdmittedRequestFileWithIO error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestStableExecutionLockCreationReportsRepeatedFirstWriterRaces(t *testing.T) {
	t.Parallel()
	calls := 0
	_, err := openExecutionLockAtWithOpen(42, func(_ int, name string, flags int, mode uint32) (int, error) {
		calls++
		if name != executionLockFileName {
			t.Fatalf("opened %q, want stable lock name", name)
		}
		if calls%2 == 1 {
			if flags&syscall.O_CREAT != 0 || mode != 0 {
				t.Fatalf("precheck flags=%x mode=%o", flags, mode)
			}
			return -1, syscall.ENOENT
		}
		if flags&syscall.O_CREAT == 0 || mode != 0o600 {
			t.Fatalf("create flags=%x mode=%o", flags, mode)
		}
		return -1, syscall.EEXIST
	})
	if err == nil || !strings.Contains(err.Error(), "did not converge") || calls != 6 {
		t.Fatalf("three first-writer races = (%d calls, %v), want six calls and convergence error", calls, err)
	}
}

func TestExecutionLockRetainRefusesClosureBetweenProofAndDuplicate(t *testing.T) {
	for _, which := range []string{"handoff", "root"} {
		t.Run(which, func(t *testing.T) {
			fixture := smCovNewLockFixture(t)
			lock := fixture.smCovAcquire(t)
			lock.afterProof = func() {
				if err := lock.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var retained *os.File
			var err error
			if which == "handoff" {
				retained, err = lock.RetainHandoffForStore(fixture.root, fixture.request, fixture.digest)
			} else {
				retained, err = lock.RetainStoreRootForStore(fixture.root, fixture.request, fixture.digest)
			}
			if err == nil {
				_ = retained.Close()
				t.Fatalf("retained %s after closure between proof and duplicate", which)
			}
		})
	}
}

func TestExecutionLockRetriesAfterOneContentionTimer(t *testing.T) {
	t.Parallel()
	fixture := smCovNewLockFixture(t)
	calls := 0
	flock := func(fd, operation int) error {
		calls++
		if calls == 1 {
			return syscall.EWOULDBLOCK
		}
		return unix.Flock(fd, operation)
	}
	lock, err := fixture.store.acquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest, unix.Fchmod, flock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if calls != 2 || !lock.HeldForStore(fixture.root, fixture.request, fixture.digest) {
		t.Fatalf("contention retry = %d flock calls and held=%t, want two calls and authority", calls, lock.HeldForStore(fixture.root, fixture.request, fixture.digest))
	}
}

func TestExecutionLockReportsInspectionFailureAfterOpeningLock(t *testing.T) {
	t.Parallel()
	fixture := smCovNewLockFixture(t)
	lock, err := fixture.store.acquireExecutionLockWithStat(context.Background(), fixture.request.HandoffID, fixture.digest, unix.Fchmod, unix.Flock, func(int, *unix.Stat_t) error {
		return syscall.EIO
	})
	if lock != nil {
		_ = lock.Close()
		t.Fatal("acquireExecutionLockWithStat retained a lock after failed inspection")
	}
	if !errors.Is(err, syscall.EIO) || !strings.Contains(err.Error(), "inspect handoff execution lock") {
		t.Fatalf("failed lock inspection = %v", err)
	}
}

func TestSecureDirectoryOpenersReportDescriptorInspectionFailure(t *testing.T) {
	t.Parallel()
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	failStat := func(int, *unix.Stat_t) error { return syscall.EIO }
	for _, which := range []string{"message", "successors", "events"} {
		t.Run(which, func(t *testing.T) {
			var directory *os.File
			var err error
			switch which {
			case "message":
				directory, err = openSecureDirectoryAtWithStat(root, "messages", true, "message", failStat)
			case "successors":
				directory, err = openSuccessorAddressesAtWithStat(root, true, failStat)
			case "events":
				directory, err = openEventsAtWithStat(root, true, failStat)
			}
			if directory != nil {
				_ = directory.Close()
				t.Fatalf("%s directory returned despite failed descriptor inspection", which)
			}
			if !errors.Is(err, syscall.EIO) {
				t.Fatalf("%s directory inspection error = %v", which, err)
			}
		})
	}
}

func TestRetainAuthorityRejectsRevokedAndChangedBindings(t *testing.T) {
	t.Parallel()
	fixture := smCovNewLockFixture(t)
	lock := fixture.smCovAcquire(t)
	defer func() { _ = lock.Close() }()
	wrong := DigestBytes([]byte("other request"))
	if retained, err := lock.RetainHandoffForStore(fixture.root, fixture.request, wrong); err == nil {
		_ = retained.Close()
		t.Fatal("retained handoff with wrong digest")
	}
	if retained, err := lock.RetainStoreRootForStore(fixture.root, fixture.request, wrong); err == nil {
		_ = retained.Close()
		t.Fatal("retained root with wrong digest")
	}
	if err := lock.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	if retained, err := lock.RetainHandoffForStore(fixture.root, fixture.request, fixture.digest); err == nil {
		_ = retained.Close()
		t.Fatal("retained handoff after close")
	}
	if retained, err := lock.RetainStoreRootForStore(fixture.root, fixture.request, fixture.digest); err == nil {
		_ = retained.Close()
		t.Fatal("retained root after close")
	}
}

func TestDurablePublicationReportsDirectoryPermissionFailures(t *testing.T) {
	// Each case retains a readable directory descriptor while removing write
	// permission. The immutable publisher must report the syscall failure.
	t.Run("admitted request", func(t *testing.T) {
		request := validRequest()
		raw, err := EncodeRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		store := NewStore(filepath.Join(t.TempDir(), "root"))
		handoffPath := filepath.Join(store.Root, request.HandoffID)
		if err := os.MkdirAll(handoffPath, 0o700); err != nil {
			t.Fatal(err)
		}
		makeDirectoryReadOnly(t, handoffPath)
		if _, err := store.Admit(raw, DigestBytes(raw)); err == nil || !strings.Contains(err.Error(), "persist handoff request") {
			t.Fatalf("Admit into read-only handoff = %v", err)
		}
	})
	for _, which := range []string{"receipt", "event", "private handover"} {
		t.Run(which, func(t *testing.T) {
			request := validRequestWithInlineHandover("exact private continuation")
			raw, err := EncodeRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			digest := DigestBytes(raw)
			store := NewStore(t.TempDir())
			if _, err := store.Admit(raw, digest); err != nil {
				t.Fatal(err)
			}
			handoffPath := filepath.Join(store.Root, request.HandoffID)
			var lock *ExecutionLock
			if which == "private handover" {
				lock, err = store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.Close() }()
			}
			if which == "event" {
				if err := os.Mkdir(filepath.Join(handoffPath, eventsDirName), 0o700); err != nil {
					t.Fatal(err)
				}
				makeDirectoryReadOnly(t, filepath.Join(handoffPath, eventsDirName))
			} else {
				makeDirectoryReadOnly(t, handoffPath)
			}
			switch which {
			case "receipt":
				if _, _, err := store.SaveReceipt(request.HandoffID, digest, validReceipt(request, digest)); err == nil || !strings.Contains(err.Error(), "persist handoff receipt") {
					t.Fatalf("SaveReceipt into read-only handoff = %v", err)
				}
			case "event":
				if _, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseOffered, At: time.Now()}); err == nil || !strings.Contains(err.Error(), "not mode 0700") {
					t.Fatalf("AppendEvent into read-only events directory = %v", err)
				}
			case "private handover":
				if _, err := store.EnsureHandoverUnderLock(lock, request.HandoffID, digest); err == nil || !strings.Contains(err.Error(), "persist private handover") {
					t.Fatalf("EnsureHandoverUnderLock into read-only handoff = %v", err)
				}
			}
		})
	}
}

func makeDirectoryReadOnly(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
}

func TestCourierIdentityPublicationPreservesFilesystemFailure(t *testing.T) {
	store, request, digest, _ := admittedRouteRequest(t, false)
	handoffPath := filepath.Join(store.Root, request.HandoffID)
	route := Route{
		HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "hetzner-vm1"},
	}
	makeDirectoryReadOnly(t, handoffPath)
	if _, _, err := store.SaveRoute(route); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("SaveRoute into read-only handoff = %v", err)
	}
	if err := os.Chmod(handoffPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveRoute(route); err != nil {
		t.Fatal(err)
	}
	makeDirectoryReadOnly(t, handoffPath)
	identity := SynchestraDispatch{
		HandoffID: request.HandoffID, RequestDigest: digest, Runner: "hetzner-vm1",
		InvocationID: request.HandoffID, Handler: SynchestraSessionAcceptHandler, DispatchID: "dispatch-1",
	}
	if _, _, err := store.SaveSynchestraDispatch(identity); err == nil || !strings.Contains(err.Error(), "publish immutable synchestra dispatch identity") {
		t.Fatalf("SaveSynchestraDispatch into read-only handoff = %v", err)
	}
}

func TestEventAndSuccessorAddressPreserveTimestampEncodingErrors(t *testing.T) {
	store, request, digest, _ := admittedRouteRequest(t, false)
	badTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.AppendEvent(request.HandoffID, digest, HandoffEvent{Phase: PhaseOffered, At: badTime}); err == nil || !strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
		t.Fatalf("AppendEvent with unencodable timestamp = %v", err)
	}
	route := Route{
		HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: CourierSynchestra, Synchestra: &SynchestraConfig{Runner: "hetzner-vm1"},
	}
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
	defer func() { _ = lock.Close() }()
	receipt.StartedAt = badTime
	if _, _, err := store.SaveSuccessorAddressUnderLock(lock, request.HandoffID, digest, receipt); err == nil || !strings.Contains(err.Error(), wantYearOutOfRangeSubstring) {
		t.Fatalf("SaveSuccessorAddressUnderLock with unencodable receipt = %v", err)
	}
}

func TestEventAndStateRejectWrongAggregateIdentity(t *testing.T) {
	t.Parallel()
	fixture := smCovNewLockFixture(t)
	handoff, err := fixture.store.openHandoff(fixture.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handoff.Close() }()
	otherDigest := DigestBytes([]byte("other request"))
	if _, err := fixture.store.AppendEvent(fixture.request.HandoffID, otherDigest, HandoffEvent{Phase: PhaseOffered, At: time.Now()}); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("AppendEvent with wrong request digest = %v", err)
	}
	if _, err := appendEventAt(handoff, fixture.request.HandoffID, otherDigest, HandoffEvent{Phase: PhaseCompleted, At: time.Now()}); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("appendEventAt completed with wrong request digest = %v", err)
	}
	if _, err := loadStateAt(handoff, fixture.request.HandoffID, otherDigest); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("loadStateAt with wrong request digest = %v", err)
	}
	if err := os.WriteFile(filepath.Join(smCovHandoffDir(fixture), receiptFileName), []byte("{corrupt receipt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := appendEventAt(handoff, fixture.request.HandoffID, fixture.digest, HandoffEvent{Phase: PhaseCompleted, At: time.Now()}); err == nil || !strings.Contains(err.Error(), "decode durable handoff receipt") {
		t.Fatalf("appendEventAt with corrupt completed receipt = %v", err)
	}
	if err := os.WriteFile(filepath.Join(smCovHandoffDir(fixture), requestFileName), []byte("{corrupt request"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := appendEventAt(handoff, fixture.request.HandoffID, fixture.digest, HandoffEvent{Phase: PhaseCompleted, At: time.Now()}); err == nil || !strings.Contains(err.Error(), "decode durable handoff request") {
		t.Fatalf("appendEventAt with corrupt admitted request = %v", err)
	}
}

func TestEventStorageRejectsUnwritableAndUnsafeDirectories(t *testing.T) {
	t.Parallel()
	fixture := smCovNewLockFixture(t)
	handoffPath := smCovHandoffDir(fixture)
	handoff, err := fixture.store.openHandoff(fixture.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handoff.Close() }()
	makeDirectoryReadOnly(t, handoffPath)
	if events, err := openEventsAt(handoff, true); err == nil || !strings.Contains(err.Error(), "create handoff events directory") {
		_ = events.Close()
		t.Fatalf("openEventsAt under read-only handoff = %v", err)
	}
	if err := os.Chmod(handoffPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-events", filepath.Join(handoffPath, eventsDirName)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEventsAt(handoff, fixture.request.HandoffID, fixture.digest); err == nil {
		t.Fatal("loadEventsAt accepted a symlinked events directory")
	}
	other := smCovNewLockFixture(t)
	otherHandoff, err := other.store.openHandoff(other.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = otherHandoff.Close() }()
	if err := os.WriteFile(filepath.Join(smCovHandoffDir(other), eventsDirName), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEventsAt(otherHandoff, other.request.HandoffID, other.digest); err == nil {
		t.Fatal("loadEventsAt accepted a regular file in place of its events directory")
	}
}

func TestLoadEventsPropagatesDirectoryEnumerationFailure(t *testing.T) {
	t.Parallel()
	fixture := smCovNewLockFixture(t)
	handoff, err := fixture.store.openHandoff(fixture.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handoff.Close() }()
	events, err := openEventsAt(handoff, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = events.Close()
	_, err = loadEventsAtWithEntries(handoff, fixture.request.HandoffID, fixture.digest, func(*os.File) ([]os.DirEntry, error) {
		return nil, syscall.EIO
	})
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("loadEventsAt directory read failure = %v", err)
	}
}

func TestImmutablePublicationReportsFailureToRemovePreparedName(t *testing.T) {
	directory := t.TempDir()
	authority, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authority.Close() }()
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	injection := &filewrite.Injector{
		Step: filewrite.StepLink,
		Name: "artifact",
		Err:  syscall.EEXIST,
		Hook: func() {
			if err := os.Chmod(directory, 0o500); err != nil {
				t.Fatalf("make immutable directory read-only before cleanup: %v", err)
			}
		},
	}
	if _, err := publishImmutableAt(authority, "artifact", []byte("exact"), 0o600, injection); err == nil || !strings.Contains(err.Error(), "remove immutable temporary name") {
		t.Fatalf("publishImmutableAt after unlink permission loss = %v", err)
	}
}
