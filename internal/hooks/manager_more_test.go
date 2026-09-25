package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestShimManagedSectionEmitsMigrationCompatMarkerWhenLegacyAllowed covers
// the wbHomeAllowsLegacy branch: when a caller says the resolved WB_HOME is
// also the default legacy location, the generated shim must additionally
// export the compatibility marker pinned to that same home so a stale
// checkout of the shim cannot accidentally read a different legacy home.
func TestShimManagedSectionEmitsMigrationCompatMarkerWhenLegacyAllowed(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	section := shimManagedSection("", "pre-commit", "", "", home, true)
	if !strings.Contains(section, "export WB_HOME="+shellQuote(home)) {
		t.Fatalf("shim section = %q, want a WB_HOME export for %s", section, home)
	}
	wantCompat := "export " + wbhome.EnvMigrationCompat + "=" + shellQuote(home)
	if !strings.Contains(section, wantCompat) {
		t.Fatalf("shim section = %q, want the legacy-compat marker %q", section, wantCompat)
	}

	// The same call with wbHomeAllowsLegacy=false must NOT emit the marker.
	without := shimManagedSection("", "pre-commit", "", "", home, false)
	if strings.Contains(without, wbhome.EnvMigrationCompat) {
		t.Fatalf("shim section (legacy disallowed) = %q, want no legacy-compat marker", without)
	}
}

// TestWriteExecutableAtInjectedExhaustsTemporaryNameCollisionRetries covers
// the retry loop's EEXIST-continue branch and its final "give up after 16
// attempts" error: an Injector that reports EEXIST for every StepOpenOrCreate
// occurrence forces every collision-avoidance attempt to fail the same way,
// so the loop must retry all 16 times and then refuse rather than loop
// forever or silently succeed.
func TestWriteExecutableAtInjectedExhaustsTemporaryNameCollisionRetries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	managed := newTestManagedHooksDirectory(t, dir)
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: unix.EEXIST}
	err := writeExecutableAtInjected(managed, "pre-commit", []byte("#!/bin/sh\n"), absentManagedHookIdentity(), nil, inj)
	if err == nil || !strings.Contains(err.Error(), "create collision-free temporary hook") {
		t.Fatalf("writeExecutableAtInjected(exhausted retries) = %v, want a collision-free-temp-hook refusal", err)
	}
	// No hook file, published or temporary, should have been left behind:
	// every attempt reported EEXIST from the fake, so CreateExclusive never
	// actually created anything for this call to leak.
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("directory after exhausted retries = %v, want empty", entries)
	}
}

// TestOpenManagedHooksDirectoryRefusesUnreadableManagedDirectory covers the
// branch where the managed hooks directory already exists (so Mkdirat is a
// no-op) but cannot be opened for reading: unlike the "occupied by a plain
// file" case, this is a real directory the descriptor-anchored open itself
// refuses.
func TestOpenManagedHooksDirectoryRefusesUnreadableManagedDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	t.Parallel()
	commonPath := t.TempDir()
	managed := filepath.Join(commonPath, "wb-hooks")
	if err := os.Mkdir(managed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(managed, 0o755) })
	_, err := openManagedHooksDirectory("", managed, nil)
	if err == nil || !strings.Contains(err.Error(), "open managed hooks directory") {
		t.Fatalf("openManagedHooksDirectory(unreadable managed dir) error = %v, want an open-managed-hooks-directory refusal", err)
	}
	if !errors.Is(err, os.ErrPermission) && !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("openManagedHooksDirectory(unreadable managed dir) error = %v, want a permission error wrapped in", err)
	}
}
