//go:build !windows

package quality

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// dqCovFileSizeLimitChildEnv marks the re-executed child process that runs one
// probe with a lowered RLIMIT_FSIZE.
//
// The limit is per-process, and Go's testing framework records every file
// operation in its own testlog whenever the test-result cache is enabled --
// which is exactly how `go test` is invoked in CI. Lowering the limit in the
// test binary therefore also caps those framework writes, and the package fails
// with "can't write testlog.txt: file too large" even though every test passed.
// The probe runs in a child that was started without -test.testlogfile, so the
// fault injection stays honest and the framework can keep logging.
const dqCovFileSizeLimitChildEnv = "WB_DQCOV_FSIZE_LIMIT_CHILD"

// dqCovSpawnFileSizeLimitedChild re-executes the calling test in a child
// process and reports whether this process is the parent, which must then
// return without running the probe body. The child sees the marker, returns
// false, and runs the body with the limit lowered.
func dqCovSpawnFileSizeLimitedChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv(dqCovFileSizeLimitChildEnv) == "1" {
		return false
	}
	command := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	command.Env = append(os.Environ(), dqCovFileSizeLimitChildEnv+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("probe under a lowered RLIMIT_FSIZE failed: %v\n%s", err, output)
	}
	return true
}

// dqCovLimitFileSize lowers RLIMIT_FSIZE for the calling child process and
// returns a restore function. Every regular-file write past limit bytes then
// fails with EFBIG (SIGXFSZ is ignored), which is how these tests exercise the
// atomic writers' failure paths without weakening any production code.
func dqCovLimitFileSize(t *testing.T, limit uint64) func() {
	t.Helper()
	signal.Ignore(syscall.SIGXFSZ)
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	limited := original
	limited.Cur = limit
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatal(err)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original)
		signal.Reset(syscall.SIGXFSZ)
	}
	t.Cleanup(restore)
	return restore
}

// TestDqCovWriteCoverageProfileAtomicallySurfacesFlushFailure proves a failed
// final flush is reported and no merged profile is published, rather than a
// silently truncated union.
func TestDqCovWriteCoverageProfileAtomicallySurfacesFlushFailure(t *testing.T) {
	if dqCovSpawnFileSizeLimitedChild(t) {
		return
	}
	restore := dqCovLimitFileSize(t, 8)
	defer restore()

	directory := t.TempDir()
	output := filepath.Join(directory, "merged.cov")
	blocks := map[string]coverageBlock{
		"example/a.go:1.1,2.2": {location: "example/a.go:1.1,2.2", statements: 2, count: 1},
	}
	err := writeCoverageProfileAtomically(output, "set", blocks)
	if err == nil || !strings.Contains(err.Error(), "flush merged coverage profile") {
		t.Fatalf("error = %v, want the flush failure surfaced", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("a partial merged profile was published: %v", statErr)
	}
	if names := dqCovReadDirNames(t, directory); len(names) != 0 {
		t.Fatalf("scratch artifacts survived a failed publication: %v", names)
	}
}

// TestDqCovWriteCoverageProfileAtomicallySurfacesBlockWriteFailure pushes the
// writer past its internal buffer so a failing write surfaces from the block
// formatting call itself.
func TestDqCovWriteCoverageProfileAtomicallySurfacesBlockWriteFailure(t *testing.T) {
	if dqCovSpawnFileSizeLimitedChild(t) {
		return
	}
	restore := dqCovLimitFileSize(t, 8)
	defer restore()

	directory := t.TempDir()
	blocks := map[string]coverageBlock{}
	for index := 0; index < 400; index++ {
		location := fmt.Sprintf("example/file%03d.go:1.1,2.2", index)
		blocks[location] = coverageBlock{location: location, statements: 1, count: 1}
	}
	output := filepath.Join(directory, "merged.cov")
	if err := writeCoverageProfileAtomically(output, "set", blocks); err == nil {
		t.Fatal("a failed buffered write was accepted")
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("a partial merged profile was published: %v", statErr)
	}
}

// TestDqCovSaveValidationCacheSurfacesEvidenceWriteFailure proves a failed
// evidence write returns an error and publishes no cache entry.
func TestDqCovSaveValidationCacheSurfacesEvidenceWriteFailure(t *testing.T) {
	if dqCovSpawnFileSizeLimitedChild(t) {
		return
	}
	restore := dqCovLimitFileSize(t, 0)
	defer restore()

	cacheRoot := filepath.Join(t.TempDir(), "cache")
	key := ValidationCacheKey{Repository: "example/cache", TargetRevision: "revision-1"}
	report := VerificationReport{Repository: key.Repository, Revision: key.TargetRevision, WorkspaceClean: true, Status: StatusPassed}
	if err := SaveValidationCache(cacheRoot, key, report); err == nil {
		t.Fatal("a failed evidence write was accepted")
	}
	for _, name := range dqCovReadDirNames(t, cacheRoot) {
		if filepath.Ext(name) == ".json" {
			t.Fatalf("a cache entry %q was published despite the write failure", name)
		}
	}
}

// dqCovReadDirNames lists directory entry names, tolerating a missing directory.
func dqCovReadDirNames(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
