package quality

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR9 is task-9 PR-9's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR9 = errors.New("pr9 boom")

func TestCoverageProfilePathInjectedReservesAScratchPath(t *testing.T) {
	t.Parallel()
	path, remove, err := coverageProfilePathInjected("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !remove {
		t.Fatal("remove = false, want true for a reserved scratch path")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("reserved scratch path missing: %v", statErr)
	}
}

func TestCoverageProfilePathInjectedKeepsAnExplicitRetainPath(t *testing.T) {
	t.Parallel()
	retain := filepath.Join(t.TempDir(), "explicit.out")
	path, remove, err := coverageProfilePathInjected(retain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if remove {
		t.Fatal("remove = true, want false for an explicit --coverage-profile")
	}
	if filepath.Base(path) != "explicit.out" {
		t.Fatalf("path = %q, want it to resolve the explicit retain path", path)
	}
}

func TestCoverageProfilePathInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR9}
	path, remove, err := coverageProfilePathInjected("", inj)
	if path != "" || remove || !errors.Is(err, errBoomPR9) {
		t.Fatalf("coverageProfilePathInjected with injected create failure = (%q, %v, %v), want (\"\", false, errBoomPR9)", path, remove, err)
	}
}

func TestCoverageProfilePathInjectedRemovesTheReservationOnAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR9}
	path, remove, err := coverageProfilePathInjected("", inj)
	if path != "" || remove || !errors.Is(err, errBoomPR9) {
		t.Fatalf("coverageProfilePathInjected with injected close failure = (%q, %v, %v), want (\"\", false, errBoomPR9)", path, remove, err)
	}
}

func TestRunShardedVerificationInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR9}
	path, attempts, err := runShardedVerificationInjected(t.Context(), RunOptions{}, ".", inj)
	if path != "" || attempts != 0 || !errors.Is(err, errBoomPR9) {
		t.Fatalf("runShardedVerificationInjected with injected create failure = (%q, %d, %v), want (\"\", 0, errBoomPR9)", path, attempts, err)
	}
}

func TestRunShardedVerificationInjectedRemovesTheReservationOnAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR9}
	path, attempts, err := runShardedVerificationInjected(t.Context(), RunOptions{}, ".", inj)
	if path != "" || attempts != 0 || !errors.Is(err, errBoomPR9) {
		t.Fatalf("runShardedVerificationInjected with injected close failure = (%q, %d, %v), want (\"\", 0, errBoomPR9)", path, attempts, err)
	}
}
