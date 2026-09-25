// Package testsweep is spec/plans/coverage-to-100/README.md task-8's
// generic fail-call-N sweep (decision 23): given a happy-path test body, it
// counts the external calls the body makes, reruns the body once per call
// with that call failing, and hands each run's outcome to the test to
// assert -- so one sweep call covers every error return a body reaches
// through a sequence of calls, instead of one hand-written failure test per
// call.
//
// Sweep depends on nothing from internal/runner/runnertest or
// internal/gitcli: it is written against the two-method [Failer] interface
// alone, so any scriptable fake that counts its calls and can be told to
// fail one of them plugs in the same way. internal/runner/runnertest.Fake
// implements it today; task-9's file-write injector is designed to grow the
// same two methods next. See the package example for a fake that has
// nothing to do with either of those, to prove the point.
//
// # Usage
//
// Write the fake's happy path once, as a body that takes the fake and
// returns an error:
//
//	body := func(fake *runnertest.Fake) error {
//		fake.ExpectArgv([]string{"git", "fetch", "--quiet", "origin"}, runner.Result{}, nil)
//		return gitcli.New(fake).Fetch(context.Background(), "/repo", "origin")
//	}
//
// Then hand Sweep a constructor for a fresh fake, the error to inject, the
// body, and a check that runs once per call number:
//
//	testsweep.Sweep(t, func() *runnertest.Fake { return runnertest.New(t) }, errBoom,
//		body,
//		func(t testing.TB, callNum, total int, err error) {
//			if !errors.Is(err, errBoom) {
//				t.Fatalf("call %d/%d: err = %v, want it to wrap errBoom", callNum, total, err)
//			}
//		})
//
// Sweep calls the constructor once to run body with no injected failure (the
// dry run), to learn how many calls a happy path makes. It then calls the
// constructor once per call number from 1 to that count, arranges (through
// [Failer.FailCall]) for that one call to fail, reruns body, and passes the
// call number, the dry run's total, and body's returned error to check.
package testsweep

import "testing"

// Failer is implemented by a scriptable fake with a fail-call-N mode: it
// counts each external call it answers, in the order it answers them, and
// can be told, before a run, to make exactly one of those calls fail
// instead of returning its normal result.
type Failer interface {
	// CallCount reports how many calls have been answered so far.
	CallCount() int
	// FailCall arranges for the callNum'th call (1-indexed) this Failer
	// answers to fail with err instead of succeeding. Every other call
	// keeps returning its own scripted result -- FailCall affects exactly
	// one call number. It must be set before the call it targets is made.
	FailCall(callNum int, err error)
}

// Sweep drives one happy-path body once per external call it makes.
//
// It first calls newFake to build a fresh [Failer] and runs body against it
// with no injected failure -- the dry run -- to count body's calls. A dry
// run that returns a non-nil error, or makes no calls at all, is not a
// happy path, and Sweep fails t rather than guessing what to sweep.
//
// Then, for each call number from 1 to the dry run's count, Sweep builds
// another fresh Failer, arranges (via [Failer.FailCall]) for that call to
// fail with failErr, runs body again, and passes the call number, the dry
// run's total, and body's returned error to check.
//
// Sweep assumes a well-behaved body stops making calls as soon as one
// fails: it fails t directly, rather than passing a misleading result to
// check, when a faulty run either returns a nil error despite the injected
// failure, or has answered a different number of calls than the call
// number it was told to fail once it returns -- a body that swallows the
// error and keeps going, retries, or otherwise makes a non-deterministic
// number of calls before stopping.
func Sweep[F Failer](t testing.TB, newFake func() F, failErr error, body func(F) error, check func(t testing.TB, callNum, total int, err error)) {
	t.Helper()

	dry := newFake()
	if err := body(dry); err != nil {
		t.Fatalf("testsweep: dry run body returned %v, want a nil error (Sweep starts from a happy-path body)", err)
	}
	total := dry.CallCount()
	if total == 0 {
		t.Fatalf("testsweep: dry run body made no calls; nothing to sweep")
	}

	for callNum := 1; callNum <= total; callNum++ {
		fake := newFake()
		fake.FailCall(callNum, failErr)

		err := body(fake)
		if err == nil {
			t.Fatalf("testsweep: call %d/%d: body returned a nil error although call %d was made to fail", callNum, total, callNum)
		}
		if seen := fake.CallCount(); seen != callNum {
			t.Fatalf("testsweep: call %d/%d: body answered %d call(s) before returning, want exactly %d; the call sequence is not deterministic, or the body kept calling after the failure", callNum, total, seen, callNum)
		}
		check(t, callNum, total, err)
	}
}
