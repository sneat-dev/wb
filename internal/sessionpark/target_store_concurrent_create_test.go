package sessionpark

import (
	"os"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

// TestOpenOrCreateRegularAtLoserOfConcurrentCreateReopensExisting
// deterministically drives openOrCreateRegularAt's EEXIST retry branch
// (internal/sessionpark/target_store.go: the second unix.Openat call,
// without O_CREAT, taken when the O_CREAT|O_EXCL attempt loses a race for a
// name that did not exist at the first, plain Openat check). Production
// reaches it when two concurrent admitters (an admit-marker write and a
// resume-lock acquisition are its two call sites) both find a name absent
// and then both try to create it — only one O_CREAT|O_EXCL can win, and the
// other must fall back to opening what the winner just created. Nothing in
// the existing sequential tests
// (TestSpCovWriteImmutableAndExactPrivateArtifacts, which always calls this
// function once a file is already known to exist or already known to be
// absent) drives two concurrent creators of the same brand-new name, so
// this branch's coverage varied between identical nightly runs (6/8).
//
// With N >= 2 concurrent callers racing O_CREAT|O_EXCL for the same name,
// exactly one always wins the create (the flag pair is atomic at the
// filesystem level) and every other caller is guaranteed to observe EEXIST
// and take the fallback open — this is deterministic in outcome, not a
// timing-dependent race, though which caller wins is still up to the
// scheduler.
func TestOpenOrCreateRegularAtLoserOfConcurrentCreateReopensExisting(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	directoryFD := int(directory.Fd())

	const callers = 32
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	fds := make([]int, callers)
	errs := make([]error, callers)
	for index := 0; index < callers; index++ {
		go func(index int) {
			defer done.Done()
			ready.Done()
			<-start
			fds[index], errs[index] = openOrCreateRegularAt(directoryFD, "contested.lock", 0o600)
		}(index)
	}
	ready.Wait()
	close(start)
	done.Wait()

	for index, callErr := range errs {
		if callErr != nil {
			t.Fatalf("caller %d error = %v, want nil (every racer must end up with a valid fd)", index, callErr)
		}
		if fds[index] < 0 {
			t.Fatalf("caller %d fd = %d, want a valid descriptor", index, fds[index])
		}
	}
	for index := range fds {
		_ = unix.Close(fds[index])
	}

	info, err := os.Stat(directory.Name() + "/contested.lock")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("contested.lock mode = %v, want a 0600 regular file", info.Mode())
	}
}
