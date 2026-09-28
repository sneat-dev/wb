package locallink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR9 is task-9 PR-9's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR9 = errors.New("pr9 boom")

// TestContentHashInjectedHonoursInjectedFailures does not run its
// create/close cases under t.Parallel(): both cases glob
// os.TempDir()'s shared "wb-locallink-index-*" namespace to prove what
// each failure leaves on disk, and would otherwise race a sibling case's
// own reservation.
func TestContentHashInjectedHonoursInjectedFailures(t *testing.T) {
	git := ExecGit{}

	glob := func(t *testing.T) []string {
		t.Helper()
		matches, globErr := filepath.Glob(filepath.Join(os.TempDir(), "wb-locallink-index-*"))
		if globErr != nil {
			t.Fatal(globErr)
		}
		return matches
	}

	t.Run("open_or_create leaves nothing behind", func(t *testing.T) {
		before := glob(t)
		inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR9}
		hash, dirty, err := git.contentHashInjected(context.Background(), t.TempDir(), inj)
		if hash != "" || dirty || !errors.Is(err, errBoomPR9) {
			t.Fatalf("contentHashInjected with injected create failure = (%q, %v, %v), want (\"\", false, errBoomPR9)", hash, dirty, err)
		}
		if after := glob(t); len(after) != len(before) {
			t.Fatalf("index reservations before/after an injected create failure = %d/%d, want no new reservation left behind", len(before), len(after))
		}
	})

	t.Run("close leaves the reservation in place, matching the original inline sequence", func(t *testing.T) {
		before := glob(t)
		inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR9}
		hash, dirty, err := git.contentHashInjected(context.Background(), t.TempDir(), inj)
		if hash != "" || dirty || !errors.Is(err, errBoomPR9) {
			t.Fatalf("contentHashInjected with injected close failure = (%q, %v, %v), want (\"\", false, errBoomPR9)", hash, dirty, err)
		}
		// The original inline sequence never removed the reservation on a
		// Close failure either (only the later, deliberate
		// os.Remove(indexPath) freed the name on the success path);
		// migrating to filewrite.CreateScratch preserves that exact
		// on-disk-failure behaviour rather than tightening it.
		after := glob(t)
		if len(after) != len(before)+1 {
			t.Fatalf("index reservations before/after an injected close failure = %d/%d, want exactly one new reservation left in place (original behaviour)", len(before), len(after))
		}
		for _, path := range after {
			isNew := true
			for _, existing := range before {
				if existing == path {
					isNew = false
					break
				}
			}
			if isNew {
				_ = os.Remove(path)
			}
		}
	})
}
