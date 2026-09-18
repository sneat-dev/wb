package runqueue

import (
	"context"
	"os"
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
	go func() {
		self := Participant{PID: os.Getpid(), Summary: "focused-on-small-machine"}
		admission, err := Admit(ctx, root, focusedArgv, self, nil)
		if err == nil {
			defer admission.Lease.Release()
		}
		resultCh <- err
	}()

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
