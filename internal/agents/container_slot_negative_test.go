//go:build !windows

package agents

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

const containerSlotHomeProbeEnv = "WB_CONTAINER_SLOT_HOME_PROBE"

// TestContainerSlotHomeProbeHelper is the isolated child that changes HOME. It
// is skipped in a normal run and only executes when the parent starts this test
// binary with containerSlotHomeProbeEnv set. The child owns its HOME mutations;
// no test in the parent process ever touches the real user home.
func TestContainerSlotHomeProbeHelper(t *testing.T) {
	if os.Getenv(containerSlotHomeProbeEnv) != "1" {
		t.Skip("isolated child of TestContainerSlotDefaultStateDirNeedsAUsableHome")
	}
	// An empty HOME must be reported as an unresolved home, not used as a
	// relative state directory.
	t.Setenv("HOME", "")
	if _, err := ContainerSlotStateDir(); err == nil {
		t.Fatal("ContainerSlotStateDir accepted an empty HOME")
	} else if !strings.Contains(err.Error(), "resolve the user home directory") {
		t.Fatalf("empty HOME error = %v, want the unresolved home named", err)
	}
	// Opening the default slot in that state must fail for the same reason
	// instead of creating a relative state directory.
	if slot, err := OpenContainerSlot(""); err == nil {
		_ = slot.Close()
		t.Fatal("OpenContainerSlot with no state directory accepted an unresolved HOME")
	} else if !strings.Contains(err.Error(), "resolve the user home directory") {
		t.Fatalf("default slot error = %v, want the unresolved home named", err)
	}
	// A whitespace-only HOME is empty after trimming and is refused with the
	// empty-path reason rather than becoming a relative state directory.
	t.Setenv("HOME", "   ")
	if _, err := ContainerSlotStateDir(); err == nil {
		t.Fatal("ContainerSlotStateDir accepted a whitespace-only HOME")
	} else if !strings.Contains(err.Error(), "empty path") {
		t.Fatalf("whitespace HOME error = %v, want the empty home path named", err)
	}
	// A usable HOME selects the fixed per-user default directory when the
	// caller passes no state directory.
	home := t.TempDir()
	t.Setenv("HOME", home)
	slot, err := OpenContainerSlot("")
	if err != nil {
		t.Fatalf("OpenContainerSlot with the default state directory: %v", err)
	}
	defer func() { _ = slot.Close() }()
	stateDir := filepath.Join(home, ".config", "wb", "linux-execution")
	if _, err := os.Stat(filepath.Join(stateDir, "container.lock")); err != nil {
		t.Fatalf("the default state directory did not hold the lock file: %v", err)
	}
	if _, ok := slot.Reservation(); ok {
		t.Fatal("a fresh default slot must hold no reservation")
	}
}

// TestContainerSlotDefaultStateDirNeedsAUsableHome proves the default state
// directory is only derived from a real home: an empty or whitespace-only HOME
// is refused, and a usable one resolves the fixed per-user directory. The HOME
// mutations happen in one isolated child process.
func TestContainerSlotDefaultStateDirNeedsAUsableHome(t *testing.T) {
	t.Parallel()
	command := exec.Command(os.Args[0], "-test.run=^TestContainerSlotHomeProbeHelper$")
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "HOME=") || strings.HasPrefix(entry, "XDG_CONFIG_HOME=") {
			continue
		}
		environment = append(environment, entry)
	}
	command.Env = append(environment, containerSlotHomeProbeEnv+"=1")
	if f := flag.Lookup("test.gocoverdir"); testing.CoverMode() != "" && f != nil && f.Value.String() != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+f.Value.String())
		command.Env = append(command.Env, "GOCOVERDIR="+f.Value.String())
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("HOME probe child = %v\n%s", err, output)
	}
}

// TestContainerSlotOpenReportsAnUnopenableLockFile converts the lock-file open
// into a deterministic failure and proves no holder is returned and no lock is
// retained afterwards.
func TestContainerSlotOpenReportsAnUnopenableLockFile(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	openFailure := errors.New("lock file cannot be opened")
	files := &containerSlotFiles{openFile: func(string, int, os.FileMode) (*os.File, error) {
		return nil, openFailure
	}}
	slot, err := openContainerSlot(dir, files)
	if slot != nil {
		_ = slot.Close()
		t.Fatal("an unopenable lock file must not yield a holder")
	}
	if !errors.Is(err, openFailure) {
		t.Fatalf("open error = %v, want %v", err, openFailure)
	}
	if !strings.Contains(err.Error(), "open container slot lock") {
		t.Fatalf("open error = %v, want the lock file named", err)
	}
	// No descriptor was opened, so a real holder must be immediately available.
	next, err := OpenContainerSlot(dir)
	if err != nil {
		t.Fatalf("the slot must be acquirable after a failed open: %v", err)
	}
	_ = next.Close()
}

// TestContainerSlotOpenReportsANonBusyLockFailure proves a lock error that is
// not an already-held lock is reported as a failure rather than as the busy
// refusal, and that the descriptor is released.
func TestContainerSlotOpenReportsANonBusyLockFailure(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	lockFailure := errors.New("the filesystem cannot lock this file")
	files := &containerSlotFiles{lock: func(*os.File) error { return lockFailure }}
	slot, err := openContainerSlot(dir, files)
	if slot != nil {
		_ = slot.Close()
		t.Fatal("a failed lock must not yield a holder")
	}
	var busy *ContainerSlotBusyError
	if errors.As(err, &busy) {
		t.Fatalf("a non-busy lock error must not be reported as busy: %v", err)
	}
	if !errors.Is(err, lockFailure) {
		t.Fatalf("lock error = %v, want %v", err, lockFailure)
	}
	if !strings.Contains(err.Error(), "lock container slot") {
		t.Fatalf("lock error = %v, want the lock file named", err)
	}
	next, err := OpenContainerSlot(dir)
	if err != nil {
		t.Fatalf("the failed holder must release its descriptor: %v", err)
	}
	_ = next.Close()
}

// TestContainerSlotReservationWriteFailuresKeepTheSlotBlocked fails one step of
// the durable write sequence at a time. Every failure must be reported, keep the
// in-memory reservation pending, leave no temporary file behind, and only
// publish the durable record when the publication step itself succeeded.
func TestContainerSlotReservationWriteFailuresKeepTheSlotBlocked(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		step    filewrite.Step
		want    string
		durable bool
	}{
		{"staging the temporary file", filewrite.StepOpenOrCreate, "stage container reservation", false},
		{"protecting the temporary file", filewrite.StepChmod, "protect staged container reservation", false},
		{"writing the temporary file", filewrite.StepWrite, "write staged container reservation", false},
		{"syncing the temporary file", filewrite.StepSync, "sync staged container reservation", false},
		{"closing the temporary file", filewrite.StepClose, "close staged container reservation", false},
		{"publishing by rename", filewrite.StepRename, "publish container reservation", false},
		{"syncing the state directory", filewrite.StepDirSync, "sync container slot state directory", true},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			failure := fmt.Errorf("injected %s failure", testCase.step)
			dir := testContainerSlotStateDir(t)
			slot := openTestContainerSlot(t, dir)
			slot.files = &containerSlotFiles{injector: &filewrite.Injector{Step: testCase.step, Err: failure}}

			err := slot.Reserve(testContainerReservation())
			if err == nil {
				t.Fatal("an injected write failure must refuse the reservation")
			}
			if !errors.Is(err, failure) {
				t.Fatalf("reserve error = %v, want %v", err, failure)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("reserve error = %v, want %q", err, testCase.want)
			}
			pending, ok := slot.Reservation()
			if !ok || pending.RunID != testContainerRunID {
				t.Fatalf("a failed write must keep the reservation pending, got %#v %v", pending, ok)
			}
			if err := slot.Reserve(testContainerReservation()); err == nil {
				t.Fatal("a failed write must leave the slot blocked")
			}
			_, statErr := os.Stat(filepath.Join(dir, "reservation.json"))
			if testCase.durable {
				if statErr != nil {
					t.Fatalf("a failed directory sync must keep the published reservation: %v", statErr)
				}
			} else if !os.IsNotExist(statErr) {
				t.Fatalf("a failed write must not publish a reservation, stat error = %v", statErr)
			}
			temporaries, globErr := filepath.Glob(filepath.Join(dir, ".reservation-*.json.tmp"))
			if globErr != nil {
				t.Fatalf("glob staged reservations: %v", globErr)
			}
			if len(temporaries) != 0 {
				t.Fatalf("a failed write left staged files behind: %v", temporaries)
			}
		})
	}
}

// TestContainerSlotRecordContainerIDFailureKeepsTheRecordedIdentityEmpty proves
// a failed save of the created container ID neither mutates the in-memory
// reservation nor the durable record.
func TestContainerSlotRecordContainerIDFailureKeepsTheRecordedIdentityEmpty(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	slot := openTestContainerSlot(t, dir)
	if err := slot.Reserve(testContainerReservation()); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	saveFailure := errors.New("state directory is full")
	slot.persist = func(ContainerReservation) error { return saveFailure }
	err := slot.RecordContainerID(testContainerID)
	if !errors.Is(err, saveFailure) {
		t.Fatalf("record error = %v, want %v", err, saveFailure)
	}
	if !strings.Contains(err.Error(), "persist container ID") {
		t.Fatalf("record error = %v, want the failed persistence named", err)
	}
	inMemory, ok := slot.Reservation()
	if !ok || inMemory.ContainerID != "" {
		t.Fatalf("a failed save must not record the container ID, got %#v %v", inMemory, ok)
	}
	if err := slot.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	durable, ok := openTestContainerSlot(t, dir).Reservation()
	if !ok || durable.ContainerID != "" {
		t.Fatalf("a failed save must not record the durable container ID, got %#v %v", durable, ok)
	}
}

// TestContainerSlotClearReportsAReservationThatCannotBeRemoved proves a durable
// record that cannot be removed is reported and keeps the reservation, rather
// than looking like a cleared slot.
func TestContainerSlotClearReportsAReservationThatCannotBeRemoved(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	slot := openTestContainerSlot(t, dir)
	if err := slot.Reserve(testContainerReservation()); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	path := filepath.Join(dir, "reservation.json")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove the file so the path can become a non-empty directory: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("replace the reservation with a directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatalf("make the replacement directory non-empty: %v", err)
	}
	err := slot.ClearReservation(func(ContainerReservation) error { return nil })
	if err == nil {
		t.Fatal("a reservation that cannot be removed must not clear the slot")
	}
	if !strings.Contains(err.Error(), "remove container reservation") {
		t.Fatalf("clear error = %v, want the failed removal named", err)
	}
	if _, ok := slot.Reservation(); !ok {
		t.Fatal("a failed removal must keep the reservation blocked")
	}
}

// TestContainerSlotClearReportsAFailedDirectorySync proves a proof-of-absence
// that removes the file but cannot make the removal durable is reported and
// keeps the reservation, and that a later successful clear recovers.
func TestContainerSlotClearReportsAFailedDirectorySync(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	slot := openTestContainerSlot(t, dir)
	if err := slot.Reserve(testContainerReservation()); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	syncFailure := errors.New("the state directory cannot be synced")
	slot.files = &containerSlotFiles{injector: &filewrite.Injector{Step: filewrite.StepDirSync, Err: syncFailure}}
	err := slot.ClearReservation(func(ContainerReservation) error { return nil })
	if !errors.Is(err, syncFailure) {
		t.Fatalf("clear error = %v, want %v", err, syncFailure)
	}
	if _, ok := slot.Reservation(); !ok {
		t.Fatal("a failed durability sync must keep the reservation blocked")
	}
	slot.files = nil
	if err := slot.ClearReservation(func(ContainerReservation) error { return nil }); err != nil {
		t.Fatalf("a repeat clear without the injected failure: %v", err)
	}
	if _, ok := slot.Reservation(); ok {
		t.Fatal("the repeat clear must release the reservation")
	}
}

// TestContainerSlotCloseHandlesAnEmptyHolder proves the defensive Close paths: a
// nil receiver, and a holder that never owned a descriptor. Neither may panic
// or report a spurious failure.
func TestContainerSlotCloseHandlesAnEmptyHolder(t *testing.T) {
	t.Parallel()
	var missing *ContainerSlot
	if err := missing.Close(); err != nil {
		t.Fatalf("nil slot Close = %v, want nil", err)
	}
	empty := &ContainerSlot{}
	if err := empty.Close(); err != nil {
		t.Fatalf("a holder with no descriptor Close = %v, want nil", err)
	}
	if !empty.closed {
		t.Fatal("Close must mark the holder closed")
	}
	if err := empty.Close(); err != nil {
		t.Fatalf("repeated Close = %v, want nil", err)
	}
}

// TestContainerSlotLoadReportsAnUnopenableReservation proves an existing but
// unopenable reservation is a refusal, not a free slot.
func TestContainerSlotLoadReportsAnUnopenableReservation(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	openFailure := errors.New("the reservation cannot be opened")
	files := &containerSlotFiles{open: func(string) (*os.File, error) { return nil, openFailure }}
	slot, err := openContainerSlot(dir, files)
	if slot != nil {
		_ = slot.Close()
		t.Fatal("an unopenable reservation must not yield a holder")
	}
	if !errors.Is(err, openFailure) {
		t.Fatalf("load error = %v, want %v", err, openFailure)
	}
	if !strings.Contains(err.Error(), "open container reservation") {
		t.Fatalf("load error = %v, want the reservation named", err)
	}
}

// TestContainerSlotLoadReportsAnUnreadableReservation proves a read failure
// after a successful open is a refusal, not a free slot.
func TestContainerSlotLoadReportsAnUnreadableReservation(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	raw, err := json.Marshal(testContainerReservation())
	if err != nil {
		t.Fatalf("marshal reservation: %v", err)
	}
	writeContainerSlotFile(t, filepath.Join(dir, "reservation.json"), raw, 0o600)
	readFailure := errors.New("the reservation cannot be read")
	files := &containerSlotFiles{readAll: func(io.Reader) ([]byte, error) { return nil, readFailure }}
	slot, err := openContainerSlot(dir, files)
	if slot != nil {
		_ = slot.Close()
		t.Fatal("an unreadable reservation must not yield a holder")
	}
	if !errors.Is(err, readFailure) {
		t.Fatalf("load error = %v, want %v", err, readFailure)
	}
	if !strings.Contains(err.Error(), "read container reservation") {
		t.Fatalf("load error = %v, want the reservation read named", err)
	}
}

// TestContainerSlotPrepareReportsAStateDirectoryThatCannotBeCreated proves a
// blocked parent is reported instead of a slot being opened somewhere else.
func TestContainerSlotPrepareReportsAStateDirectoryThatCannotBeCreated(t *testing.T) {
	t.Parallel()
	blocking := filepath.Join(t.TempDir(), "blocking")
	if err := os.WriteFile(blocking, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write the blocking file: %v", err)
	}
	slot, err := OpenContainerSlot(filepath.Join(blocking, "state"))
	if slot != nil {
		_ = slot.Close()
		t.Fatal("a state directory below a file must not yield a holder")
	}
	if err == nil {
		t.Fatal("a state directory that cannot be created must be refused")
	}
	if !strings.Contains(err.Error(), "create container slot state directory") {
		t.Fatalf("error = %v, want the failed creation named", err)
	}
}

// TestContainerSlotPrepareReportsAnUninspectableStateDirectory proves a state
// directory that was created but can no longer be inspected is refused.
func TestContainerSlotPrepareReportsAnUninspectableStateDirectory(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")
	inspectFailure := errors.New("the state directory cannot be inspected")
	files := &containerSlotFiles{lstat: func(string) (os.FileInfo, error) { return nil, inspectFailure }}
	slot, err := openContainerSlot(dir, files)
	if slot != nil {
		_ = slot.Close()
		t.Fatal("an uninspectable state directory must not yield a holder")
	}
	if !errors.Is(err, inspectFailure) {
		t.Fatalf("error = %v, want %v", err, inspectFailure)
	}
	if !strings.Contains(err.Error(), "inspect container slot state directory") {
		t.Fatalf("error = %v, want the failed inspection named", err)
	}
}

// TestContainerSlotInspectReportsAPathItCannotStat proves the private-file
// inspector reports a lookup failure that is neither a missing file nor a
// permission refusal.
func TestContainerSlotInspectReportsAPathItCannotStat(t *testing.T) {
	t.Parallel()
	base := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(base, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write the base file: %v", err)
	}
	err := inspectPrivateRegularFile(filepath.Join(base, "child"))
	if err == nil {
		t.Fatal("a path below a file must not be accepted")
	}
	if !strings.Contains(err.Error(), "inspect") {
		t.Fatalf("error = %v, want the failed inspection named", err)
	}
}

// TestContainerSlotSyncReportsAMissingStateDirectory proves the durability sync
// reports an unopenable state directory instead of pretending it synced.
func TestContainerSlotSyncReportsAMissingStateDirectory(t *testing.T) {
	t.Parallel()
	err := syncContainerSlotDir(filepath.Join(t.TempDir(), "missing"), nil)
	if err == nil {
		t.Fatal("a missing state directory must not sync silently")
	}
	if !strings.Contains(err.Error(), "for sync") {
		t.Fatalf("error = %v, want the failed open named", err)
	}
}

// TestContainerSlotSyncReportsAFailedDirectorySync proves the directory fsync
// failure is reported rather than swallowed.
func TestContainerSlotSyncReportsAFailedDirectorySync(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	syncFailure := errors.New("the state directory cannot be synced")
	err := syncContainerSlotDir(dir, &filewrite.Injector{Step: filewrite.StepDirSync, Err: syncFailure})
	if !errors.Is(err, syncFailure) {
		t.Fatalf("sync error = %v, want %v", err, syncFailure)
	}
	if !strings.Contains(err.Error(), "sync container slot state directory") {
		t.Fatalf("sync error = %v, want the failed sync named", err)
	}
}

// TestContainerSlotBusyErrorNamesTheHeldLock proves the busy refusal explains
// where the single-container slot is held.
func TestContainerSlotBusyErrorNamesTheHeldLock(t *testing.T) {
	t.Parallel()
	err := &ContainerSlotBusyError{Path: "/state/container.lock"}
	message := err.Error()
	if !strings.Contains(message, "/state/container.lock") {
		t.Fatalf("busy message = %q, want the lock path", message)
	}
	if !strings.Contains(message, "already holds the durable slot") {
		t.Fatalf("busy message = %q, want the slot refusal", message)
	}
}
