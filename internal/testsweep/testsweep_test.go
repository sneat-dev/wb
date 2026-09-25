package testsweep_test

import (
	"errors"
	"fmt"
	"runtime"
	"testing"

	"github.com/sneat-dev/wb/internal/testsweep"
)

// stepFake is a tiny, standalone Failer: it has nothing to do with
// internal/runner/runnertest or internal/gitcli, and exists only to prove
// Sweep depends on neither -- exactly the decoupling task-9's file-write
// injector needs next. Each call to step records one "step" name; FailCall
// makes one chosen call fail instead of recording success.
type stepFake struct {
	failAt  int
	failErr error
	steps   []string
}

func (f *stepFake) CallCount() int { return len(f.steps) }

func (f *stepFake) FailCall(callNum int, err error) {
	f.failAt = callNum
	f.failErr = err
}

// step is stepFake's one operation: the "external call" Sweep counts and
// can make fail.
func (f *stepFake) step(name string) error {
	f.steps = append(f.steps, name)
	if f.failAt != 0 && len(f.steps) == f.failAt {
		return f.failErr
	}
	return nil
}

// threeStepBody is a happy-path body that makes three of stepFake's calls
// in a fixed order, stopping at the first error -- exactly the shape Sweep
// expects a well-behaved body to have.
func threeStepBody(f *stepFake) error {
	if err := f.step("open"); err != nil {
		return err
	}
	if err := f.step("write"); err != nil {
		return err
	}
	if err := f.step("close"); err != nil {
		return err
	}
	return nil
}

// Example demonstrates Sweep against stepFake, a fake with no relationship
// to internal/runner/runnertest or internal/gitcli, to show that Sweep's
// core is generic: task-9's file-write injector can plug in the same way,
// by growing the same two methods.
func Example() {
	errBoom := errors.New("boom")

	testsweep.Sweep(&recordingTB{}, func() *stepFake { return &stepFake{} }, errBoom,
		threeStepBody,
		func(_ testing.TB, callNum, total int, err error) {
			fmt.Printf("call %d/%d failed: %v\n", callNum, total, errors.Is(err, errBoom))
		})
	// Output:
	// call 1/3 failed: true
	// call 2/3 failed: true
	// call 3/3 failed: true
}

func TestSweepCallsCheckOncePerCallInOrderWithTheDryRunTotal(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")

	var gotCallNums []int
	var gotTotals []int
	testsweep.Sweep(t, func() *stepFake { return &stepFake{} }, errBoom,
		threeStepBody,
		func(_ testing.TB, callNum, total int, err error) {
			gotCallNums = append(gotCallNums, callNum)
			gotTotals = append(gotTotals, total)
			if !errors.Is(err, errBoom) {
				t.Errorf("call %d: err = %v, want it to wrap errBoom", callNum, err)
			}
		})

	wantCallNums := []int{1, 2, 3}
	if len(gotCallNums) != len(wantCallNums) {
		t.Fatalf("check ran %d time(s), want %d", len(gotCallNums), len(wantCallNums))
	}
	for i, want := range wantCallNums {
		if gotCallNums[i] != want {
			t.Errorf("call %d: callNum = %d, want %d", i, gotCallNums[i], want)
		}
		if gotTotals[i] != 3 {
			t.Errorf("call %d: total = %d, want 3", i, gotTotals[i])
		}
	}
}

// runSweep runs run against a fresh *recordingTB on its own goroutine, the
// way the real testing package runs a test function, so that a Fatalf
// inside run stops only that goroutine (via runtime.Goexit, exactly as
// *testing.T.Fatalf does) instead of the test observing it.
func runSweep(run func(tb testing.TB)) *recordingTB {
	fakeT := &recordingTB{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(fakeT)
	}()
	<-done
	return fakeT
}

func TestSweepFailsWhenTheDryRunReturnsAnError(t *testing.T) {
	t.Parallel()
	dryRunErr := errors.New("dry run boom")
	checkRan := false

	// dryRunOnceFake only ever fails its dry run: it ignores FailCall
	// entirely and always succeeds otherwise, so if Sweep did not stop
	// after a failing dry run, the loop below would run to completion
	// (and call check) rather than diverge some other way -- isolating
	// this test to the dry-run-error check alone.
	firstConstruction := true
	newFake := func() *dryRunOnceFake {
		f := &dryRunOnceFake{}
		if firstConstruction {
			f.dryRunErr = dryRunErr
			firstConstruction = false
		}
		return f
	}
	body := func(f *dryRunOnceFake) error { return f.op() }

	fakeT := runSweep(func(tb testing.TB) {
		testsweep.Sweep(tb, newFake, errors.New("injected"), body,
			func(testing.TB, int, int, error) { checkRan = true })
	})

	if !fakeT.fataled {
		t.Fatal("Sweep did not fail t when the dry run body returned an error")
	}
	if checkRan {
		t.Fatal("check ran although the dry run itself failed")
	}
}

// dryRunOnceFake is a Failer built only for
// TestSweepFailsWhenTheDryRunReturnsAnError: its op ignores FailCall
// entirely (unlike stepFake's step), so the only way it can return an
// error is dryRunErr -- set on the fake TestSweepFailsWhenTheDryRunReturnsAnError's
// newFake hands Sweep for its dry run, and left nil for every fake Sweep
// builds afterward.
type dryRunOnceFake struct {
	dryRunErr error
	calls     int
	failAt    int
	failErr   error
}

func (f *dryRunOnceFake) CallCount() int { return f.calls }

func (f *dryRunOnceFake) FailCall(callNum int, err error) {
	f.failAt = callNum
	f.failErr = err
}

func (f *dryRunOnceFake) op() error {
	f.calls++
	if f.dryRunErr != nil {
		return f.dryRunErr
	}
	if f.failAt != 0 && f.calls == f.failAt {
		return f.failErr
	}
	return nil
}

func TestSweepFailsWhenTheDryRunMakesNoCalls(t *testing.T) {
	t.Parallel()
	checkRan := false

	fakeT := runSweep(func(tb testing.TB) {
		testsweep.Sweep(tb, func() *stepFake { return &stepFake{} }, errors.New("boom"),
			func(*stepFake) error { return nil },
			func(testing.TB, int, int, error) { checkRan = true })
	})

	if !fakeT.fataled {
		t.Fatal("Sweep did not fail t when the dry run body made no calls")
	}
	if checkRan {
		t.Fatal("check ran although the dry run made no calls")
	}
}

func TestSweepFailsWhenAFaultyRunReturnsNoError(t *testing.T) {
	t.Parallel()
	checkRan := false

	// swallowsError ignores step's returned error entirely, so a faulty run
	// never reports the failure Sweep injected.
	swallowsError := func(f *stepFake) error {
		_ = f.step("open")
		return nil
	}

	fakeT := runSweep(func(tb testing.TB) {
		testsweep.Sweep(tb, func() *stepFake { return &stepFake{} }, errors.New("boom"), swallowsError,
			func(testing.TB, int, int, error) { checkRan = true })
	})

	if !fakeT.fataled {
		t.Fatal("Sweep did not fail t when a faulty run returned a nil error")
	}
	if checkRan {
		t.Fatal("check ran although the faulty run reported no error")
	}
}

func TestSweepFailsWhenAFaultyRunMakesAnUnexpectedNumberOfCallsBeforeReturning(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")

	// keepsGoingAfterFailure ignores the first step's error and makes a
	// second call anyway, so failing call 1 still leaves the run having
	// answered 2 calls instead of the expected 1 -- a non-deterministic
	// call count Sweep must catch rather than silently pass to check.
	keepsGoingAfterFailure := func(f *stepFake) error {
		firstErr := f.step("open")
		secondErr := f.step("write")
		if firstErr != nil {
			return firstErr
		}
		return secondErr
	}

	var sawCallNum int
	fakeT := runSweep(func(tb testing.TB) {
		testsweep.Sweep(tb, func() *stepFake { return &stepFake{} }, errBoom, keepsGoingAfterFailure,
			func(_ testing.TB, callNum, _ int, _ error) { sawCallNum = callNum })
	})

	if !fakeT.fataled {
		t.Fatal("Sweep did not fail t when a faulty run's call count diverged from its call number")
	}
	// callNum 1 is the diverging case (2 calls answered, not 1) and must
	// stop Sweep before check ever runs for it; callNum 2 fails "write"
	// itself and stops immediately, so Sweep never reaches it once callNum
	// 1 has already failed t.
	if sawCallNum != 0 {
		t.Fatalf("check ran for callNum %d, want it never to run", sawCallNum)
	}
}

// recordingTB is a minimal testing.TB that records whether Fatalf ran and
// mimics *testing.T.Fatalf's runtime.Goexit, instead of the embedded nil
// testing.TB panicking, so a test can assert Sweep detected a bad body
// without a real *testing.T ending the outer test itself.
type recordingTB struct {
	testing.TB
	fataled bool
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(string, ...any) {
	r.fataled = true
	runtime.Goexit()
}
