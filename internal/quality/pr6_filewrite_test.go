package quality

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR6 is task-9 PR-6's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR6 = errors.New("pr6 boom")

// The following tests exercise the filewrite.Injector-reachable error
// branches of task-9 PR-6's two internal/quality call sites, plus a
// leftover-temp-file check on each (review-756 B1 lesson) and a
// published-mode check on writeCoverageProfileAtomically (review-756 B2
// lesson). Both lessons, plus review-767's B1 (byte-identical error text),
// B2/B3 (cover every changed statement, including restore and error-return
// branches), and N5 (umask-independent mode checks) are applied up front
// rather than retrofitted after review.

// --- writeCoverageProfileAtomicallyInjected ---

func TestWriteCoverageProfileAtomicallyInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepSync,
		filewrite.StepClose, filewrite.StepRename,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			output := filepath.Join(dir, "coverage.out")
			inj := &filewrite.Injector{Step: step, Err: errBoomPR6}
			blocks := map[string]coverageBlock{"pkg/file.go:1.1,2.2": {location: "pkg/file.go:1.1,2.2", statements: 1, count: 1}}
			err := writeCoverageProfileAtomicallyInjected(output, "atomic", blocks, inj)
			if !errors.Is(err, errBoomPR6) {
				t.Fatalf("writeCoverageProfileAtomicallyInjected(%s failure) = %v, want errBoomPR6", step, err)
			}
			if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
				t.Fatalf("failed merge published a visible coverage profile: %v", statErr)
			}
			assertNoLeftoverPR6TempFile(t, dir, ".wb-coverage-merge-*.tmp")
		})
	}
}

// TestWriteCoverageProfileAtomicallyInjectedPublishesAt0644 covers
// review-756's B2 lesson: unlike a filewrite.CreateExclusive-based call
// site (which creates the temp file already at its final mode, masking a
// deleted chmod call by coincidence), filewrite.CreateTemp is path-based
// and always creates at os.CreateTemp's own default 0600 regardless of
// what mode a caller later asks for -- so a deleted ChmodFile call here is
// directly, sufficiently detectable without review-763's chmod-preset Hook
// trick (that trick exists only to defeat a create call's own coincidental
// final mode).
func TestWriteCoverageProfileAtomicallyInjectedPublishesAt0644(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	output := filepath.Join(dir, "coverage.out")
	blocks := map[string]coverageBlock{"pkg/file.go:1.1,2.2": {location: "pkg/file.go:1.1,2.2", statements: 1, count: 1}}
	if err := writeCoverageProfileAtomicallyInjected(output, "atomic", blocks, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("published coverage profile mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestWriteCoverageProfileAtomicallyInjectedRoundTripsMergedBlocks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	output := filepath.Join(dir, "coverage.out")
	blocks := map[string]coverageBlock{
		"pkg/b.go:2.1,3.1": {location: "pkg/b.go:2.1,3.1", statements: 1, count: 2},
		"pkg/a.go:1.1,1.9": {location: "pkg/a.go:1.1,1.9", statements: 1, count: 0},
	}
	if err := writeCoverageProfileAtomicallyInjected(output, "atomic", blocks, nil); err != nil {
		t.Fatal(err)
	}
	mode, got, err := readCoverageProfile(output)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "atomic" || len(got) != 2 || got[0].location != "pkg/a.go:1.1,1.9" || got[1].location != "pkg/b.go:2.1,3.1" {
		t.Fatalf("readCoverageProfile(%s) = %q, %+v, want atomic mode with a.go sorted before b.go", output, mode, got)
	}
	assertNoLeftoverPR6TempFile(t, dir, ".wb-coverage-merge-*.tmp")
}

// --- saveValidationCacheInjected ---

func TestSaveValidationCacheInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepSync,
		filewrite.StepClose, filewrite.StepRename,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			key := ValidationCacheKey{Repository: "acme/widget", TargetRevision: "0123456789012345678901234567890123456789"}
			report := VerificationReport{Repository: key.Repository, Revision: key.TargetRevision, WorkspaceClean: true, Status: StatusPassed}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR6}
			err := saveValidationCacheInjected(dir, key, report, inj)
			if !errors.Is(err, errBoomPR6) {
				t.Fatalf("saveValidationCacheInjected(%s failure) = %v, want errBoomPR6", step, err)
			}
			digest, digestErr := validationCacheKeyDigest(key)
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			if _, statErr := os.Stat(filepath.Join(dir, digest+".json")); !os.IsNotExist(statErr) {
				t.Fatalf("failed save published a visible cache entry: %v", statErr)
			}
			assertNoLeftoverPR6TempFile(t, dir, ".validation-*.tmp")
		})
	}
}

func TestSaveValidationCacheInjectedRoundTripsThroughLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := ValidationCacheKey{Repository: "acme/widget", TargetRevision: "0123456789012345678901234567890123456789"}
	report := VerificationReport{Repository: key.Repository, Revision: key.TargetRevision, WorkspaceClean: true, Status: StatusPassed}
	if err := saveValidationCacheInjected(dir, key, report, nil); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LoadValidationCache(dir, key)
	if err != nil || !ok || got.Status != StatusPassed {
		t.Fatalf("LoadValidationCache after saveValidationCacheInjected = %+v, hit=%v, err=%v", got, ok, err)
	}
	assertNoLeftoverPR6TempFile(t, dir, ".validation-*.tmp")
}

func assertNoLeftoverPR6TempFile(t *testing.T, dir, pattern string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temp file(s) after a failed publish: %v", matches)
	}
}
