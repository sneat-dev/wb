package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sneat-dev/wb/internal/locallink"
)

// errRwi01WriteAt is the sentinel rwi01FailAtWriter returns on its Nth write,
// letting a test target one specific fmt.Fprint* call's error-check branch.
var errRwi01WriteAt = errors.New("rwi01: write refused")

// rwi01FailAtWriter succeeds on every write except the Nth, so a test can
// prove that printPropagateLocal surfaces a write failure at each individual
// call site rather than only the first.
type rwi01FailAtWriter struct {
	n     int
	count int
}

func (w *rwi01FailAtWriter) Write(p []byte) (int, error) {
	w.count++
	if w.count == w.n {
		return 0, errRwi01WriteAt
	}
	return len(p), nil
}

// TestPrintPropagateLocalEachWriteFailureIsSurfaced drives a write
// failure at every distinct fmt.Fprint* call in printPropagateLocal's text
// path (plan lines, content-hash line, per-identity line, per-consumer
// header, skipped reason, links, skipped-checks, verification statement,
// active links, linked/baseline runs and their details, notes, and the
// closing undo-hint footer) and asserts the specific write error is
// returned rather than swallowed.
func TestPrintPropagateLocalEachWriteFailureIsSurfaced(t *testing.T) {
	t.Parallel()
	result := cwDepsPropagateResultFixture()
	// Call numbers correspond, in order, to: plan header(1), plan item(2,3),
	// content-hash(4), identity(5), consumer[0] header(6), consumer[0]
	// skipped(7), consumer[1] header(8), link(9), skipped-check(10),
	// verification statement(11), active link(12), linked run(13), linked
	// detail(14), published baseline(15), baseline detail(16), note(17),
	// footer(18). Call 1 is already covered elsewhere; 3 and 8 hit the same
	// branch as 2 and 6.
	for _, n := range []int{2, 4, 5, 6, 7, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18} {
		n := n
		t.Run(fmt.Sprintf("call_%d", n), func(t *testing.T) {
			t.Parallel()
			w := &rwi01FailAtWriter{n: n}
			err := printPropagateLocal(cwDepsNewOutCommand(w), "text", result)
			if !errors.Is(err, errRwi01WriteAt) {
				t.Fatalf("printPropagateLocal write failure at call %d: err = %v, want errRwi01WriteAt", n, err)
			}
		})
	}
}

// TestPrintPropagateLocalConsumerErrorWriteFailureIsSurfaced targets
// the per-consumer failure line (result.Consumers[i].Errors), which the
// comprehensive fixture above never populates: a write failure on the
// second write (the failure line itself, after the consumer header) must
// be surfaced.
func TestPrintPropagateLocalConsumerErrorWriteFailureIsSurfaced(t *testing.T) {
	t.Parallel()
	failed := locallink.Result{Consumers: []locallink.ConsumerResult{
		{Consumer: "/tmp/cw/app", Errors: []string{"go.work could not be written"}},
	}}
	w := &rwi01FailAtWriter{n: 2}
	err := printPropagateLocal(cwDepsNewOutCommand(w), "text", failed)
	if !errors.Is(err, errRwi01WriteAt) {
		t.Fatalf("printPropagateLocal consumer-error write failure: err = %v, want errRwi01WriteAt", err)
	}
}
