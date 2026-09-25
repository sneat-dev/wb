package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR3 is task-9 PR-3's sentinel injected failure, distinct from any
// other package's sentinel so errors.Is never accidentally matches a
// different test's error by coincidence.
var errBoomPR3 = errors.New("pr3 boom")

// The following tests exercise writeBytesImmutableAtInjected's,
// writeBytesAtomicAtInjected's and writeBytesAtomicInjected's
// filewrite.Injector-reachable error branches (task-9 PR-3): their happy
// paths are already covered by TestWtLogCovAtomicReadWriteHelpers, but
// reaching a create, write, sync, close, rename(-no-replace), chmod or
// dir-sync failure deterministically needs the injector.

func openPR3TestDir(t *testing.T) *os.File {
	t.Helper()
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return directory
}

func TestWriteBytesImmutableAtInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR3}
	if err := writeBytesImmutableAtInjected(directory, "f", []byte("x"), 0o600, false, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesImmutableAtInjected error = %v", err)
	}
}

func TestWriteBytesImmutableAtInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR3}
	if err := writeBytesImmutableAtInjected(directory, "f", []byte("x"), 0o600, false, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesImmutableAtInjected error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory.Name(), "f")); !os.IsNotExist(err) {
		t.Fatalf("target file exists despite the injected write failure: %v", err)
	}
}

func TestWriteBytesImmutableAtInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errBoomPR3}
	if err := writeBytesImmutableAtInjected(directory, "f", []byte("x"), 0o600, false, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesImmutableAtInjected error = %v", err)
	}
}

func TestWriteBytesImmutableAtInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR3}
	if err := writeBytesImmutableAtInjected(directory, "f", []byte("x"), 0o600, false, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesImmutableAtInjected error = %v", err)
	}
}

func TestWriteBytesImmutableAtInjectedHonoursAnInjectedRenameNoReplaceFailureNonIdempotent(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepRenameNoReplace, Err: errBoomPR3}
	if err := writeBytesImmutableAtInjected(directory, "f", []byte("x"), 0o600, false, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesImmutableAtInjected error = %v, want errBoomPR3", err)
	}
}

func TestWriteBytesImmutableAtInjectedRenameNoReplaceFailureSurvivesIdenticalIdempotentContent(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	if err := writeBytesImmutableAtInjected(directory, "f", []byte("x"), 0o600, true, nil); err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepRenameNoReplace, Err: errBoomPR3}
	if err := writeBytesImmutableAtInjected(directory, "f", []byte("x"), 0o600, true, inj); err != nil {
		t.Fatalf("idempotent rewrite with an injected rename failure = %v, want nil (existing content already matches)", err)
	}
}

func TestWriteBytesImmutableAtInjectedHonoursAnInjectedDirSyncFailure(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepDirSync, Err: errBoomPR3}
	if err := writeBytesImmutableAtInjected(directory, "f", []byte("x"), 0o600, false, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesImmutableAtInjected error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory.Name(), "f")); err != nil {
		t.Fatalf("target file was not published before the injected dir-sync failure: %v", err)
	}
}

func TestWriteBytesAtomicAtInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR3}
	if err := writeBytesAtomicAtInjected(directory, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicAtInjected error = %v", err)
	}
}

func TestWriteBytesAtomicAtInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR3}
	if err := writeBytesAtomicAtInjected(directory, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicAtInjected error = %v", err)
	}
}

func TestWriteBytesAtomicAtInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errBoomPR3}
	if err := writeBytesAtomicAtInjected(directory, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicAtInjected error = %v", err)
	}
}

func TestWriteBytesAtomicAtInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR3}
	if err := writeBytesAtomicAtInjected(directory, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicAtInjected error = %v", err)
	}
}

func TestWriteBytesAtomicAtInjectedHonoursAnInjectedRenameFailure(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomPR3}
	if err := writeBytesAtomicAtInjected(directory, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicAtInjected error = %v", err)
	}
}

func TestWriteBytesAtomicAtInjectedHonoursAnInjectedDirSyncFailure(t *testing.T) {
	t.Parallel()
	directory := openPR3TestDir(t)
	inj := &filewrite.Injector{Step: filewrite.StepDirSync, Err: errBoomPR3}
	if err := writeBytesAtomicAtInjected(directory, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicAtInjected error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory.Name(), "f")); err != nil {
		t.Fatalf("target file was not published before the injected dir-sync failure: %v", err)
	}
}

func TestWriteBytesAtomicInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR3}
	if err := writeBytesAtomicInjected(dir, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicInjected error = %v", err)
	}
}

func TestWriteBytesAtomicInjectedHonoursAnInjectedChmodFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Err: errBoomPR3}
	if err := writeBytesAtomicInjected(dir, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicInjected error = %v", err)
	}
}

func TestWriteBytesAtomicInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomPR3}
	if err := writeBytesAtomicInjected(dir, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicInjected error = %v", err)
	}
}

func TestWriteBytesAtomicInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errBoomPR3}
	if err := writeBytesAtomicInjected(dir, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicInjected error = %v", err)
	}
}

func TestWriteBytesAtomicInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR3}
	if err := writeBytesAtomicInjected(dir, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicInjected error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".f.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temp file(s) after close failure: %v", matches)
	}
}

func TestWriteBytesAtomicInjectedHonoursAnInjectedRenameFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomPR3}
	if err := writeBytesAtomicInjected(dir, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicInjected error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".f.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temp file(s) after rename failure: %v", matches)
	}
}

func TestWriteBytesAtomicInjectedHonoursAnInjectedDirSyncFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepDirSync, Err: errBoomPR3}
	if err := writeBytesAtomicInjected(dir, "f", []byte("x"), 0o600, inj); !errors.Is(err, errBoomPR3) {
		t.Fatalf("writeBytesAtomicInjected error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Fatalf("target file was not published before the injected dir-sync failure: %v", err)
	}
}
