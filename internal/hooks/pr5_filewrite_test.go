package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR5 is task-9 PR-5's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR5 = errors.New("pr5 boom")

// The following tests exercise the filewrite.Injector-reachable error
// branches of task-9 PR-5's two internal/hooks call sites, plus a
// leftover-temp-file check on each (review-756 B1) and a published-mode
// check on each (review-756 B2, with review-763's chmod-preset fix for
// writeExecutableAt so a missing ChmodFile call is actually detectable).

func newTestManagedHooksDirectory(t *testing.T, dir string) managedHooksDirectory {
	t.Helper()
	handle, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	return managedHooksDirectory{path: dir, commonPath: dir, common: handle, directory: handle}
}

// --- writeExecutableAtInjected ---

func TestWriteExecutableAtInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepClose, filewrite.StepRenameNoReplace,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			managed := newTestManagedHooksDirectory(t, dir)
			inj := &filewrite.Injector{Step: step, Err: errBoomPR5}
			err := writeExecutableAtInjected(managed, "pre-commit", []byte("#!/bin/sh\n"), absentManagedHookIdentity(), nil, inj)
			if !errors.Is(err, errBoomPR5) {
				t.Fatalf("writeExecutableAtInjected(%s failure) = %v, want errBoomPR5", step, err)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "pre-commit")); !os.IsNotExist(statErr) {
				t.Fatalf("failed write published a visible hook: %v", statErr)
			}
			assertNoBareTemporaryHook(t, dir, "pre-commit")
		})
	}
}

func TestWriteExecutableAtPublishesAt0755(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	managed := newTestManagedHooksDirectory(t, dir)
	// review-763's chmod-preset fix: a Hook fires immediately before the
	// real ChmodFile syscall (whatever the temporary's random name turned
	// out to be) and pre-sets it to a mode that is NOT the final published
	// mode. filewrite.CreateExclusive already creates the temp file at
	// 0o755 itself (unlike a CreateTemp-based site, which always gets
	// os.CreateTemp's own 0600 regardless of what a caller wants), so
	// without this preset, deleting the ChmodFile call could not be
	// detected here either: the file would already be 0755 from creation.
	hookRan := false
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Hook: func() {
		hookRan = true
		matches, err := filepath.Glob(filepath.Join(dir, ".pre-commit.wb-tmp-*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("locate temporary hook before chmod: matches=%v err=%v", matches, err)
		}
		if err := os.Chmod(matches[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	if err := writeExecutableAtInjected(managed, "pre-commit", []byte("#!/bin/sh\n"), absentManagedHookIdentity(), nil, inj); err != nil {
		t.Fatal(err)
	}
	if !hookRan {
		t.Fatal("Hook did not run before the real chmod")
	}
	info, err := os.Stat(filepath.Join(dir, "pre-commit"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("published hook mode = %v, want 0755", info.Mode().Perm())
	}
}

// TestWriteExecutableAtInjectedRestoresQuarantinedHookOnRenameFailure covers
// review-767's B2 finding: manager.go:1123's restore call (and its own
// filewrite.RenameNoReplace publish attempt) had no test, because every
// other fault-injection subtest passes absentManagedHookIdentity(), so
// quarantineManagedHook never parks an existing hook and parkedName is
// always "". Installing a real prior hook and injecting the primary
// publish's own StepRenameNoReplace failure forces the restore branch, and
// proves the restore is unaffected by the injected failure -- exactly why
// the restore call passes a nil Injector rather than reusing inj.
func TestWriteExecutableAtInjectedRestoresQuarantinedHookOnRenameFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	managed := newTestManagedHooksDirectory(t, dir)
	original := "#!/bin/sh\necho original\n"
	mustWriteExecutable(t, filepath.Join(dir, "pre-commit"), original)
	identity, err := managedHookIdentityAt(managed.directory, "pre-commit")
	if err != nil {
		t.Fatal(err)
	}

	inj := &filewrite.Injector{Step: filewrite.StepRenameNoReplace, Err: errBoomPR5}
	writeErr := writeExecutableAtInjected(managed, "pre-commit", []byte("#!/bin/sh\necho new\n"), identity, nil, inj)
	if !errors.Is(writeErr, errBoomPR5) {
		t.Fatalf("writeExecutableAtInjected(rename failure) = %v, want errBoomPR5", writeErr)
	}
	if restored := mustReadFile(t, filepath.Join(dir, "pre-commit")); restored != original {
		t.Fatalf("original hook was not restored: got %q, want %q", restored, original)
	}
	restoredIdentity, err := managedHookIdentityAt(managed.directory, "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	if restoredIdentity != identity {
		t.Fatalf("restored hook identity = %+v, want the original %+v", restoredIdentity, identity)
	}
}

// --- savePRStatusCacheInjected ---

func TestSavePRStatusCacheInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepWrite, filewrite.StepRename} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "cache.json")
			inj := &filewrite.Injector{Step: step, Err: errBoomPR5}
			err := savePRStatusCacheInjected(path, map[string]prStatusCacheEntry{"acme/widget#feature": {Open: true}}, inj)
			if !errors.Is(err, errBoomPR5) {
				t.Fatalf("savePRStatusCacheInjected(%s failure) = %v, want errBoomPR5", step, err)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("failed save published a visible cache: %v", statErr)
			}
			assertNoLeftoverPR5TempFile(t, dir, "cache.json.tmp")
		})
	}
}

// TestSavePRStatusCacheInjectedRefusesUnmarshalableEntry covers review-767's
// B3 finding: the return err after json.Marshal (changed from the
// pre-migration code's bare return, since that path is what this test
// reaches) had no test. json.Marshal fails encoding a time.Time whose year
// falls outside [0,9999]; CheckedAt is exactly such a field.
func TestSavePRStatusCacheInjectedRefusesUnmarshalableEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	err := savePRStatusCacheInjected(path, map[string]prStatusCacheEntry{
		"acme/widget#feature": {Open: true, CheckedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
	}, nil)
	if err == nil {
		t.Fatal("savePRStatusCacheInjected(unmarshalable CheckedAt) = nil, want an error")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("failed save published a visible cache: %v", statErr)
	}
	assertNoLeftoverPR5TempFile(t, dir, "cache.json.tmp")
}

// TestSavePRStatusCachePublishesAt0644 lives in pr5_filewrite_umask_test.go
// (review-767 N5): it pins the process umask, which needs a !windows build
// tag (Windows has no umask concept and golang.org/x/sys/unix does not
// build there).

// assertNoBareTemporaryHook asserts writeExecutableAtInjected's random
// temporary name for name is gone from dir -- either it was never created,
// or a failure's defer quarantined it under a further ".wb-backup-<hex>"
// suffix (a real file this migration is not responsible for cleaning up:
// quarantineManagedHook's own no-clobber preservation protocol, pre-dating
// this task). A loose glob for "*wb-tmp-*" would false-positive on that
// quarantined name, since quarantine appends rather than replaces, so this
// matches the bare temporary name exactly instead.
func assertNoBareTemporaryHook(t *testing.T, dir, name string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	bareTemp := regexp.MustCompile(`^\.` + regexp.QuoteMeta(name) + `\.wb-tmp-[0-9a-f]{32}$`)
	for _, entry := range entries {
		if bareTemp.MatchString(entry.Name()) {
			t.Fatalf("bare temporary hook left behind after a failed publish: %s", entry.Name())
		}
	}
}

func assertNoLeftoverPR5TempFile(t *testing.T, dir, pattern string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temp file(s) after a failed publish: %v", matches)
	}
}
