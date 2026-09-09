package runqueue

import (
	"context"
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
