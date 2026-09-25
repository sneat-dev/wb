package lifecyclehooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestWriteJSONAtomicInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepRename,
		filewrite.StepDirSync,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "value.json")
			inj := &filewrite.Injector{Step: step, Err: errBoomPR8}
			err := writeJSONAtomicInjected(path, map[string]int{"a": 1}, 0o644, inj)
			if !errors.Is(err, errBoomPR8) {
				t.Fatalf("writeJSONAtomicInjected(%s failure) = %v, want errBoomPR8", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(dir, ".tmp-*"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if step != filewrite.StepDirSync && len(matches) != 0 {
				t.Fatalf("leftover temp file(s) after injected failure: %v", matches)
			}
			if step == filewrite.StepDirSync {
				if _, statErr := os.Stat(path); statErr != nil {
					t.Fatalf("dir-sync failure unexpectedly lost the published file: %v", statErr)
				}
				return
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("failed write published a visible value: %v", statErr)
			}
		})
	}
}

func TestWriteJSONAtomicInjectedPublishesAtRequestedMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "value.json")
	if err := writeJSONAtomicInjected(path, map[string]int{"a": 1}, 0o640, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("published value mode = %o, want 0640", perm)
	}
}

func TestQuarantineFileInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepRename, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepDirSync,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			stateDir := t.TempDir()
			dispatcher := Dispatcher{StateDir: stateDir, Now: time.Now}
			pendingDir := dispatcher.pendingDir()
			if err := os.MkdirAll(pendingDir, 0o700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(pendingDir, "job.json")
			if err := os.WriteFile(source, []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR8}
			destination, err := dispatcher.quarantineFileInjected(source, "boom reason", inj)
			if !errors.Is(err, errBoomPR8) {
				t.Fatalf("quarantineFileInjected(%s failure) = (%q, %v), want errBoomPR8", step, destination, err)
			}
			if step == filewrite.StepRename {
				if destination != "" {
					t.Fatalf("rename failure returned a non-empty destination: %q", destination)
				}
				if _, statErr := os.Stat(source); statErr != nil {
					t.Fatalf("rename failure unexpectedly moved the source file: %v", statErr)
				}
			}
			if step == filewrite.StepDirSync && destination == "" {
				t.Fatal("dir-sync failure must still report the destination that was already quarantined")
			}
		})
	}
}

func TestQuarantineFileInjectedMovesChmodsAndRecordsReason(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	dispatcher := Dispatcher{StateDir: stateDir, Now: time.Now}
	pendingDir := dispatcher.pendingDir()
	if err := os.MkdirAll(pendingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(pendingDir, "job.json")
	if err := os.WriteFile(source, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination, err := dispatcher.quarantineFileInjected(source, "boom reason", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(source); !os.IsNotExist(statErr) {
		t.Fatalf("source still present after quarantine: %v", statErr)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("quarantined file mode = %o, want 0600", perm)
	}
	reason, err := os.ReadFile(destination + ".reason.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(reason) != "boom reason\n" {
		t.Fatalf("quarantine reason = %q, want %q", reason, "boom reason\n")
	}
}
