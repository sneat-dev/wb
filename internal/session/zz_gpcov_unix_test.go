//go:build !windows

package session

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// This file covers the parts of internal/session that need POSIX process and
// filesystem semantics: symlink collisions on the no-replace lifecycle
// markers, NAME_MAX limits, record modes, and the real process table behind
// parentPID / processEvidence / findHarnessAncestor. It is excluded from
// Windows builds, where those mechanisms do not exist.

// gpCovHarnessHelperEnv names the environment variable through which the
// process-tree tests tell a child copy of this test binary where to report the
// PID of the sleeping child it has to own.
const gpCovHarnessHelperEnv = "GPCOV_HARNESS_HELPER_PID_FILE"

// TestGpCovHarnessHelperProcess is the long-lived stand-in for a harness
// process that the process-tree tests below exec under a harness name. With the
// marker variable set it starts a sleeping child, writes that child's PID where
// the parent can read it, and outlives the parent's observation; run as part of
// the ordinary suite it only confirms it was not invoked as a helper.
func TestGpCovHarnessHelperProcess(t *testing.T) {
	t.Parallel()
	pidFile := strings.TrimSpace(os.Getenv(gpCovHarnessHelperEnv))
	if pidFile == "" {
		if executable, err := os.Executable(); err != nil || executable == "" {
			t.Fatalf("os.Executable() = (%q, %v), want the running test binary", executable, err)
		}
		return
	}

	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatalf("start the helper's child process: %v", err)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
		t.Fatalf("report the helper's child PID: %v", err)
	}
	time.Sleep(60 * time.Second)
}

// gpCovHarnessNamedProcess execs a copy of this test binary under name and
// returns the PID of that process together with the PID of a live child of it,
// which is what findHarnessAncestor inspects from below.
func gpCovHarnessNamedProcess(t *testing.T, name string) (namedPID, childPID int) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable(): %v", err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatalf("read the test binary: %v", err)
	}
	named := filepath.Join(t.TempDir(), name)
	if err := testenv.WriteExecutableFile(named, raw, 0o755); err != nil {
		t.Fatalf("write the %s-named copy: %v", name, err)
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// "cursor-agent" is deliberately at least as long as the name this test
	// binary runs under: the kernel command name is a fixed-width field that an
	// exec overwrites but does not clear, so a shorter name would read back
	// trailing bytes of the parent's name.
	cmd := exec.Command(named, "-test.run=^TestGpCovHarnessHelperProcess$")
	cmd.Env = append(os.Environ(), gpCovHarnessHelperEnv+"="+pidFile)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the %s-named process: %v", name, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	childPID = gpCovWaitForPID(t, pidFile)
	t.Cleanup(func() {
		if child, err := os.FindProcess(childPID); err == nil {
			_ = child.Kill()
		}
	})
	return cmd.Process.Pid, childPID
}

func gpCovWaitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the child PID in %s", path)
	return 0
}

func TestGpCovMarkParkedRejectsADanglingMarkerSymlink(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-gp-dangling", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	markerDir := filepath.Join(dir, "lifecycle")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A dangling symlink is invisible to ReadFile but already exists to
	// O_CREAT|O_EXCL, which is exactly the collision the no-replace marker must
	// refuse rather than follow.
	if err := os.Symlink(filepath.Join(markerDir, "absent.json"), parkedMarkerPath(dir, registered.WBSessionID)); err != nil {
		t.Fatal(err)
	}

	if _, err := MarkParked(dir, registered.PID, "gp-park"); err == nil {
		t.Fatal("MarkParked created a marker through a dangling symlink")
	} else if !strings.Contains(err.Error(), "already parked") {
		t.Fatalf("error = %q, want an already-parked refusal", err)
	}
}

func TestGpCovMarkResumedRejectsADanglingMarkerSymlink(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-gp-resume-dangling", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkParked(dir, registered.PID, "gp-park"); err != nil {
		t.Fatal(err)
	}
	markerDir := filepath.Join(dir, "lifecycle")
	if err := os.Symlink(filepath.Join(markerDir, "absent.json"), resumedMarkerPath(dir, registered.WBSessionID)); err != nil {
		t.Fatal(err)
	}

	if _, err := MarkResumed(dir, registered.PID, "gp-park", "wbs-gp-successor"); err == nil {
		t.Fatal("MarkResumed created a marker through a dangling symlink")
	} else if !strings.Contains(err.Error(), "different resumed projection") {
		t.Fatalf("error = %q, want a different-projection refusal", err)
	}
}

func TestGpCovMarkResumedReportsAResumedMarkerThatCannotBeCreated(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	// NAME_MAX is 255, so this session ID's parked marker name fits exactly
	// while its resumed marker name (one byte longer) cannot be created. The
	// parked half must succeed and the resumed half must report the failure.
	longID := strings.Repeat("g", 255-len(".parked.json"))
	registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: longID, Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkParked(dir, registered.PID, "gp-park"); err != nil {
		t.Fatalf("MarkParked with a NAME_MAX-length parked marker: %v", err)
	}

	if _, err := MarkResumed(dir, registered.PID, "gp-park", "wbs-gp-successor"); err == nil {
		t.Fatal("MarkResumed reported success without writing its marker")
	} else if !strings.Contains(err.Error(), "record resumed session lifecycle") {
		t.Fatalf("error = %q, want a resumed-marker write failure", err)
	}
	if _, err := os.Stat(resumedMarkerPath(dir, longID)); err == nil {
		t.Fatal("a resumed marker was created despite the reported failure")
	}
}

func TestGpCovLookupExactRejectsUnsafeRecordModes(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	record, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-gp-exact-mode", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	path := recordPath(dir, record.PID)

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LookupExact(dir, record.PID); err == nil {
		t.Fatal("LookupExact accepted a record that is not mode 0644")
	} else if !strings.Contains(err.Error(), "mode 0644") {
		t.Fatalf("error = %q, want a record-mode refusal", err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(dir, "gp-hardlink.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LookupExact(dir, record.PID); err == nil {
		t.Fatal("LookupExact accepted a record with more than one link")
	} else if !strings.Contains(err.Error(), "single-link") {
		t.Fatalf("error = %q, want a single-link refusal", err)
	}
}

func TestGpCovProcessEvidenceReadsTheRealProcessTable(t *testing.T) {
	t.Parallel()
	for _, pid := range []int{0, -1, -4242} {
		if evidence, ok := processEvidence(pid); ok {
			t.Fatalf("processEvidence(%d) = (%#v, true), want no evidence", pid, evidence)
		}
	}
	if _, ok := processEvidence(1 << 30); ok {
		t.Fatal("processEvidence invented evidence for a PID that cannot exist")
	}

	ownEvidence, ok := processEvidence(os.Getpid())
	if !ok {
		t.Fatalf("processEvidence(%d) for this test binary reported no evidence", os.Getpid())
	}
	// The platform reports different spellings here -- Linux resolves the full
	// /proc/<pid>/exe path while darwin can return just the process name -- so
	// compare basenames, which is the part that identifies the running binary.
	if want := filepath.Base(os.Args[0]); filepath.Base(ownEvidence.Executable) != want {
		t.Fatalf("own executable = %q, want a path ending in %q", ownEvidence.Executable, want)
	}
	if len(ownEvidence.Args) == 0 || filepath.Base(ownEvidence.Args[0]) != filepath.Base(os.Args[0]) {
		t.Fatalf("own args = %#v, want the running command line", ownEvidence.Args)
	}

	namedPID, childPID := gpCovHarnessNamedProcess(t, "cursor-agent")
	namedEvidence, ok := processEvidence(namedPID)
	if !ok {
		t.Fatalf("processEvidence(%d) for the harness-named process reported no evidence", namedPID)
	}
	// Linux reports the full resolved path for a named process while darwin can
	// report just the name, so compare basenames.
	if got := filepath.Base(namedEvidence.Executable); got != "cursor-agent" {
		t.Fatalf("named executable = %q, want a path ending in %q", namedEvidence.Executable, "cursor-agent")
	}

	childEvidence, ok := processEvidence(childPID)
	if !ok {
		t.Fatalf("processEvidence(%d) for the sleeping child reported no evidence", childPID)
	}
	// The kernel command name is a fixed-width field that an exec rewrites but
	// does not clear, so only the NUL-terminated prefix identifies the child on
	// platforms that leave the rest of the field alone.
	if got, _, _ := strings.Cut(childEvidence.Executable, "\x00"); filepath.Base(got) != "sleep" {
		t.Fatalf("child executable = %q, want the process named sleep", childEvidence.Executable)
	}
}

func TestGpCovProcessEvidenceRefusesAProcessThatHasExited(t *testing.T) {
	t.Parallel()
	exitPath := ""
	for _, candidate := range []string{"/usr/bin/true", "/bin/true"} {
		if _, err := os.Stat(candidate); err == nil {
			exitPath = candidate
			break
		}
	}
	if exitPath == "" {
		t.Fatal("no `true` binary is available to make a process that exits")
	}
	cmd := exec.Command(exitPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = cmd.Process.Wait() })

	// A process that has exited is gone as a session: it may linger as a zombie
	// in the process table, but it has no executable or argument evidence left.
	deadline := time.Now().Add(10 * time.Second)
	for {
		evidence, ok := processEvidence(cmd.Process.Pid)
		if !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("processEvidence(%d) = (%#v, true), want no evidence for an exited process", cmd.Process.Pid, evidence)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGpCovParentPIDReportsTheRealAncestorAndRefusesRoot(t *testing.T) {
	t.Parallel()
	parent, ok := parentPID(os.Getpid())
	if !ok {
		t.Fatalf("parentPID(%d) reported no parent for a running process", os.Getpid())
	}
	if parent <= 0 || parent == os.Getpid() {
		t.Fatalf("parentPID(%d) = %d, want the real parent PID", os.Getpid(), parent)
	}
	if _, ok := parentPID(1); ok {
		t.Fatal("parentPID(1) invented a parent for the root process")
	}
	if _, ok := parentPID(1 << 30); ok {
		t.Fatal("parentPID invented a parent for a PID that cannot exist")
	}
}

func TestGpCovFindHarnessAncestorNamesAKnownHarnessAncestor(t *testing.T) {
	harnessPID, childPID := gpCovHarnessNamedProcess(t, "cursor-agent")
	pid, runtime := findHarnessAncestor(childPID)
	if pid != harnessPID || runtime != "cursor-agent" {
		t.Fatalf("findHarnessAncestor(child of a process named cursor-agent) = (%d, %q), want (%d, %q)",
			pid, runtime, harnessPID, "cursor-agent")
	}
}

func TestGpCovFindHarnessAncestorWalksPastANonHarnessParent(t *testing.T) {
	namedPID, childPID := gpCovHarnessNamedProcess(t, "gpcovsh")
	pid, runtime := findHarnessAncestor(childPID)
	if pid == namedPID {
		t.Fatal("findHarnessAncestor named a process whose executable is not a known harness")
	}
	if runtime == "gpcovsh" {
		t.Fatalf("findHarnessAncestor reported runtime %q for an unknown executable", runtime)
	}

	if pid, runtime := findHarnessAncestor(1 << 30); pid != 0 || runtime != "" {
		t.Fatalf("findHarnessAncestor(uninspectable PID) = (%d, %q), want no evidence", pid, runtime)
	}
}

// gpCovFileSizeLimitChildEnv marks the re-executed child that runs one probe
// with a lowered RLIMIT_FSIZE.
//
// The limit is per-process, and Go's testing framework records every file
// operation in its own testlog whenever the test-result cache is enabled --
// which is how CI invokes `go test`. Lowering the limit in the test binary also
// caps those framework writes, so the package fails with "can't write
// testlog.txt: file too large" even when every test passed. The child is
// started without -test.testlogfile, so the fault stays honest.
const gpCovFileSizeLimitChildEnv = "WB_SESSION_FSIZE_LIMIT_CHILD"

// gpCovSpawnFileSizeLimitedChild re-executes the calling test in a child and
// reports whether this process is the parent, which must then return without
// running the probe body.
func gpCovSpawnFileSizeLimitedChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv(gpCovFileSizeLimitChildEnv) == "1" {
		return false
	}
	command := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	command.Env = append(os.Environ(), gpCovFileSizeLimitChildEnv+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("probe under a lowered RLIMIT_FSIZE failed: %v\n%s", err, output)
	}
	return true
}

// gpCovWithoutFileWrites runs fn with the process file-size limit pinned to
// zero, so the next write to a regular file fails with EFBIG. The limit is
// restored before the caller continues, whatever fn does.
func gpCovWithoutFileWrites(t *testing.T, fn func()) {
	t.Helper()
	var previous syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &previous); err != nil {
		t.Fatalf("read RLIMIT_FSIZE: %v", err)
	}
	pinned := syscall.Rlimit{Cur: 0, Max: previous.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &pinned); err != nil {
		t.Fatalf("pin RLIMIT_FSIZE: %v", err)
	}
	defer func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &previous); err != nil {
			t.Errorf("restore RLIMIT_FSIZE: %v", err)
		}
	}()
	fn()
}

func TestGpCovLifecycleMarkersReportWriteFailures(t *testing.T) {
	t.Parallel()
	if gpCovSpawnFileSizeLimitedChild(t) {
		return
	}
	dir := filepath.Join(t.TempDir(), "sessions")
	parked, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-gp-write-parked", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}

	gpCovWithoutFileWrites(t, func() {
		if _, err := MarkParked(dir, parked.PID, "gp-park"); !errors.Is(err, syscall.EFBIG) {
			t.Errorf("MarkParked under a zero file-size limit = %v, want a file-too-large write failure", err)
		}
	})
	// The failed write leaves an empty marker behind, and the no-replace rule
	// then reads it back as an already-parked session rather than overwriting it.
	parkedMarker := parkedMarkerPath(dir, parked.WBSessionID)
	raw, err := os.ReadFile(parkedMarker)
	if err != nil {
		t.Fatalf("read the partial parked marker: %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("partial parked marker = %q, want the empty file the failed write left", raw)
	}
	if _, err := MarkParked(dir, parked.PID, "gp-park"); err == nil {
		t.Fatal("MarkParked overwrote the partial marker a failed write left behind")
	}
	if err := os.Remove(parkedMarker); err != nil {
		t.Fatal(err)
	}
	// With the limit restored and the partial marker gone, the same call works.
	if _, err := MarkParked(dir, parked.PID, "gp-park"); err != nil {
		t.Fatalf("MarkParked after the limit was restored: %v", err)
	}

	resumeDir := filepath.Join(t.TempDir(), "sessions")
	resumed, err := Register(resumeDir, Record{PID: os.Getpid(), WBSessionID: "wbs-gp-write-resumed", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkParked(resumeDir, resumed.PID, "gp-park"); err != nil {
		t.Fatal(err)
	}
	gpCovWithoutFileWrites(t, func() {
		if _, err := MarkResumed(resumeDir, resumed.PID, "gp-park", "wbs-gp-successor"); !errors.Is(err, syscall.EFBIG) {
			t.Errorf("MarkResumed under a zero file-size limit = %v, want a file-too-large write failure", err)
		}
	})
	if err := os.Remove(resumedMarkerPath(resumeDir, resumed.WBSessionID)); err != nil {
		t.Fatalf("remove the partial resumed marker: %v", err)
	}
	if _, err := MarkResumed(resumeDir, resumed.PID, "gp-park", "wbs-gp-successor"); err != nil {
		t.Fatalf("MarkResumed after the limit was restored: %v", err)
	}
}
