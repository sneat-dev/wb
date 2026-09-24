package sessionmove

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestRepairPendingLinkAtConcurrentCallersRaceOpeningARemovedPendingEntry
// deterministically drives repairPendingLinkAt's open-then-ENOENT-continue
// branch (internal/sessionmove/store.go's "if errors.Is(err, unix.ENOENT) {
// continue }" inside the pending-name scan loop): it only fires when a
// pending link that one caller's directory listing named has already been
// removed by someone else by the time that caller gets around to opening
// it — a genuine TOCTOU between two concurrent repairs of the same
// directory. Production reaches it when two callers both read the same
// immutable artifact (readImmutableAt calls repairPendingLinkAt on every
// read) after an interrupted publication left duplicate pending links
// behind; nothing in the existing single-caller repair tests
// (TestSmCovStoreRepairPendingLinkAtReachableBranches) drives two repairs
// at once, so this branch's coverage varied between identical nightly runs
// (1/8).
//
// The setup creates several duplicate pending hard links to one final
// file (as repeated crashed publications would) and releases many
// goroutines from one shared barrier to repair the same directory at
// once. Because every duplicate is present at every goroutine's first
// directory listing (nothing is removed before any listing happens), each
// duplicate is certain to be matched by at least one goroutine's scan —
// but with several goroutines racing to open and remove the same
// duplicate names, at least one of them is virtually certain to lose an
// open race on at least one entry, on every run. The test repeats the
// scenario several times to make that virtual certainty a practical one.
func TestRepairPendingLinkAtConcurrentCallersRaceOpeningARemovedPendingEntry(t *testing.T) {
	const (
		trials     = 20
		duplicates = 12
		callers    = 8
	)
	for trial := 0; trial < trials; trial++ {
		t.Run(strconv.Itoa(trial), func(t *testing.T) {
			directory := t.TempDir()
			target := filepath.Join(directory, "artifact")
			if err := os.WriteFile(target, []byte("payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			for duplicate := 0; duplicate < duplicates; duplicate++ {
				pendingName := fmt.Sprintf(".pending-%032x", duplicate+1)
				if err := os.Link(target, filepath.Join(directory, pendingName)); err != nil {
					t.Fatal(err)
				}
			}

			authority := smCovOpenDirectory(t, directory)
			start := make(chan struct{})
			var ready, done sync.WaitGroup
			ready.Add(callers)
			done.Add(callers)
			errs := make([]error, callers)
			for index := 0; index < callers; index++ {
				go func(index int) {
					defer done.Done()
					ready.Done()
					<-start
					errs[index] = repairPendingLinkAt(authority, "artifact")
				}(index)
			}
			ready.Wait()
			close(start)
			done.Wait()

			for index, repairErr := range errs {
				if repairErr != nil && !strings.Contains(repairErr.Error(), "not single-link after pending-link repair") {
					t.Fatalf("caller %d error = %v, want nil or a not-single-link race outcome", index, repairErr)
				}
			}

			// Every concurrent repair has now returned, so every duplicate
			// this trial created has been matched by some goroutine's scan
			// and removed (an ENOENT on open just means a different
			// goroutine's Unlinkat already won that entry) — the final file
			// must be single-link again regardless of which goroutine did
			// the removing.
			for duplicate := 0; duplicate < duplicates; duplicate++ {
				pendingName := fmt.Sprintf(".pending-%032x", duplicate+1)
				if _, err := os.Stat(filepath.Join(directory, pendingName)); !os.IsNotExist(err) {
					t.Fatalf("pending link %s remains: %v", pendingName, err)
				}
			}
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if !info.Mode().IsRegular() {
				t.Fatalf("repaired artifact mode = %v, want a regular file", info.Mode())
			}
		})
	}
}
