package runqueue

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestBudgetLeavesOneLogicalCPU(t *testing.T) {
	want := runtime.NumCPU() - 1
	if want < 1 {
		want = 1
	}
	if got := Budget(); got != want {
		t.Fatalf("Budget() = %d, want %d", got, want)
	}
}

func TestUnitsClassifiesHeavyWork(t *testing.T) {
	// N=4 (budget 3): the small-machine table, unchanged since before
	// sneat-dev/wb#621's adaptive heavy-job sharing.
	defer SetNumCPUForTest(4)()
	cases := []struct {
		argv []string
		want int
	}{
		{[]string{"git", "status"}, 0},
		{[]string{"gofmt", "-w", "x.go"}, 0},
		{[]string{"go", "test", "./internal/runlog"}, 1},
		{[]string{"go", "test", "./..."}, 2},
		{[]string{"go", "test", "./internal/..."}, 2},
		{[]string{"go", "test", "-race", "./..."}, 3},
		{[]string{"pnpm", "run", "build"}, 2},
		{[]string{"nx", "test", "app"}, 1},
	}
	for _, testCase := range cases {
		if got := Units(testCase.argv, 3); got != testCase.want {
			t.Errorf("Units(%v, 3) = %d, want %d", testCase.argv, got, testCase.want)
		}
	}
}

// TestClassifyGoRespectsGOFLAGSRaceOrCover pins the review finding (PR
// #628, M7) that -race/-cover given ambiently through the GOFLAGS
// environment variable — not just as an explicit argv flag — must still
// classify a go test/build as KindRaceOrCover.
func TestClassifyGoRespectsGOFLAGSRaceOrCover(t *testing.T) {
	argv := []string{"go", "test", "./internal/runqueue"}
	if got := Classify(argv); got != KindFocused {
		t.Fatalf("Classify(%v) with no GOFLAGS = %v, want KindFocused", argv, got)
	}
	t.Setenv("GOFLAGS", "-race")
	if got := Classify(argv); got != KindRaceOrCover {
		t.Fatalf("Classify(%v) with GOFLAGS=-race = %v, want KindRaceOrCover", argv, got)
	}
	t.Setenv("GOFLAGS", "-count=1 -cover")
	if got := Classify(argv); got != KindRaceOrCover {
		t.Fatalf("Classify(%v) with GOFLAGS=%q = %v, want KindRaceOrCover", argv, "-count=1 -cover", got)
	}
}

// TestGoflagsRaceOrCoverUsesEffectiveGOFLAGSGoEnvFallback pins the
// re-review's Minor 3 finding: classification must catch -race/-cover
// persisted via `go env -w GOFLAGS=...` (EffectiveGOFLAGS's `go env
// GOFLAGS` fallback), not just the GOFLAGS environment variable itself.
// Points GOENV at an isolated temp file first, so this never reads or
// writes the machine's real go env config.
func TestGoflagsRaceOrCoverUsesEffectiveGOFLAGSGoEnvFallback(t *testing.T) {
	t.Setenv("GOENV", filepath.Join(t.TempDir(), "env"))
	t.Setenv("GOFLAGS", "")

	argv := []string{"go", "test", "./internal/runqueue"}
	if got := Classify(argv); got != KindFocused {
		t.Fatalf("Classify(%v) before go env -w = %v, want KindFocused", argv, got)
	}

	if output, err := exec.Command("go", "env", "-w", "GOFLAGS=-race").CombinedOutput(); err != nil {
		t.Fatalf("go env -w GOFLAGS=-race: %v: %s", err, output)
	}

	if got := Classify(argv); got != KindRaceOrCover {
		t.Fatalf("Classify(%v) after go env -w GOFLAGS=-race = %v, want KindRaceOrCover", argv, got)
	}
}

// TestClassifyDelegatesPackageManagerNxInvocations pins the review finding
// (PR #628, S3) that "<package manager> nx …" is classified the same as a
// direct nx invocation: run-many/affected are always broad, and a verb
// with no explicit project target ("pnpm nx run-many" with no -t target,
// or a bare "pnpm nx lint") is unscoped and therefore also broad.
func TestClassifyDelegatesPackageManagerNxInvocations(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want Kind
	}{
		{"pnpm nx run-many with a target is still broad", []string{"pnpm", "nx", "run-many", "-t", "test"}, KindBroad},
		{"pnpm nx run-many with no target is broad", []string{"pnpm", "nx", "run-many"}, KindBroad},
		{"npx nx affected is broad", []string{"npx", "nx", "affected", "--base=main"}, KindBroad},
		{"pnpm nx test with a project is focused", []string{"pnpm", "nx", "test", "app"}, KindFocused},
		{"pnpm nx lint with no project is broad", []string{"pnpm", "nx", "lint"}, KindBroad},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := Classify(testCase.argv); got != testCase.want {
				t.Errorf("Classify(%v) = %v, want %v", testCase.argv, got, testCase.want)
			}
		})
	}
}

// TestClassifyFixesReReviewMinor2Findings pins three specific
// misclassifications the re-review found (PR #628 re-review, Minor 2):
//   - `pnpm -w test` is broad — pnpm's bare `-w` runs the whole suite from
//     the workspace root, the opposite of narrowing scope, unlike npm's
//     `--workspace=<name>`, which does name one workspace.
//   - a flag's own value must never count as a positional file/path
//     argument: `jest -c jest.config.js`, `mocha --config .mocharc.yml`,
//     and `vitest --project core` are all still whole-suite (broad) runs;
//     a real file/path argument after a value-taking flag's value is
//     still recognized.
//   - keyword matching is on whole tokens, not substrings of a joined
//     command line: `pnpm test -- src/builder.spec.ts` is focused, not
//     broad, even though "builder.spec.ts" contains the substring "build".
func TestClassifyFixesReReviewMinor2Findings(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want Kind
	}{
		{"pnpm -w test is broad (workspace root, not one workspace)", []string{"pnpm", "-w", "test"}, KindBroad},
		{"jest -c value is not a scoping path", []string{"jest", "-c", "jest.config.js"}, KindBroad},
		{"jest -c value then a real file is still focused", []string{"jest", "-c", "jest.config.js", "src/foo.test.js"}, KindFocused},
		{"mocha --config value is not a scoping path", []string{"mocha", "--config", ".mocharc.yml"}, KindBroad},
		{"vitest --project value is not a scoping path", []string{"vitest", "--project", "core"}, KindBroad},
		{"pnpm test -- a spec file is focused despite containing \"build\"", []string{"pnpm", "test", "--", "src/builder.spec.ts"}, KindFocused},
		{"npm run build:prod is governed, not KindNone", []string{"npm", "run", "build:prod"}, KindBroad},
		{"pnpm run test:unit is governed, not KindNone", []string{"pnpm", "run", "test:unit"}, KindBroad},
		{"pnpm test:ci is governed, not KindNone", []string{"pnpm", "test:ci"}, KindBroad},
		{"a bare spec-file argument alone still does not match", []string{"pnpm", "src/builder.spec.ts"}, KindNone},
		{"npm -w foo test is focused (npm's -w names one workspace)", []string{"npm", "-w", "foo", "test"}, KindFocused},
		{"pnpm -w test is still broad (pnpm's bare -w is the workspace root)", []string{"pnpm", "-w", "test"}, KindBroad},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := Classify(testCase.argv); got != testCase.want {
				t.Errorf("Classify(%v) = %v, want %v", testCase.argv, got, testCase.want)
			}
		})
	}
}

// TestRegisterForAdmissionMakesSmallMachineFocusedJobsVisible pins the
// re-review's Minor 1 finding: RegisterForAdmission returned nil for
// KindFocused unconditionally, even on a small machine, where a focused
// job goes through the same legacy Acquire/Register pool as any other
// small-machine job (see Admit's smallMachine()-first ordering). That made
// a small-machine focused job invisible to `wb run --queue` and to other
// waiters' reported positions — a regression from origin/main. On a large
// machine, a focused job is genuinely admitted instantly and outside any
// queue, so nil there remains correct.
func TestRegisterForAdmissionMakesSmallMachineFocusedJobsVisible(t *testing.T) {
	focusedArgv := []string{"go", "vet", "./internal/runqueue"}

	t.Run("small machine registers a visible ticket", func(t *testing.T) {
		defer SetNumCPUForTest(4)()
		root := t.TempDir()
		self := Participant{PID: os.Getpid(), Summary: "focused"}
		ticket := RegisterForAdmission(root, focusedArgv, self)
		defer ticket.Forget()
		if ticket == nil {
			t.Fatal("RegisterForAdmission(focused) on a small machine = nil, want a registered ticket")
		}
		if total := Peek(root, Budget()).Total; total != 1 {
			t.Fatalf("Peek total after registering a small-machine focused job = %d, want 1", total)
		}
	})

	t.Run("large machine stays nil (genuinely instant, never queued)", func(t *testing.T) {
		defer SetNumCPUForTest(18)()
		root := t.TempDir()
		self := Participant{PID: os.Getpid(), Summary: "focused"}
		ticket := RegisterForAdmission(root, focusedArgv, self)
		defer ticket.Forget()
		if ticket != nil {
			t.Fatal("RegisterForAdmission(focused) on a large machine = non-nil, want nil")
		}
	})
}

// TestSmallMachineFocusedJobWaitsForRoom pins the S2 fix directly through
// Admit: on a small machine (numCPU < 8) a focused job is not exempt from
// budget admission the way it is on a large machine (see
// TestFocusedJobsNeverWaitBehindHeavyOnes) — it goes through the same
// budget-sum Acquire pool as every other small-machine job and waits its
// turn when the whole budget is already held. Review finding (PR #628,
// M8).
func TestSmallMachineFocusedJobWaitsForRoom(t *testing.T) {
	defer SetNumCPUForTest(4)()
	root := t.TempDir()
	budget := Budget()
	if budget != 3 {
		t.Fatalf("Budget() at NumCPU=4 = %d, want 3", budget)
	}

	holder, _, err := Acquire(context.Background(), root, budget, budget)
	if err != nil {
		t.Fatalf("Acquire(budget) = %v", err)
	}

	focusedArgv := []string{"go", "test", "./internal/runqueue"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan error, 1)
	// goroutineDone closes only after the goroutine's own deferred Release
	// has fully returned (LIFO: Release, registered second, runs before
	// close(goroutineDone), registered first) — not merely after resultCh
	// is sent, which happens earlier in the same goroutine. Without this,
	// the test could return (and go test could start the next test) while
	// Release's internal heartbeat-goroutine shutdown was still in
	// flight, an unsynchronized leak into whatever test ran next.
	goroutineDone := make(chan struct{})
	go func() {
		defer close(goroutineDone)
		self := Participant{PID: os.Getpid(), Summary: "focused-on-small-machine"}
		admission, err := Admit(ctx, root, focusedArgv, self, nil)
		if err == nil {
			defer admission.Lease.Release()
		}
		resultCh <- err
	}()
	defer func() { <-goroutineDone }()

	select {
	case err := <-resultCh:
		t.Fatalf("Admit(focused) on a saturated small machine returned early (err=%v), want it still waiting", err)
	case <-time.After(200 * time.Millisecond):
	}

	holder.Release()
	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("Admit(focused) after the budget freed up = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Admit(focused) did not return within 2s of the budget freeing up")
	}
}

func TestAcquireCoordinatesIndependentCallers(t *testing.T) {
	root := t.TempDir()
	first, _, err := Acquire(context.Background(), root, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, _, err := Acquire(ctx, root, 2, 3); err == nil {
		t.Fatal("a second two-unit command exceeded the three-unit budget")
	}
	first.Release()
	second, _, err := Acquire(context.Background(), root, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
}

// TestQueueRootOverrideIsolatesAdmissionFromAnOuterHolderOfTheSameProjectsRoot
// pins the sneat-dev/wb#623 deadlock this seam exists to fix:
// TestRunCommandAdmitsCPUHeavyWorkBelowFloor (and any test invoking `wb run
// --` in-process) used to join the very same machine-wide CPU admission
// queue an outer `wb run -- go test ./cmd/wb/...` already held every slot
// in, for the very same projectsRoot, and would wait behind it forever.
//
// This test reproduces that shape directly against this package's own API
// (cmd/wb's regression test, hostload_admission_test.go, exercises the same
// fix at the `wb run --` level): an "outer holder" takes every budget slot
// under a shared projectsRoot, using the real, unoverridden formula,
// exactly as it would before any override existed. Only then is the
// override installed — keyed to that identical projectsRoot, exactly as
// TestMain keys it to whatever `defaultProjectsRoot()` resolves to at
// binary start — and an "inner" Admit call against that same projectsRoot
// is admitted immediately despite the outer holder never releasing,
// because it resolves to a different, isolated directory instead.
func TestQueueRootOverrideIsolatesAdmissionFromAnOuterHolderOfTheSameProjectsRoot(t *testing.T) {
	defer SetNumCPUForTest(4)()
	sharedProjectsRoot := t.TempDir()

	budget := Budget()
	if budget < 1 {
		t.Fatalf("Budget() at NumCPU=4 = %d, want at least 1", budget)
	}
	outer, _, err := Acquire(context.Background(), sharedProjectsRoot, budget, budget)
	if err != nil {
		t.Fatalf("simulate outer holder: %v", err)
	}
	defer outer.Release()

	restore := SetQueueRootForTest(sharedProjectsRoot, t.TempDir())
	defer restore()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	self := Participant{PID: os.Getpid(), Summary: "inner-under-test"}
	admission, err := Admit(ctx, sharedProjectsRoot, []string{"go", "test", "./..."}, self, nil)
	if err != nil {
		t.Fatalf("Admit with an isolated queue root still blocked behind the outer holder of the same projectsRoot (deadlock regressed): %v", err)
	}
	defer admission.Lease.Release()
}

// TestQueueRootOverrideOnlyRedirectsTheKeyedProjectsRoot pins review
// finding B1 on sneat-dev/wb#736: an unkeyed override redirected every
// projectsRoot, silently defeating any existing test that pre-holds slots
// under its own explicit root and expects `wb run --projects-root
// <that root>` to contend for the same, real, unoverridden directory
// (e.g. cmd/wb's TestRunCommandReportsQueueVisibilityOnStderr). The
// override must redirect only the exact projectsRoot it was keyed to —
// never any other, even one from the very same test — and restoring must
// return every root, keyed or not, to its own real, unoverridden path.
func TestQueueRootOverrideOnlyRedirectsTheKeyedProjectsRoot(t *testing.T) {
	keyedRoot := t.TempDir()
	otherRoot := t.TempDir()
	overrideDir := t.TempDir()
	restore := SetQueueRootForTest(keyedRoot, overrideDir)
	t.Cleanup(restore)

	if got := queueRoot(keyedRoot); got != overrideDir {
		t.Fatalf("queueRoot(keyedRoot) = %q, want the override %q", got, overrideDir)
	}
	wantOther := filepath.Join(otherRoot, ".wb", "runtime", "cpu")
	if got := queueRoot(otherRoot); got != wantOther {
		t.Fatalf("queueRoot(otherRoot) = %q, want its own unoverridden path %q (a test-owned root must never be redirected)", got, wantOther)
	}

	restore()
	wantKeyed := filepath.Join(keyedRoot, ".wb", "runtime", "cpu")
	if got := queueRoot(keyedRoot); got != wantKeyed {
		t.Fatalf("queueRoot(keyedRoot) after restore = %q, want %q", got, wantKeyed)
	}
}

// withTinyLeaseHeartbeatInterval shrinks leaseHeartbeatInterval for the
// duration of a test, so armHeartbeat's ticker fires essentially
// immediately and reliably races a Release called right after admission,
// instead of depending on 10s timing luck.
func withTinyLeaseHeartbeatInterval(t *testing.T) {
	t.Helper()
	original := leaseHeartbeatInterval
	leaseHeartbeatInterval = time.Microsecond
	t.Cleanup(func() { leaseHeartbeatInterval = original })
}

// TestLeaseReleaseWaitsForInFlightHeartbeatOnLegacyPool pins the re-review's
// Serious 1 finding on the legacy (small-machine) pool: Release used to
// close stopHeartbeat and immediately run extraRelease (Announcement.
// Cleanup, which nils paths) without waiting for the self-heartbeat
// goroutine to actually stop, so a heartbeat already in flight
// (Announcement.Heartbeat, which reads paths) raced Cleanup's write —  a
// genuine data race caught under -race before the heartbeatDone wait was
// added. Loops with a near-zero heartbeat interval so the race reproduces
// reliably rather than depending on timing luck.
func TestLeaseReleaseWaitsForInFlightHeartbeatOnLegacyPool(t *testing.T) {
	defer SetNumCPUForTest(4)()
	withTinyLeaseHeartbeatInterval(t)
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		self := Participant{PID: os.Getpid(), Summary: "race"}
		admission, err := Admit(context.Background(), root, broadArgv, self, nil)
		if err != nil {
			t.Fatalf("iteration %d: Admit = %v", i, err)
		}
		admission.Lease.Release()
	}
}

// TestLeaseReleaseWaitsForInFlightHeartbeatOnHeavyPool is the heavy-pool
// counterpart: before the heartbeatDone wait, a heartbeat write already in
// flight when Release called removeHeavyHolder could rename its holder
// file back into existence just after the delete, resurrecting a "ghost"
// holder — reproduced in the review 190 of 200 runs. Also asserts the
// holder file is really gone after every Release, not just that Admit/
// Release themselves didn't error.
func TestLeaseReleaseWaitsForInFlightHeartbeatOnHeavyPool(t *testing.T) {
	defer SetNumCPUForTest(18)()
	withTinyLeaseHeartbeatInterval(t)
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		self := heavyTestParticipant("race")
		ticket := RegisterForAdmission(root, broadArgv, self)
		admission, err := Admit(context.Background(), root, broadArgv, self, ticket)
		ticket.Forget()
		if err != nil {
			t.Fatalf("iteration %d: Admit = %v", i, err)
		}
		admission.Lease.Release()
		if holders := readHeavyHolders(root); len(holders) != 0 {
			t.Fatalf("iteration %d: holder file still present after Release: %v", i, holders)
		}
	}
}
