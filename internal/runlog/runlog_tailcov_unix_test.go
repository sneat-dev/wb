//go:build unix

package runlog

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// tailCovFileSizeLimitChildEnv marks the re-executed child process that runs
// the write-failure probe with a lowered RLIMIT_FSIZE.
//
// The limit is per-process, and Go's testing framework records every file
// operation in its own testlog whenever the test-result cache is enabled --
// which is exactly how `go test` is invoked in CI. Lowering the limit in the
// test binary therefore also caps those framework writes, and the package fails
// with "can't write testlog.txt: file too large" even though every test passed.
// The child is started without -test.testlogfile, so the probe stays honest and
// the framework can keep logging.
const tailCovFileSizeLimitChildEnv = "WB_RUNLOG_FSIZE_LIMIT_CHILD"

// tailCovSpawnFileSizeLimitedChild re-executes the calling test in a child
// process and reports whether this process is the parent, which must then
// return without running the probe body.
func tailCovSpawnFileSizeLimitedChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv(tailCovFileSizeLimitChildEnv) == "1" {
		return false
	}
	command := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	command.Env = append(os.Environ(), tailCovFileSizeLimitChildEnv+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("probe under a lowered RLIMIT_FSIZE failed: %v\n%s", err, output)
	}
	return true
}

// TestTailCovAppendReportsWriteFailure covers the post-lock write guard. A
// zero RLIMIT_FSIZE makes the kernel refuse any write that would extend the
// freshly created log, which is the only portable way to reach that branch —
// open and flock have already succeeded by then.
func TestTailCovAppendReportsWriteFailure(t *testing.T) {
	if tailCovSpawnFileSizeLimitedChild(t) {
		return
	}
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatalf("getrlimit(RLIMIT_FSIZE): %v", err)
	}
	limit := syscall.Rlimit{Cur: 0, Max: original.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatalf("setrlimit(RLIMIT_FSIZE, 0): %v", err)
	}
	defer func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
			t.Errorf("restore RLIMIT_FSIZE: %v", err)
		}
	}()

	path := filepath.Join(t.TempDir(), "events.jsonl")
	err := Append(path, Event{SchemaVersion: EventSchemaVersion, State: "requested"})
	tailCovWantError(t, "Append (write fails)", err, "append run event")

	if info, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("the log file should exist after the failed write: %v", statErr)
	} else if info.Size() != 0 {
		t.Fatalf("a rejected write must leave no bytes behind, size = %d", info.Size())
	}
}

// tailCovChdirBeyondPathMax moves the process into a directory whose absolute
// path is longer than PATH_MAX. getcwd then fails with ERANGE, and the
// fallback walk-up in os.Getwd trips its own 1024-byte guard. This is how a
// surviving cwd becomes unresolvable on platforms (macOS, the BSDs) that keep
// a deleted directory's name cached in its vnode.
func tailCovChdirBeyondPathMax(t *testing.T) {
	t.Helper()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir to temp dir: %v", err)
	}
	for level := 0; level < 360; level++ {
		if err := os.Mkdir("abc", 0o700); err != nil {
			t.Fatalf("mkdir level %d: %v", level, err)
		}
		if err := os.Chdir("abc"); err != nil {
			t.Fatalf("chdir level %d: %v", level, err)
		}
	}
}

// TestTailCovManagedWorktreeRejectsUnresolvableWorkingDirectory covers the
// filepath.Abs guard: when the process working directory cannot be turned
// into an absolute path, a relative cwd is reported as unmanaged rather than
// guessed at.
func TestTailCovManagedWorktreeRejectsUnresolvableWorkingDirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	// Defers run before t.Cleanup, so the working directory is restored before
	// t.TempDir tries to delete anything.
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()

	if runtime.GOOS == "linux" {
		// Linux drops a deleted directory's name, so removing the working
		// directory is enough to make getcwd (and therefore filepath.Abs) fail.
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatalf("chdir: %v", err)
		}
		if err := os.Remove(dir); err != nil {
			t.Fatalf("remove the working directory: %v", err)
		}
	} else {
		tailCovChdirBeyondPathMax(t)
	}

	if _, err := os.Getwd(); err == nil {
		t.Fatal("the working directory should be unresolvable for this test to prove anything")
	}

	root, _, found := managedWorktree("relative")
	if found {
		t.Fatalf("managedWorktree found %q for an unresolvable relative cwd", root)
	}
	if root != "" {
		t.Fatalf("root = %q, want empty", root)
	}
}
