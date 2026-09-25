package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR6 is task-9 PR-6's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR6 = errors.New("pr6 boom")

// TestWriteExecutableAtInjectedReportsRestoreFailureIfQuarantinedHookCannotBeReinstated
// covers manager.go:1124-1125's restore-failure branch (review-767's
// optional, non-blocking follow-up): every existing test for the primary
// publish's failure path (including review-767's B2 test) leaves the
// restore succeeding, because nothing else ever recreates name between
// quarantine and restore.
//
// The restore call deliberately passes a nil *filewrite.Injector (B2), so
// this branch cannot be reached by injecting a failure on the restore
// itself -- it needs a genuine OS-level conflict. This test builds one:
// an Injector.Hook fires immediately before the primary publish's
// injected StepRenameNoReplace failure is returned, and in that Hook a
// competing actor recreates name (quarantine already moved the original
// out of the way, so name is briefly absent) with different content --
// exactly the kind of real race the no-clobber restore rename exists to
// detect. The restore's own real renameat2(RENAME_NOREPLACE) syscall then
// genuinely fails with EEXIST, with no injection involved.
func TestWriteExecutableAtInjectedReportsRestoreFailureIfQuarantinedHookCannotBeReinstated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	managed := newTestManagedHooksDirectory(t, dir)
	original := "#!/bin/sh\necho original\n"
	mustWriteExecutable(t, filepath.Join(dir, "pre-commit"), original)
	identity, err := managedHookIdentityAt(managed.directory, "pre-commit")
	if err != nil {
		t.Fatal(err)
	}

	competingContent := "#!/bin/sh\necho competitor\n"
	hookRan := false
	inj := &filewrite.Injector{Step: filewrite.StepRenameNoReplace, Err: errBoomPR6, Hook: func() {
		hookRan = true
		// quarantine already moved "pre-commit" out of the way by the time
		// this Hook runs (it fires immediately before the primary publish's
		// own StepRenameNoReplace occurrence), so this recreates a genuine
		// conflict for the restore's own real, uninjected rename to trip on.
		mustWriteExecutable(t, filepath.Join(dir, "pre-commit"), competingContent)
	}}

	writeErr := writeExecutableAtInjected(managed, "pre-commit", []byte("#!/bin/sh\necho new\n"), identity, nil, inj)
	if !hookRan {
		t.Fatal("Hook did not run before the primary publish's injected failure")
	}
	if !errors.Is(writeErr, errBoomPR6) {
		t.Fatalf("writeExecutableAtInjected(unrestorable quarantine) = %v, want it to wrap errBoomPR6", writeErr)
	}
	// manager.go's restore-failure message names both the primary error and
	// the restore error, so its text is the only observable proof the
	// restore branch actually ran (rather than, say, silently swallowing
	// the failed restore).
	if !regexp.MustCompile(`preserve quarantined hook`).MatchString(writeErr.Error()) {
		t.Fatalf("writeExecutableAtInjected(unrestorable quarantine) = %q, want it to mention the preserved quarantined hook", writeErr.Error())
	}

	if got := mustReadFile(t, filepath.Join(dir, "pre-commit")); got != competingContent {
		t.Fatalf("competing content at pre-commit = %q, want the competitor's %q (restore must not have clobbered it)", got, competingContent)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	quarantined := regexp.MustCompile(`^pre-commit\.wb-backup-[0-9a-f]{32}$`)
	found := false
	for _, entry := range entries {
		if quarantined.MatchString(entry.Name()) {
			found = true
			if got := mustReadFile(t, filepath.Join(dir, entry.Name())); got != original {
				t.Fatalf("quarantined hook %s content = %q, want the original %q", entry.Name(), got, original)
			}
		}
	}
	if !found {
		t.Fatal("original hook was not preserved under a quarantine name after the restore itself failed")
	}
}
