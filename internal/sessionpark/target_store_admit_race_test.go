package sessionpark

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// TestAdmitFallsBackToOpeningTheAdmitLockAfterACompetingCreate is task-9
// PR-1 review N4's restoration of the end-to-end sessionpark race test
// the migration to internal/filewrite deleted (target_store_hook_race_test.go,
// which drove sessionpark's own now-removed openOrCreateRegularAt through a
// package-level test-only hook var). It exercises the same real race --
// TargetStore.Admit's admit-lock create losing to a competing creator's
// O_CREAT|O_EXCL, so admit falls back to a plain open of the winner's file
// -- but end to end through the real Admit/admit code path (not just
// internal/filewrite's own unit tests), using filewrite.Injector's Hook
// (an explicit argument, never a package var) instead of a scheduler race:
// the Hook fires deterministically, from inside filewrite.OpenOrCreateRegular,
// after admit's own initial open finds the admit-lock name absent but
// before its O_CREAT|O_EXCL create, guaranteeing that create observes a
// real EEXIST every time.
func TestAdmitFallsBackToOpeningTheAdmitLockAfterACompetingCreate(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "target-store")
	store := NewTargetStore(root)
	raw := targetEnvelopeForTest(t)
	envelope, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	admitName := ".admit-" + envelope.Request.ResumeID + ".lock"

	var competingFD int
	hookCalls := 0
	inj := &filewrite.Injector{
		Step: filewrite.StepOpenOrCreate,
		Name: admitName,
		Skip: 1, // let admit's own real initial (non-creating) open through.
		Hook: func() {
			hookCalls++
			storeRoot, mkdirErr := cleanAbsoluteStoreRoot(store.Root)
			if mkdirErr != nil {
				t.Fatalf("competing creator: resolve store root: %v", mkdirErr)
			}
			directoryFD, openErr := unix.Open(storeRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if openErr != nil {
				t.Fatalf("competing creator: open store root: %v", openErr)
			}
			defer func() { _ = unix.Close(directoryFD) }()
			fd, createErr := unix.Openat(directoryFD, admitName,
				unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
			if createErr != nil {
				t.Fatalf("competing creator inside hook: %v", createErr)
			}
			competingFD = fd
		},
		FailAfterHook: true, // the occurrence right after Hook must still succeed for real (EEXIST), not be overridden.
	}
	t.Cleanup(func() {
		if competingFD != 0 {
			_ = unix.Close(competingFD)
		}
	})

	admission, err := store.admit(raw, inj)
	if hookCalls != 1 {
		t.Fatalf("competing-creator hook ran %d times, want exactly 1", hookCalls)
	}
	if err != nil {
		t.Fatalf("admit with a competing admit-lock creator = %v, want it to fall back to opening the winner's file", err)
	}
	if admission.Envelope.Request.ResumeID != envelope.Request.ResumeID {
		t.Fatalf("admission envelope resume ID = %q, want %q", admission.Envelope.Request.ResumeID, envelope.Request.ResumeID)
	}

	// admit must have proceeded past the race using the exact file the
	// competing creator made (fstat identity checked inside
	// filewrite.OpenOrCreateRegular itself), not a second admit-lock file,
	// and must have gone on to persist the aggregate normally.
	for _, name := range []string{EnvelopeFileName, ContinuationFileName} {
		info, statErr := os.Stat(filepath.Join(root, envelope.Request.ResumeID, name))
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s info=%v err=%v", name, info, statErr)
		}
	}
}
