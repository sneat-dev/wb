package waitregistry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAliveRejectsNonPositivePIDsWithoutConsultingTheOS covers DefaultAlive's
// guard branch (registry.go:75-77): a non-positive PID is never live, and the
// function must return that without delegating to session.ProcessAlive.
func TestAliveRejectsNonPositivePIDsWithoutConsultingTheOS(t *testing.T) {
	t.Parallel()
	for _, pid := range []int{0, -1, -42} {
		if DefaultAlive(pid) {
			t.Errorf("DefaultAlive(%d) = true, want false for a non-positive pid", pid)
		}
	}
}

// TestAliveDelegatesToProcessAliveForAPositivePID covers registry.go:78: a
// positive PID reaches session.ProcessAlive. The current test process's own
// PID is a real, already-running process (no new process is started), so
// this observes the real delegation rather than a stub.
func TestAliveDelegatesToProcessAliveForAPositivePID(t *testing.T) {
	t.Parallel()
	if !DefaultAlive(os.Getpid()) {
		t.Fatalf("DefaultAlive(%d) = false, want true for the running test process", os.Getpid())
	}
}

// TestRegisterInjectedFailsWhenTheRegistryRootCannotBeCreated covers
// registerInjected's os.MkdirAll error branch (registry.go:121-122): the
// registry directory path is occupied by a regular file, so MkdirAll cannot
// create it, and Register must surface that instead of silently proceeding.
func TestRegisterInjectedFailsWhenTheRegistryRootCannotBeCreated(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, directory), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := registerInjected(home, Record{ID: "blocked"}, nil)
	if err == nil {
		t.Fatal("registerInjected with an occupied registry root = nil error, want one")
	}
	if !strings.Contains(err.Error(), "create wait registry") {
		t.Fatalf("registerInjected error = %q, want it to name the create-registry step", err.Error())
	}
}

// TestRegisterInjectedFailsWhenTheRecordCannotBeEncoded covers
// registerInjected's json.Marshal error branch (registry.go:126-127). A
// StartedAt year outside [0,9999] makes time.Time's own MarshalJSON fail,
// which is a real encoding failure reachable through public Record fields —
// no production seam needed.
func TestRegisterInjectedFailsWhenTheRecordCannotBeEncoded(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	unencodable := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	_, err := registerInjected(home, Record{ID: "bad-time", StartedAt: unencodable}, nil)
	if err == nil {
		t.Fatal("registerInjected with an unencodable StartedAt = nil error, want one")
	}
	if !strings.Contains(err.Error(), "encode wait record") {
		t.Fatalf("registerInjected error = %q, want it to name the encode step", err.Error())
	}
	// The failed encode must not have published anything.
	if _, statErr := os.Stat(filepath.Join(dir(home), "bad-time.json")); !os.IsNotExist(statErr) {
		t.Fatalf("a failed encode published a visible record: %v", statErr)
	}
}

// TestListFailsWhenTheRegistryRootIsNotADirectory covers List's non-NotExist
// ReadDir error branch (registry.go:148): the registry path exists but is a
// regular file, so ReadDir fails with something other than "not exist", and
// List must report it rather than treating it as an empty registry.
func TestListFailsWhenTheRegistryRootIsNotADirectory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, directory), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := List(home, Options{}); err == nil {
		t.Fatal("List(home) with a non-directory registry root = nil error, want one")
	} else if !strings.Contains(err.Error(), "read wait registry") {
		t.Fatalf("List error = %q, want it to name the read-registry step", err.Error())
	}
}

// TestListSkipsAnEntryItCannotRead covers List's per-entry ReadFile error
// branch (registry.go:157): a dangling symlink named like a record has a
// ".json" suffix and is not itself a directory, so it passes the earlier
// filters, but reading it fails. List must skip it rather than fail the
// whole listing, and a genuine record alongside it must still surface.
func TestListSkipsAnEntryItCannotRead(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.MkdirAll(dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	danglingTarget := filepath.Join(home, "does-not-exist")
	if err := os.Symlink(danglingTarget, filepath.Join(dir(home), "dangling.json")); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}
	if _, err := Register(home, Record{ID: "real", PID: 1, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	records, err := List(home, fixedAlive(true))
	if err != nil {
		t.Fatalf("List with an unreadable entry present failed entirely: %v", err)
	}
	if len(records) != 1 || records[0].ID != "real" {
		t.Fatalf("List(home) = %+v, want exactly the readable \"real\" record", records)
	}
}

// TestPruneSurfacesAListFailureRatherThanReportingZero covers Prune's error
// propagation from List (registry.go:176-177): when List cannot even read
// the registry, Prune must return that error instead of silently reporting
// zero records pruned.
func TestPruneSurfacesAListFailureRatherThanReportingZero(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, directory), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := Prune(home, Options{})
	if err == nil {
		t.Fatal("Prune(home) with an unreadable registry = nil error, want one")
	}
	if removed != 0 {
		t.Errorf("Prune(home) removed = %d on a failed List, want 0", removed)
	}
	if !strings.Contains(err.Error(), "read wait registry") {
		t.Fatalf("Prune error = %q, want the underlying List error to surface", err.Error())
	}
}
