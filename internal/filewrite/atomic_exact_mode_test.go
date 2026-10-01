package filewrite

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

//nolint:paralleltest // umask is process-wide; restore it before other tests run.
func TestWriteBytesAtomicAtExactModeRetainsPermissionsUnderUmask(t *testing.T) {
	directory := openTestDir(t)
	target := filepath.Join(directory.Name(), "existing")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)
	if err := WriteBytesAtomicAtExactMode(directory, "existing", []byte("new"), 0o640); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "new" {
		t.Fatalf("exact-mode target = %q, %v", got, err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("exact-mode target permission = %v, %v", info, err)
	}
}

func TestWriteBytesAtomicAtExactModeReportsInvalidInputAndEveryStep(t *testing.T) {
	t.Parallel()
	if err := WriteBytesAtomicAtExactMode(nil, "file", nil, 0o600); err == nil {
		t.Fatal("nil held directory accepted")
	}
	directory := openTestDir(t)
	if err := WriteBytesAtomicAtExactMode(directory, "../escape", nil, 0o600); err == nil {
		t.Fatal("unsafe filename accepted")
	}
	for _, step := range []Step{StepOpenOrCreate, StepChmod, StepWrite, StepSync, StepClose, StepRename, StepDirSync} {
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			held := openTestDir(t)
			err := WriteBytesAtomicAtExactModeInjected(held, "file", []byte("private"), 0o640, &Injector{Step: step, Err: errBoom})
			if !errors.Is(err, errBoom) {
				t.Fatalf("exact-mode %s error = %v", step, err)
			}
			if step == StepDirSync {
				if _, err := os.Stat(filepath.Join(held.Name(), "file")); err != nil {
					t.Fatalf("published target missing after sync error: %v", err)
				}
			} else {
				assertNoAtomicTemps(t, held.Name(), ".file.tmp-*")
			}
		})
	}
}
