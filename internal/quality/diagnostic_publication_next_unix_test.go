//go:build !windows

package quality

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestCoverageDiagnosticPublicationFailureCleanup(t *testing.T) {
	t.Parallel()
	for _, phase := range []filewrite.Step{filewrite.StepChmod, filewrite.StepWrite, filewrite.StepShortWrite, filewrite.StepSync, filewrite.StepClose, filewrite.StepRename, filewrite.StepDirSync} {
		t.Run(string(phase), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "report.log")
			failure := errors.New("publication failed")
			inj := &filewrite.Injector{Step: phase, Err: failure, ShortBytes: 1}
			var held *os.File
			err := writeCoverageDiagnosticFileWithIO(path, []byte("complete report"), inj, func(path string) (*os.File, error) { file, err := os.Open(path); held = file; return file, err })
			if phase == filewrite.StepShortWrite {
				if err == nil || !strings.Contains(err.Error(), "wrote 1 of 15 bytes") {
					t.Fatalf("native short-write failure = %v", err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("failure = %v", err)
			}
			raw, readErr := os.ReadFile(path)
			if phase == filewrite.StepDirSync {
				if readErr != nil || string(raw) != "complete report" {
					t.Fatalf("published evidence lost: %q %v", raw, readErr)
				}
				if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("directory leaked: %v", err)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("premature publication: %q %v", raw, readErr)
			}
			leftover, err := filepath.Glob(filepath.Join(root, ".coverage-diagnostic-*.tmp"))
			if err != nil || len(leftover) != 0 {
				t.Fatalf("leftovers=%v err=%v", leftover, err)
			}
		})
	}
}

func TestCoverageDiagnosticNativeDirectoryObservationFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"open", "sync and close"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "report.log")
			published := path
			var held *os.File
			err := writeCoverageDiagnosticFileWithIO(path, []byte("published"), nil, func(directory string) (*os.File, error) {
				if phase == "open" {
					retained := root + "-retained"
					if err := os.Rename(root, retained); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(retained) })
					published = filepath.Join(retained, "report.log")
					return os.Open(directory)
				}
				file, err := os.Open(directory)
				if err != nil {
					return nil, err
				}
				held = file
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				return file, nil
			})
			if phase == "open" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("native open = %v", err)
				}
			} else {
				if !errors.Is(err, os.ErrClosed) {
					t.Fatalf("native sync/close = %v", err)
				}
				if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("held file = %v", err)
				}
			}
			raw, readErr := os.ReadFile(published)
			if readErr != nil || string(raw) != "published" {
				t.Fatalf("published evidence changed: %q %v", raw, readErr)
			}
		})
	}
}

func TestCoverageDiagnosticPublicationModeAndBytes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "report.log")
	if err := writeCoverageDiagnosticFileAtomically(path, []byte("complete")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "complete" {
		t.Fatalf("bytes=%q err=%v", raw, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v err=%v", info, err)
	}
}
