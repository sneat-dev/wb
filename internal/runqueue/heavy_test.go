package runqueue

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestHeavyShareTiers pins the k-tiered share formula directly (lead
// design formalizing the founder's "If no queue we can start 100%. If
// something is running and queue has more than 2 items start 2 with 2/3.
// ... If 3 items in queue start 3 each 50% of cores", sneat-dev/wb#621):
// with N = NumCPU, k=1 gives N, k=2 gives N*2/3, k>=3 gives N/2.
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

// TestHeavyAdmissionCandidateWorkedExamples reproduces every worked case
// from the lead's N=18 walkthrough (cap 27 = 1.5*18, formalizing the
// founder's 150% cap remark) directly against the pure candidate function,
// with no goroutines or real waiting needed.
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
// admitHeavyAsync's goroutine closes resultCh when it returns, whether or
// not it ever sent an admission — a caller with a cancellable ctx can join
// it deterministically with a single `<-resultCh` after calling cancel
// (see joinCancelledAdmit), instead of leaking it past the test (review
// finding, PR #628, B2).
func admitHeavyAsync(t *testing.T, ctx context.Context, root string, argv []string, summary string) (<-chan Admission, *Ticket) {
	t.Helper()
	self := heavyTestParticipant(summary)
	ticket := RegisterForAdmission(root, argv, self)
	resultCh := make(chan Admission, 1)
	go func() {
		defer close(resultCh)
		admission, err := Admit(ctx, root, argv, self, ticket)
		if err != nil {
			// A cancelled/expired ctx is the expected outcome for a caller
			// that intentionally abandons a still-waiting Admit (see
			// joinCancelledAdmit) — only an unexpected error is a test bug.
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Admit(%v) = %v", argv, err)
			}
			return
		}
		resultCh <- admission
	}()
	return resultCh, ticket
}

// admitTicketAsync runs Admit for an already-registered ticket in a
// goroutine, returning a channel for its result. Unlike admitHeavyAsync,
// it does not register the ticket itself: a caller that wants several
// jobs to have "arrived together" in a fixed, deterministic FIFO order
// must register every ticket first (synchronously, back-to-back, before
// any of them race for admission) and only then launch each one's Admit
// concurrently with this helper — registering and launching each job one
// at a time via admitHeavyAsync does not give that guarantee, because
// nothing stops the first job's Admit goroutine from running its whole
// admission loop, including reading the waiting count for its share
// computation, before a later job's ticket is even registered.
func admitTicketAsync(t *testing.T, ctx context.Context, root string, argv []string, self Participant, ticket *Ticket) <-chan Admission {
	t.Helper()
	resultCh := make(chan Admission, 1)
	go func() {
		defer close(resultCh)
		admission, err := Admit(ctx, root, argv, self, ticket)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Admit(%v) = %v", argv, err)
			}
			return
		}
		resultCh <- admission
	}()
	return resultCh
}

// joinCancelledAdmit cancels a still-waiting admitHeavyAsync call and
// blocks until its goroutine has actually exited (resultCh closes), so a
// deferred cleanup never returns while that goroutine is still running.
func joinCancelledAdmit(t *testing.T, cancel context.CancelFunc, resultCh <-chan Admission) {
	t.Helper()
	cancel()
	select {
	case <-resultCh:
	case <-time.After(2 * time.Second):
		t.Error("Admit goroutine did not exit within 2s of cancel")
	}
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

// TestAdmitHeavyWorkedSequenceAtN18 drives the same sequence of arrivals
// and departures from the lead's N=18 walkthrough (cap 27, formalizing the
// founder's 150%-cap remark): one alone gets the whole machine, a second
// running job throttles to the 150%-cap remainder rather than its own
// k-tier share, a third waits for room, and once a slot frees the waiter
// is admitted at the share for k at that moment — and a job that ends up
// last, once everyone else has finished, gets the whole machine again.
func TestAdmitHeavyWorkedSequenceAtN18(t *testing.T) {
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

	// third never gets admitted in this test (the heavy pool stays
	// saturated), so its Admit goroutine must be cancelled and joined
	// before the test returns, not left running past it.
	thirdCtx, thirdCancel := context.WithCancel(context.Background())
	thirdCh, thirdTicket := admitHeavyAsync(t, thirdCtx, root, broadArgv, "third")
	defer thirdTicket.Forget()
	defer joinCancelledAdmit(t, thirdCancel, thirdCh)
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

// TestAdmitHeavySimultaneousArrivalsRespectTheCap drives the lead's N=18
// "two/three submitted together" worked examples (heavy.go's top-of-file
// doc comment) through real concurrent Admit calls, not just the pure
// heavyAdmissionCandidate function: two heavy jobs registered back-to-back
// (deterministic FIFO order) and then admitted via their own concurrent
// goroutines land at 12+12=24, and three land at 9+9+9=27 — both within
// the 150% (27) cap. Review finding (PR #628, M8).
func TestAdmitHeavySimultaneousArrivalsRespectTheCap(t *testing.T) {
	defer SetNumCPUForTest(18)()

	t.Run("two together", func(t *testing.T) {
		root := t.TempDir()
		ctx := context.Background()
		// Register both tickets before either one races for admission, so
		// "arrived together" is deterministic FIFO order rather than a
		// race between one job's admission loop and the other's
		// registration.
		firstSelf, secondSelf := heavyTestParticipant("first"), heavyTestParticipant("second")
		firstTicket := RegisterForAdmission(root, broadArgv, firstSelf)
		secondTicket := RegisterForAdmission(root, broadArgv, secondSelf)
		defer firstTicket.Forget()
		defer secondTicket.Forget()

		firstCh := admitTicketAsync(t, ctx, root, broadArgv, firstSelf, firstTicket)
		secondCh := admitTicketAsync(t, ctx, root, broadArgv, secondSelf, secondTicket)
		first := mustAdmit(t, firstCh, 2*time.Second)
		defer first.Lease.Release()
		second := mustAdmit(t, secondCh, 2*time.Second)
		defer second.Lease.Release()
		if first.Units != 12 || second.Units != 12 {
			t.Fatalf("two together = %d + %d, want 12 + 12", first.Units, second.Units)
		}
		if total := first.Units + second.Units; total > 27 {
			t.Fatalf("two together total = %d, want <= 27 (150%% cap)", total)
		}
	})

	t.Run("three together", func(t *testing.T) {
		root := t.TempDir()
		ctx := context.Background()
		firstSelf, secondSelf, thirdSelf := heavyTestParticipant("first"), heavyTestParticipant("second"), heavyTestParticipant("third")
		firstTicket := RegisterForAdmission(root, broadArgv, firstSelf)
		secondTicket := RegisterForAdmission(root, broadArgv, secondSelf)
		thirdTicket := RegisterForAdmission(root, broadArgv, thirdSelf)
		defer firstTicket.Forget()
		defer secondTicket.Forget()
		defer thirdTicket.Forget()

		firstCh := admitTicketAsync(t, ctx, root, broadArgv, firstSelf, firstTicket)
		secondCh := admitTicketAsync(t, ctx, root, broadArgv, secondSelf, secondTicket)
		thirdCh := admitTicketAsync(t, ctx, root, broadArgv, thirdSelf, thirdTicket)
		first := mustAdmit(t, firstCh, 2*time.Second)
		defer first.Lease.Release()
		second := mustAdmit(t, secondCh, 2*time.Second)
		defer second.Lease.Release()
		third := mustAdmit(t, thirdCh, 2*time.Second)
		defer third.Lease.Release()
		if first.Units != 9 || second.Units != 9 || third.Units != 9 {
			t.Fatalf("three together = %d + %d + %d, want 9 + 9 + 9", first.Units, second.Units, third.Units)
		}
		if total := first.Units + second.Units + third.Units; total > 27 {
			t.Fatalf("three together total = %d, want <= 27 (150%% cap)", total)
		}
	})
}

// TestReadHeavyHoldersReapsACrashedHolder pins the review ask (PR #628,
// M8) that a holder whose process has died — crashed, rather than
// releasing cleanly through Lease.Release — is excluded from
// readHeavyHolders exactly as the legacy pool's readHolderRecords already
// excludes a dead waiter or holder, so its capacity is available again
// instead of leaving the room/k accounting permanently short.
func TestReadHeavyHoldersReapsACrashedHolder(t *testing.T) {
	defer SetNumCPUForTest(18)()
	root := t.TempDir()

	if _, ok := announceHeavyHolder(root, Participant{PID: deadPID, Summary: "crashed"}, 18, time.Now().UTC()); !ok {
		t.Fatal("announceHeavyHolder(dead PID) = false, want true (the record must still be written)")
	}
	if holders := readHeavyHolders(root); len(holders) != 0 {
		t.Fatalf("readHeavyHolders() after a crashed holder = %v, want none (reaped)", holders)
	}

	// A fresh admission should see the machine as idle, not as if the
	// crashed holder's 18 units were still allocated.
	ch, ticket := admitHeavyAsync(t, context.Background(), root, broadArgv, "fresh")
	defer ticket.Forget()
	admission := mustAdmit(t, ch, 2*time.Second)
	defer admission.Lease.Release()
	if admission.Units != 18 {
		t.Fatalf("fresh admission after a crashed holder = %d units, want 18 (whole machine)", admission.Units)
	}
}
