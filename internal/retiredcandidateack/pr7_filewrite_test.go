package retiredcandidateack

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR7 is task-9 PR-7's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR7 = errors.New("pr7 boom")

func TestPersistInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepSync,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "receipt"+FileSuffix)
			inj := &filewrite.Injector{Step: step, Err: errBoomPR7}
			err := persistInjected(path, Acknowledgement{}, inj)
			if !errors.Is(err, errBoomPR7) {
				t.Fatalf("persistInjected(%s failure) = %v, want errBoomPR7", step, err)
			}
			if step == filewrite.StepOpenOrCreate {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("failed create left a visible file: %v", statErr)
				}
			}
		})
	}
}

// TestPersistInjectedClosesOnEveryPath covers the deferred Close call: it
// always runs, on both the success and the failure paths, and its own
// injected failure never masks whatever error the function already had (the
// original code discarded the deferred Close's error unconditionally on
// every path, and this migration keeps that).
func TestPersistInjectedClosesOnEveryPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "receipt"+FileSuffix)
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR7}
	if err := persistInjected(path, Acknowledgement{}, inj); err != nil {
		t.Fatalf("persistInjected(close failure) = %v, want nil (the deferred Close's error is discarded)", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persistInjected did not publish despite a discarded close failure: %v", err)
	}
}
