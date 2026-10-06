package qualityrun

import (
	"errors"
	"github.com/sneat-dev/wb/internal/filewrite"
	"os"
	"path/filepath"
	"testing"
)

var errBoomPR9 = errors.New("pr9 boom")

func TestChangedCoverageProfilePathInjectedKeepsAnExplicitProfile(t *testing.T) {
	t.Parallel()
	explicit := filepath.Join(t.TempDir(), "explicit.out")
	path, removeProfile, err := changedCoverageProfilePathInjected(explicit, nil)
	if err != nil || path != explicit || removeProfile {
		t.Fatalf("changedCoverageProfilePathInjected(explicit) = (%q, %v, %v), want (%q, false, nil)", path, removeProfile, err, explicit)
	}
}
func TestChangedCoverageProfilePathInjectedReservesAScratchPath(t *testing.T) {
	t.Parallel()
	path, removeProfile, err := changedCoverageProfilePathInjected("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !removeProfile {
		t.Fatal("removeProfile = false, want true for a reserved scratch path")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("reserved scratch path missing: %v", statErr)
	}
	_ = os.Remove(path)
}
func TestChangedCoverageProfilePathInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR9}
	path, removeProfile, err := changedCoverageProfilePathInjected("", inj)
	if path != "" || removeProfile || !errors.Is(err, errBoomPR9) {
		t.Fatalf("changedCoverageProfilePathInjected with injected create failure = (%q, %v, %v), want (\"\", false, errBoomPR9)", path, removeProfile, err)
	}
}
func TestChangedCoverageProfilePathInjectedIgnoresAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	// runChangedCoverage's original inline sequence discarded Close's error
	// entirely (`_ = file.Close()`): the reserved path is about to be
	// overwritten wholesale by `go test -coverprofile` regardless.
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR9}
	path, removeProfile, err := changedCoverageProfilePathInjected("", inj)
	if path == "" || !removeProfile || err != nil {
		t.Fatalf("changedCoverageProfilePathInjected with injected close failure = (%q, %v, %v), want (non-empty, true, nil)", path, removeProfile, err)
	}
	_ = os.Remove(path)
}
