//go:build !windows

package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestSavePRStatusCachePublishesAt0644 is deliberately not t.Parallel, and
// lives in its own !windows-tagged file: review-767's N5 finding was that
// the plain version of this assertion depended on the running process's
// ambient umask (a stricter umask than 022 would strip bits and fail the
// test for a reason unrelated to the migration). unix.Umask is a
// process-wide syscall -- it does not exist as a concept on Windows, and
// golang.org/x/sys/unix does not build there -- so this test pins it to 0
// for its own duration and restores the previous value on exit; running any
// other test concurrently while it does that would leak the temporary
// umask into them.
//
//nolint:paralleltest // pins the process umask for its duration (review-767 N5); see comment above
func TestSavePRStatusCachePublishesAt0644(t *testing.T) {
	previousUmask := unix.Umask(0)
	t.Cleanup(func() { unix.Umask(previousUmask) })
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	// Unlike writeExecutableAt, savePRStatusCache has no separate chmod
	// step to lose: filewrite.WriteFile creates the file with the given
	// mode directly (os.WriteFile's own OpenFile|O_CREATE call), so there
	// is no CreateTemp-style default to mask a dropped mode argument. This
	// assertion is a direct, sufficient proof, made umask-independent above.
	if err := savePRStatusCacheInjected(path, map[string]prStatusCacheEntry{"acme/widget#feature": {Open: true}}, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("published cache mode = %v, want 0644", info.Mode().Perm())
	}
}
