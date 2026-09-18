package runqueue

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestHeavyShareTiers pins the k-tiered share formula directly. Founder
// 2026-09-18 (sneat-dev/wb#621): "With N = NumCPU: k=1: N (100%); k=2:
// N*2/3; k>=3: N/2."
func TestHeavyShareTiers(t *testing.T) {
	defer SetNumCPUForTest(18)()
	cases := []struct {
		k    int
		want int
	}{
		{0, 18}, {1, 18}, {2, 12}, {3, 9}, {4, 9}, {10, 9},
	}
	for _, testCase := range cases {
		if got := heavyShare(testCase.k); got != testCase.want {
			t.Errorf("heavyShare(%d) = %d, want %d", testCase.k, got, testCase.want)
		}
	}
}

// TestHeavyAdmissionCandidateWorkedExamples reproduces every worked case the
// founder gave at N=18 (cap 27 = 1.5*18) directly against the pure
// candidate function, with no goroutines or real waiting needed.
func TestHeavyAdmissionCandidateWorkedExamples(t *testing.T) {
	defer SetNumCPUForTest(18)()

	// "alone: 18."
	if share, k := heavyAdmissionCandidate(nil, 1); share != 18 || k != 1 {
		t.Errorf("alone: share=%d k=%d, want 18, 1", share, k)
	}

	// "one running at 18, a second arrives: min(12, 9) = 9, total 27."
	oneRunning := []Holder{{Units: 18}}
	if share, k := heavyAdmissionCandidate(oneRunning, 1); share != 9 || k != 2 {
		t.Errorf("second arrival: share=%d k=%d, want 9, 2", share, k)
	}

	// "then a third arrives: room 0, so it waits."
	twoRunning := []Holder{{Units: 18}, {Units: 9}}
	if share, k := heavyAdmissionCandidate(twoRunning, 1); share != 0 || k != 3 {
		t.Errorf("third arrival: share=%d k=%d, want 0, 3", share, k)
	}
	if share := 0; share >= numCPU()/heavyShareFloorDivisor {
		t.Fatalf("a 0 share must be below the N/4 floor (%d)", numCPU()/heavyShareFloorDivisor)
	}

	// "when the first finishes: room 18, so the third is admitted at
	// share(k=2) = 12."
	afterFirstFinishes := []Holder{{Units: 9}}
	if share, k := heavyAdmissionCandidate(afterFirstFinishes, 1); share != 12 || k != 2 {
		t.Errorf("third admitted after first finishes: share=%d k=%d, want 12, 2", share, k)
	}

	// "two submitted together from idle ... two queued at once get
	// 12 + 12 = 24." Evaluated sequentially: the first sees both waiting
	// (k=2, nothing running yet); the second sees the first now running
	// plus itself waiting (still k=2).
	firstOfPair, _ := heavyAdmissionCandidate(nil, 2)
	secondOfPair, k := heavyAdmissionCandidate([]Holder{{Units: firstOfPair}}, 1)
	if firstOfPair != 12 || secondOfPair != 12 || k != 2 {
		t.Errorf("pair from idle: first=%d second=%d k=%d, want 12, 12, 2", firstOfPair, secondOfPair, k)
	}
	if total := firstOfPair + secondOfPair; total != 24 {
		t.Errorf("pair from idle total = %d, want 24", total)
	}

	// "three queued together: 9 + 9 + 9 = 27."
	first, _ := heavyAdmissionCandidate(nil, 3)
	second, _ := heavyAdmissionCandidate([]Holder{{Units: first}}, 2)
	third, _ := heavyAdmissionCandidate([]Holder{{Units: first}, {Units: second}}, 1)
	if first != 9 || second != 9 || third != 9 {
		t.Errorf("trio from idle: %d, %d, %d, want 9, 9, 9", first, second, third)
	}
	if total := first + second + third; total != 27 {
		t.Errorf("trio from idle total = %d, want 27 (the 1.5N cap)", total)
	}
}

// heavyTestParticipant is a live, distinguishable Participant for these
// tests: registered PIDs are liveness-checked, so a real, live PID is
// required (see visibility_test.go's deadPID convention for the opposite
// case).
func heavyTestParticipant(summary string) Participant {
	return Participant{PID: os.Getpid(), Summary: summary}
}

// admitHeavyAsync runs Admit for argv in a goroutine and returns a channel
// for its result, so a test can drive several concurrent heavy admissions
// without blocking its own goroutine — and without any real sleep beyond
// the package's own 100ms retry cadence.
func admitHeavyAsync(t *testing.T, ctx context.Context, root string, argv []string, summary string) (<-chan Admission, *Ticket) {
	t.Helper()
	self := heavyTestParticipant(summary)
	ticket := RegisterForAdmission(root, argv, self)
	resultCh := make(chan Admission, 1)
	go func() {
		admission, err := Admit(ctx, root, argv, self, ticket)
		if err != nil {
			t.Errorf("Admit(%v) = %v", argv, err)
			return
		}
		resultCh <- admission
	}()
	return resultCh, ticket
}

func mustAdmit(t *testing.T, ch <-chan Admission, timeout time.Duration) Admission {
	t.Helper()
	select {
	case admission := <-ch:
		return admission
	case <-time.After(timeout):
		t.Fatal("timed out waiting for admission")
		return Admission{}
	}
}

func mustNotAdmitYet(t *testing.T, ch <-chan Admission, within time.Duration) {
	t.Helper()
	select {
	case admission := <-ch:
		t.Fatalf("admitted with %d units, want it still waiting", admission.Units)
	case <-time.After(within):
	}
}

var broadArgv = []string{"go", "test", "./..."}

// TestAdmitHeavyFollowsFounderWorkedSequenceAtN18 drives the same sequence
// of arrivals and departures the founder specified for N=18 (cap 27): one
// alone gets the whole machine, a second running job throttles to the
// 150%-cap remainder rather than its own k-tier share, a third waits for
// room, and once a slot frees the waiter is admitted at the share for k at
// that moment — and a job that ends up last, once everyone else has
// finished, gets the whole machine again.
func TestAdmitHeavyFollowsFounderWorkedSequenceAtN18(t *testing.T) {
	defer SetNumCPUForTest(18)()
	root := t.TempDir()
	ctx := context.Background()

	firstCh, firstTicket := admitHeavyAsync(t, ctx, root, broadArgv, "first")
	first := mustAdmit(t, firstCh, 2*time.Second)
	firstTicket.Forget()
	if first.Units != 18 {
		t.Fatalf("first alone = %d units, want 18", first.Units)
	}

	secondCh, secondTicket := admitHeavyAsync(t, ctx, root, broadArgv, "second")
	second := mustAdmit(t, secondCh, 2*time.Second)
	secondTicket.Forget()
	if second.Units != 9 {
		t.Fatalf("second (capped by 150%% room) = %d units, want 9", second.Units)
	}

	thirdCh, thirdTicket := admitHeavyAsync(t, ctx, root, broadArgv, "third")
	// Room is 27 - (18+9) = 0 < N/4, so the third must keep waiting.
	mustNotAdmitYet(t, thirdCh, 300*time.Millisecond)

	// When the first (18-unit) job finishes, room reopens and the third is
	// admitted at share(k=2) = 12 (itself plus the still-running second).
	first.Lease.Release()
	third := mustAdmit(t, thirdCh, 2*time.Second)
	thirdTicket.Forget()
	if third.Units != 12 {
		t.Fatalf("third admitted after first finished = %d units, want 12", third.Units)
	}

	// Once the second also finishes, the third is the only heavy job left
	// alive; a heavy job that is last, once the others have finished, gets
	// the whole machine again — proven by a brand new arrival finding an
	// empty queue right after the third itself finishes.
	second.Lease.Release()
	third.Lease.Release()
	lastCh, lastTicket := admitHeavyAsync(t, ctx, root, broadArgv, "last")
	last := mustAdmit(t, lastCh, 2*time.Second)
	lastTicket.Forget()
	last.Lease.Release()
	if last.Units != 18 {
		t.Fatalf("last alone once others finished = %d units, want 18", last.Units)
	}
}

// TestAdmitHeavyIsStrictFIFONoBackfill proves a later, smaller-seeming
// arrival never jumps a still-waiting earlier one — "FIFO among heavy
// waiters" replaces backfill-with-aging entirely.
func TestAdmitHeavyIsStrictFIFONoBackfill(t *testing.T) {
	defer SetNumCPUForTest(18)()
	root := t.TempDir()
	ctx := context.Background()

	firstCh, firstTicket := admitHeavyAsync(t, ctx, root, broadArgv, "first")
	first := mustAdmit(t, firstCh, 2*time.Second)
	firstTicket.Forget()

	secondCh, secondTicket := admitHeavyAsync(t, ctx, root, broadArgv, "second")
	second := mustAdmit(t, secondCh, 2*time.Second)
	secondTicket.Forget()
	// Two heavy jobs are now running (18 + 9 = 27, the full cap), so any
	// further heavy arrival must wait regardless of order.
	if first.Units+second.Units != 27 {
		t.Fatalf("running total = %d, want the full 27 cap", first.Units+second.Units)
	}

	thirdCh, thirdTicket := admitHeavyAsync(t, ctx, root, broadArgv, "third")
	mustNotAdmitYet(t, thirdCh, 200*time.Millisecond)
	fourthCh, fourthTicket := admitHeavyAsync(t, ctx, root, broadArgv, "fourth")
	mustNotAdmitYet(t, fourthCh, 200*time.Millisecond)

	// Freeing exactly one unit of room must admit the third (the head of
	// the FIFO queue), never the fourth.
	second.Lease.Release()
	third := mustAdmit(t, thirdCh, 2*time.Second)
	thirdTicket.Forget()
	mustNotAdmitYet(t, fourthCh, 300*time.Millisecond)

	third.Lease.Release()
	first.Lease.Release()
	fourth := mustAdmit(t, fourthCh, 2*time.Second)
	fourthTicket.Forget()
	fourth.Lease.Release()
}

// TestFocusedJobsNeverWaitBehindHeavyOnes proves a focused job is admitted
// immediately even while the heavy pool is fully saturated — "focused jobs
// never wait behind heavy ones."
func TestFocusedJobsNeverWaitBehindHeavyOnes(t *testing.T) {
	defer SetNumCPUForTest(18)()
	root := t.TempDir()
	ctx := context.Background()

	firstCh, firstTicket := admitHeavyAsync(t, ctx, root, broadArgv, "first")
	first := mustAdmit(t, firstCh, 2*time.Second)
	firstTicket.Forget()
	secondCh, secondTicket := admitHeavyAsync(t, ctx, root, broadArgv, "second")
	second := mustAdmit(t, secondCh, 2*time.Second)
	secondTicket.Forget()
	defer first.Lease.Release()
	defer second.Lease.Release()

	thirdCh, thirdTicket := admitHeavyAsync(t, ctx, root, broadArgv, "third")
	defer thirdTicket.Forget()
	mustNotAdmitYet(t, thirdCh, 200*time.Millisecond)

	focusedArgv := []string{"go", "vet", "./internal/runqueue"}
	self := heavyTestParticipant("focused")
	ticket := RegisterForAdmission(root, focusedArgv, self)
	if ticket != nil {
		t.Fatal("a focused job must not register a waiting ticket")
	}
	deadline, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	admission, err := Admit(deadline, root, focusedArgv, self, ticket)
	if err != nil {
		t.Fatalf("focused Admit = %v, want immediate admission despite the saturated heavy pool", err)
	}
	defer admission.Lease.Release()
	if admission.Units != 2 { // max(1, 18/8)
		t.Fatalf("focused share = %d, want max(1, N/8) = 2", admission.Units)
	}
	if admission.Waited != 0 {
		t.Fatalf("focused Waited = %s, want 0 (never queued)", admission.Waited)
	}
}
