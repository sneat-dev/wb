package defaultbranch

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

var errBoomPR9 = errors.New("pr9 boom")

func TestDefaultBranchReportPathInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR9}
	path, err := defaultBranchReportPathInjected(Scope{}, t.TempDir(), inj)
	if path != "" || !errors.Is(err, errBoomPR9) {
		t.Fatalf("defaultBranchReportPathInjected with injected create failure = (%q, %v), want (\"\", errBoomPR9)", path, err)
	}
}
func TestDefaultBranchReportPathInjectedLeavesTheReservationOnAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR9}
	path, err := defaultBranchReportPathInjected(Scope{}, dir, inj)
	if path != "" || !errors.Is(err, errBoomPR9) {
		t.Fatalf("defaultBranchReportPathInjected with injected close failure = (%q, %v), want (\"\", errBoomPR9)", path, err)
	}
	// The original inline sequence never removed the reservation on a Close
	// failure (only the later, deliberate os.Remove(path) freed the name on
	// the success path); migrating to filewrite.CreateScratch preserves that
	// exact on-disk-failure behaviour rather than tightening it.
	matches, globErr := filepath.Glob(filepath.Join(dir, "default-branch-*.json"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 1 {
		t.Fatalf("reservation(s) after an injected close failure = %v, want exactly one left in place (original behaviour)", matches)
	}
}
