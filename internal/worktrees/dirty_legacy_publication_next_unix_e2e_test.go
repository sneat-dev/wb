//go:build e2e && !windows

package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

//nolint:paralleltest // Existing native fixture and immutable-publication hook require serial process-wide state.
func TestE2EDirtyLegacyPublicationRetainsNativeCompetingRecords(t *testing.T) {
	for _, kind := range []string{"recovery", "claim"} {
		//nolint:paralleltest // Each case owns the process-wide immutable-publication hook and native fixture configuration.
		t.Run(kind, func(t *testing.T) {
			f := newLegacyMissingClaimNativeFixture(t, "legacy-publication-"+kind, false)
			plan, err := planLegacyMissingClaimRecovery(f.git.home, f.options, f.entry)
			if err != nil {
				t.Fatal(err)
			}
			target := f.claimPath
			want := "publish recovered immutable claim:"
			if kind == "recovery" {
				target = f.recoveryPath
				want = "write immutable missing-claim recovery receipt:"
				if err := os.Mkdir(filepath.Dir(target), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			directoryPath, targetName := filepath.Dir(target), filepath.Base(target)
			owned, err := os.Open(directoryPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owned.Close() })
			identity, err := owned.Stat()
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadDir(directoryPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{f.claimPath, f.recoveryPath} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("publication prerequisite already exists: %s: %v", path, err)
				}
			}
			unchanged := assertLegacyEvidenceBytesUnchanged(t, f.manifestPath, f.projectionPath, f.outboxPath)
			winner := []byte("native competing immutable record\n")
			called := false
			var recoveryBefore []byte
			original := writeBytesImmutableAtBeforeRename
			t.Cleanup(func() { writeBytesImmutableAtBeforeRename = original })
			writeBytesImmutableAtBeforeRename = func(directory *os.File, name string) {
				if name != targetName {
					original(directory, name)
					return
				}
				actual, err := directory.Stat()
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(identity, actual) {
					original(directory, name)
					return
				}
				if called {
					t.Fatal("target publication hook repeated")
				}
				called = true
				if _, err := filewrite.ReadAt(directory, name); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("target appeared before competing publication: %v", err)
				}
				if kind == "claim" {
					recoveryBefore, err = os.ReadFile(f.recoveryPath)
					if err != nil {
						t.Fatalf("claim publication preceded durable recovery receipt: %v", err)
					}
				}
				// Use the real immutable writer without this hook to publish the
				// competitor after the outer writer's durable temporary is closed.
				// The unchanged outer RenameNoReplace must now observe EEXIST.
				if err := filewrite.WriteBytesImmutableAt(directory, name, winner, 0o600, false, nil); err != nil {
					t.Fatalf("native competing publication: %v", err)
				}
			}
			err = recoverLegacyMissingClaimForAbort(f.git.home, f.options, f.entry)
			if !called || !errors.Is(err, syscall.EEXIST) || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("publication phase refusal: called=%t error=%v", called, err)
			}
			unchanged()
			if stored, err := filewrite.ReadAt(owned, targetName); err != nil || string(stored) != string(winner) {
				t.Fatalf("competing record overwritten: %q %v", stored, err)
			}
			after, err := os.ReadDir(directoryPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before)+1 {
				t.Fatalf("publication left temporary files or removed entries: before=%v after=%v", before, after)
			}
			for _, entry := range before {
				if _, err := os.Lstat(filepath.Join(directoryPath, entry.Name())); err != nil {
					t.Fatalf("publication removed preexisting entry %s: %v", entry.Name(), err)
				}
			}
			if kind == "recovery" {
				if _, err := os.Lstat(f.claimPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("recovery refusal published private claim: %v", err)
				}
			} else {
				if retained, err := os.ReadFile(f.recoveryPath); err != nil || string(retained) != string(recoveryBefore) {
					t.Fatalf("claim refusal changed prior durable recovery bytes: %q %v", retained, err)
				}
				var retained legacyMissingClaimRecovery
				directory, err := os.Open(filepath.Dir(f.recoveryPath))
				if err != nil {
					t.Fatal(err)
				}
				readErr := readJSONAt(directory, filepath.Base(f.recoveryPath), &retained)
				_ = directory.Close()
				planned := plan.recovery
				planned.RecoveredAt = retained.RecoveredAt
				if readErr != nil || retained.RecoveredAt.IsZero() || !reflect.DeepEqual(retained, planned) {
					t.Fatalf("prior durable recovery receipt lost exact authority: %+v %v", retained, readErr)
				}
			}
			if got := gitTestOutput(t, f.entry.WorktreeDir, "rev-parse", "HEAD"); got != f.head {
				t.Fatalf("source HEAD changed: %s", got)
			}
		})
	}
}
