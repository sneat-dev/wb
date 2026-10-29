//go:build !windows

package agents

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testContainerRunID  = "agt-00000000000000000000000000000011"
	testContainerRunID2 = "agt-00000000000000000000000000000012"
	testContainerName   = "wb-agent-run"
	testContainerNonce  = "0123456789abcdef0123456789abcdef"
	testContainerID     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testContainerID2    = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	// testContainerDaemonID is the Docker Engine identity the common fake
	// fixtures report. A reservation binds it before create.
	testContainerDaemonID  = "1111111111111111111111111111111111111111111111111111111111111111"
	testContainerDaemonID2 = "2222222222222222222222222222222222222222222222222222222222222222"
	testContainerContext   = "default"
)

func testContainerReservation() ContainerReservation {
	return ContainerReservation{
		RunID:    testContainerRunID,
		Name:     testContainerName,
		Nonce:    testContainerNonce,
		DaemonID: testContainerDaemonID,
	}
}

// testContainerSlotStateDir returns a state directory whose leaf is explicitly
// private. Some environments hand out a world-writable t.TempDir leaf; the slot
// correctly requires its own state directory to be 0700, so fixtures that mean
// to exercise a different branch must create and pin their own leaf.
func testContainerSlotStateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create container slot state directory: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod container slot state directory: %v", err)
	}
	return dir
}

func openTestContainerSlot(t *testing.T, stateDir string) *ContainerSlot {
	t.Helper()
	slot, err := OpenContainerSlot(stateDir)
	if err != nil {
		t.Fatalf("OpenContainerSlot(%s): %v", stateDir, err)
	}
	t.Cleanup(func() { _ = slot.Close() })
	return slot
}

func TestContainerSlotDefaultStateDirIsIndependentOfProject(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("this environment has no user home directory: %v", err)
	}
	got, err := ContainerSlotStateDir()
	if err != nil {
		t.Fatalf("ContainerSlotStateDir: %v", err)
	}
	want := filepath.Join(home, ".config", "wb", "linux-execution")
	if got != want {
		t.Fatalf("default state directory = %s, want %s", got, want)
	}
	if cwd, err := os.Getwd(); err == nil && strings.HasPrefix(got, cwd+string(filepath.Separator)) {
		t.Fatalf("default state directory %s is inside the project %s", got, cwd)
	}
}

func TestContainerSlotDefaultStateDirIgnoresXDGConfigHome(t *testing.T) {
	baseline, err := ContainerSlotStateDir()
	if err != nil {
		t.Skipf("this environment has no user home directory: %v", err)
	}
	for _, xdg := range []string{"/tmp/wb-xdg-one", "/tmp/wb-xdg-two"} {
		t.Setenv("XDG_CONFIG_HOME", xdg)
		got, err := ContainerSlotStateDir()
		if err != nil {
			t.Fatalf("ContainerSlotStateDir with XDG_CONFIG_HOME=%s: %v", xdg, err)
		}
		if got != baseline {
			t.Fatalf("XDG_CONFIG_HOME=%s moved the single-user slot to %s, want the fixed %s", xdg, got, baseline)
		}
	}
}

func TestContainerSlotExcludesASecondHolder(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	first := openTestContainerSlot(t, dir)

	second, err := OpenContainerSlot(dir)
	if err == nil {
		_ = second.Close()
		t.Fatal("a second holder must be refused while the first is live")
	}
	var busy *ContainerSlotBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("second holder error = %v, want ContainerSlotBusyError", err)
	}
	if busy.Path == "" {
		t.Fatal("the busy error must name the lock file")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	third := openTestContainerSlot(t, dir)
	if third == nil {
		t.Fatal("the slot must be acquirable after Close")
	}
}

func TestContainerSlotCloseRetainsTheReservationForTheNextHolder(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	slot := openTestContainerSlot(t, dir)
	reservation := testContainerReservation()
	if err := slot.Reserve(reservation); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	lockPath := filepath.Join(dir, "container.lock")
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat lock before Close: %v", err)
	}
	if err := slot.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := slot.Close(); err != nil {
		t.Fatalf("Close must be idempotent: %v", err)
	}
	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("the lock file must never be unlinked: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("Close reopened a different lock inode; the retained lock inode is the authority")
	}

	next := openTestContainerSlot(t, dir)
	got, ok := next.Reservation()
	if !ok || got.RunID != reservation.RunID || got.Name != reservation.Name || got.Nonce != reservation.Nonce {
		t.Fatalf("next holder reservation = %#v %v, want %#v", got, ok, reservation)
	}
	if err := next.Reserve(testContainerReservation()); err == nil {
		t.Fatal("a next holder must not Reserve over a retained reservation without an absence proof")
	}
}

func TestContainerSlotClearRequiresTheExactAbsenceProof(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	slot := openTestContainerSlot(t, dir)
	reservation := testContainerReservation()
	if err := slot.Reserve(reservation); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := slot.ClearReservation(nil); err == nil {
		t.Fatal("ClearReservation must refuse a nil absence proof")
	}

	var seen ContainerReservation
	proofFailure := errors.New("container is still running")
	if err := slot.ClearReservation(func(got ContainerReservation) error {
		seen = got
		return proofFailure
	}); !errors.Is(err, proofFailure) {
		t.Fatalf("failed absence proof error = %v, want %v", err, proofFailure)
	}
	if seen.RunID != reservation.RunID || seen.Name != reservation.Name || seen.Nonce != reservation.Nonce {
		t.Fatalf("callback received %#v, want the exact prior reservation %#v", seen, reservation)
	}
	if _, ok := slot.Reservation(); !ok {
		t.Fatal("a failed absence proof must keep the reservation")
	}
	if err := slot.Reserve(testContainerReservation()); err == nil {
		t.Fatal("a failed absence proof must keep the slot blocked")
	}

	var proven ContainerReservation
	if err := slot.ClearReservation(func(got ContainerReservation) error {
		proven = got
		return nil
	}); err != nil {
		t.Fatalf("ClearReservation with a successful proof: %v", err)
	}
	if proven.RunID != reservation.RunID {
		t.Fatalf("successful proof callback received %#v", proven)
	}
	if _, ok := slot.Reservation(); ok {
		t.Fatal("a successful proof must clear the reservation")
	}
	if _, err := os.Stat(filepath.Join(dir, "reservation.json")); !os.IsNotExist(err) {
		t.Fatalf("the durable reservation file must be removed, stat error = %v", err)
	}
	next := testContainerReservation()
	next.RunID = testContainerRunID2
	if err := slot.Reserve(next); err != nil {
		t.Fatalf("Reserve after an absence proof: %v", err)
	}
}

func TestContainerSlotSaveFailureKeepsTheSlotPending(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	slot := openTestContainerSlot(t, dir)
	saveFailure := errors.New("state directory is full")
	slot.persist = func(ContainerReservation) error { return saveFailure }

	if err := slot.Reserve(testContainerReservation()); !errors.Is(err, saveFailure) {
		t.Fatalf("Reserve save error = %v, want %v", err, saveFailure)
	}
	pending, ok := slot.Reservation()
	if !ok || pending.RunID != testContainerRunID {
		t.Fatalf("a failed save must keep the reservation pending, got %#v %v", pending, ok)
	}
	if err := slot.Reserve(testContainerReservation()); err == nil {
		t.Fatal("a failed save must leave the slot blocked rather than clear")
	}
	if err := slot.ClearReservation(func(ContainerReservation) error { return nil }); err != nil {
		t.Fatalf("an explicit absence proof must recover a failed save: %v", err)
	}
}

func TestContainerSlotRecordContainerIDPersistsAndSnapshotsDoNotAlias(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	slot := openTestContainerSlot(t, dir)
	if err := slot.RecordContainerID(testContainerID); err == nil {
		t.Fatal("RecordContainerID must refuse a slot with no reservation")
	}
	if err := slot.Reserve(testContainerReservation()); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	snapshot, ok := slot.Reservation()
	if !ok || snapshot.ContainerID != "" {
		t.Fatalf("initial reservation = %#v %v, want an empty container ID", snapshot, ok)
	}
	snapshot.RunID = "mutated"
	snapshot.ContainerID = testContainerID
	fresh, _ := slot.Reservation()
	if fresh.RunID == "mutated" || fresh.ContainerID != "" {
		t.Fatalf("Reservation returned a mutable alias: %#v", fresh)
	}

	if err := slot.RecordContainerID("deadbeef"); err == nil {
		t.Fatal("RecordContainerID must refuse a short ID")
	}
	if err := slot.RecordContainerID(strings.ToUpper(testContainerID)); err == nil {
		t.Fatal("RecordContainerID must refuse an uppercase ID")
	}
	if err := slot.RecordContainerID(testContainerID); err != nil {
		t.Fatalf("RecordContainerID: %v", err)
	}
	if got, _ := slot.Reservation(); got.ContainerID != testContainerID {
		t.Fatalf("recorded container ID = %q", got.ContainerID)
	}
	if err := slot.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	next := openTestContainerSlot(t, dir)
	durable, ok := next.Reservation()
	if !ok || durable.ContainerID != testContainerID {
		t.Fatalf("recorded container ID was not durable, got %#v %v", durable, ok)
	}
	info, err := os.Stat(filepath.Join(dir, "reservation.json"))
	if err != nil {
		t.Fatalf("stat reservation: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("reservation file mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestContainerSlotRecordContainerIDRefusesToReplaceTheRecordedIdentity(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	slot := openTestContainerSlot(t, dir)
	if err := slot.Reserve(testContainerReservation()); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := slot.RecordContainerID(testContainerID); err != nil {
		t.Fatalf("initial RecordContainerID: %v", err)
	}
	if err := slot.RecordContainerID(testContainerID); err != nil {
		t.Fatalf("re-recording the same container ID must be idempotent: %v", err)
	}
	if err := slot.RecordContainerID(testContainerID2); err == nil {
		t.Fatal("RecordContainerID must refuse a different container ID once one is recorded")
	}
	inMemory, ok := slot.Reservation()
	if !ok || inMemory.ContainerID != testContainerID {
		t.Fatalf("refused rewrite changed the in-memory identity: %#v %v", inMemory, ok)
	}
	if err := slot.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	next := openTestContainerSlot(t, dir)
	durable, ok := next.Reservation()
	if !ok || durable.ContainerID != testContainerID {
		t.Fatalf("refused rewrite changed the durable identity: %#v %v", durable, ok)
	}
}

func TestContainerSlotReserveValidatesTheReservationIdentity(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	slot := openTestContainerSlot(t, dir)
	cases := []struct {
		name   string
		mutate func(*ContainerReservation)
	}{
		{"escaped run ID", func(r *ContainerReservation) { r.RunID = "../escape" }},
		{"empty name", func(r *ContainerReservation) { r.Name = "" }},
		{"name with whitespace", func(r *ContainerReservation) { r.Name = "wb run" }},
		{"option-shaped name", func(r *ContainerReservation) { r.Name = "--rm" }},
		{"oversized name", func(r *ContainerReservation) { r.Name = strings.Repeat("a", 64) }},
		{"uppercase nonce", func(r *ContainerReservation) { r.Nonce = strings.ToUpper(testContainerNonce) }},
		{"short nonce", func(r *ContainerReservation) { r.Nonce = "abcd" }},
		{"container ID at reserve", func(r *ContainerReservation) { r.ContainerID = testContainerID }},
		{"missing daemon identity", func(r *ContainerReservation) { r.DaemonID = "" }},
		{"option-shaped daemon identity", func(r *ContainerReservation) { r.DaemonID = "--host" }},
		{"daemon identity with whitespace", func(r *ContainerReservation) { r.DaemonID = "daemon one" }},
		{"oversized daemon identity", func(r *ContainerReservation) { r.DaemonID = strings.Repeat("a", 129) }},
	}
	for _, testCase := range cases {
		reservation := testContainerReservation()
		testCase.mutate(&reservation)
		if err := slot.Reserve(reservation); err == nil {
			t.Errorf("%s: Reserve accepted an invalid reservation", testCase.name)
		}
	}
	if _, ok := slot.Reservation(); ok {
		t.Fatal("invalid reservations must not leave a pending row")
	}
	if err := slot.Reserve(testContainerReservation()); err != nil {
		t.Fatalf("Reserve of a valid reservation: %v", err)
	}
}

func TestContainerSlotRefusesMalformedOversizedOrInsecureReservations(t *testing.T) {
	t.Parallel()
	validJSON := func(t *testing.T) []byte {
		t.Helper()
		raw, err := json.Marshal(testContainerReservation())
		if err != nil {
			t.Fatalf("marshal reservation: %v", err)
		}
		return raw
	}
	cases := []struct {
		name    string
		prepare func(t *testing.T, dir string)
	}{
		{"corrupt JSON", func(t *testing.T, dir string) {
			writeContainerSlotFile(t, filepath.Join(dir, "reservation.json"), []byte("{not json"), 0o600)
		}},
		{"empty file", func(t *testing.T, dir string) {
			writeContainerSlotFile(t, filepath.Join(dir, "reservation.json"), nil, 0o600)
		}},
		{"oversized file", func(t *testing.T, dir string) {
			writeContainerSlotFile(t, filepath.Join(dir, "reservation.json"), []byte(strings.Repeat("x", maxContainerReservationBytes+1)), 0o600)
		}},
		{"insecure permissions", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "reservation.json")
			writeContainerSlotFile(t, path, validJSON(t), 0o600)
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatalf("chmod reservation: %v", err)
			}
		}},
		{"symlink", func(t *testing.T, dir string) {
			target := filepath.Join(dir, "real.json")
			writeContainerSlotFile(t, target, validJSON(t), 0o600)
			if err := os.Symlink(target, filepath.Join(dir, "reservation.json")); err != nil {
				t.Fatalf("symlink reservation: %v", err)
			}
		}},
		{"invalid identity", func(t *testing.T, dir string) {
			reservation := testContainerReservation()
			reservation.Name = "--rm"
			raw, err := json.Marshal(reservation)
			if err != nil {
				t.Fatalf("marshal reservation: %v", err)
			}
			writeContainerSlotFile(t, filepath.Join(dir, "reservation.json"), raw, 0o600)
		}},
		{"invalid container ID", func(t *testing.T, dir string) {
			reservation := testContainerReservation()
			reservation.ContainerID = "deadbeef"
			raw, err := json.Marshal(reservation)
			if err != nil {
				t.Fatalf("marshal reservation: %v", err)
			}
			writeContainerSlotFile(t, filepath.Join(dir, "reservation.json"), raw, 0o600)
		}},
		{"invalid daemon identity", func(t *testing.T, dir string) {
			reservation := testContainerReservation()
			reservation.DaemonID = "daemon one"
			raw, err := json.Marshal(reservation)
			if err != nil {
				t.Fatalf("marshal reservation: %v", err)
			}
			writeContainerSlotFile(t, filepath.Join(dir, "reservation.json"), raw, 0o600)
		}},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			dir := testContainerSlotStateDir(t)
			testCase.prepare(t, dir)
			slot, err := OpenContainerSlot(dir)
			if err == nil {
				_ = slot.Close()
				t.Fatal("OpenContainerSlot accepted an unsafe reservation file")
			}
			// A failed open must not leak the lock: after the bad file is gone
			// the same directory must be acquirable.
			if removeErr := os.Remove(filepath.Join(dir, "reservation.json")); removeErr != nil {
				t.Fatalf("remove bad reservation: %v", removeErr)
			}
			recovered, err := OpenContainerSlot(dir)
			if err != nil {
				t.Fatalf("OpenContainerSlot after removing the bad reservation: %v", err)
			}
			_ = recovered.Close()
		})
	}
}

func TestContainerSlotRefusesAnUnsafeStateDirectory(t *testing.T) {
	t.Parallel()
	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		realDir := t.TempDir()
		link := filepath.Join(t.TempDir(), "state")
		if err := os.Symlink(realDir, link); err != nil {
			t.Fatalf("symlink state directory: %v", err)
		}
		if slot, err := OpenContainerSlot(link); err == nil {
			_ = slot.Close()
			t.Fatal("OpenContainerSlot must refuse a symlinked state directory")
		}
	})
	t.Run("insecure permissions", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatalf("chmod state directory: %v", err)
		}
		if slot, err := OpenContainerSlot(dir); err == nil {
			_ = slot.Close()
			t.Fatal("OpenContainerSlot must refuse a world-readable state directory")
		}
	})
	t.Run("creates a private directory", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "nested", "state")
		slot := openTestContainerSlot(t, dir)
		if slot == nil {
			t.Fatal("slot was not opened")
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat created state directory: %v", err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("created state directory mode = %04o, want 0700", info.Mode().Perm())
		}
	})
}

func TestContainerSlotRefusesAnUnsafeExistingLockFile(t *testing.T) {
	t.Parallel()
	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		dir := testContainerSlotStateDir(t)
		target := filepath.Join(dir, "real.lock")
		writeContainerSlotFile(t, target, nil, 0o600)
		if err := os.Symlink(target, filepath.Join(dir, "container.lock")); err != nil {
			t.Fatalf("symlink lock file: %v", err)
		}
		if slot, err := OpenContainerSlot(dir); err == nil {
			_ = slot.Close()
			t.Fatal("OpenContainerSlot must refuse a symlinked lock file")
		}
	})
	t.Run("insecure permissions", func(t *testing.T) {
		t.Parallel()
		dir := testContainerSlotStateDir(t)
		writeContainerSlotFile(t, filepath.Join(dir, "container.lock"), nil, 0o644)
		if slot, err := OpenContainerSlot(dir); err == nil {
			_ = slot.Close()
			t.Fatal("OpenContainerSlot must refuse a world-readable lock file")
		}
	})
	t.Run("not a regular file", func(t *testing.T) {
		t.Parallel()
		dir := testContainerSlotStateDir(t)
		if err := os.Mkdir(filepath.Join(dir, "container.lock"), 0o700); err != nil {
			t.Fatalf("make lock directory: %v", err)
		}
		if slot, err := OpenContainerSlot(dir); err == nil {
			_ = slot.Close()
			t.Fatal("OpenContainerSlot must refuse a non-regular lock file")
		}
	})
}

func TestContainerSlotClosedSlotRefusesMutations(t *testing.T) {
	t.Parallel()
	dir := testContainerSlotStateDir(t)
	slot := openTestContainerSlot(t, dir)
	if _, ok := slot.Reservation(); ok {
		t.Fatal("a fresh slot must hold no reservation")
	}
	if err := slot.ClearReservation(func(ContainerReservation) error { return nil }); err == nil {
		t.Fatal("a slot with no reservation must refuse ClearReservation")
	}
	if err := slot.Reserve(testContainerReservation()); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := slot.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := slot.Reserve(testContainerReservation()); err == nil {
		t.Fatal("a closed slot must refuse Reserve")
	}
	if err := slot.RecordContainerID(testContainerID); err == nil {
		t.Fatal("a closed slot must refuse RecordContainerID")
	}
	if err := slot.ClearReservation(func(ContainerReservation) error {
		t.Error("a closed slot must not run the absence proof")
		return nil
	}); err == nil {
		t.Fatal("a closed slot must refuse ClearReservation")
	}
}

func writeContainerSlotFile(t *testing.T, path string, raw []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
