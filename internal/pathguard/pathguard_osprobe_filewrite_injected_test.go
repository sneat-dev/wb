package pathguard

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR9 is task-9 PR-9's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR9 = errors.New("pr9 boom")

func TestOSProbeInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR9}
	if err := osProbeInjected(dir, inj); !errors.Is(err, errBoomPR9) {
		t.Fatalf("osProbeInjected with injected create failure = %v, want errBoomPR9", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, ".wb-writable-probe-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover probe entry after an injected create failure: %v", matches)
	}
}

func TestOSProbeInjectedRemovesTheProbeOnAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR9}
	if err := osProbeInjected(dir, inj); !errors.Is(err, errBoomPR9) {
		t.Fatalf("osProbeInjected with injected close failure = %v, want errBoomPR9", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, ".wb-writable-probe-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover probe entry after an injected close failure: %v", matches)
	}
}

func TestOSProbeInjectedSucceedsAndLeavesNothingBehind(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := osProbeInjected(dir, nil); err != nil {
		t.Fatal(err)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, ".wb-writable-probe-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover probe entry after a successful probe: %v", matches)
	}
}
