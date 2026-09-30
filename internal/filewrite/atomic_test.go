package filewrite

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicCodecsReadWriteAndRejectInvalidInput(t *testing.T) {
	directory := openTestDir(t)
	payload := map[string]string{"hello": "world"}

	if err := WriteJSONAtomicAt(directory, "value.json", payload, 0o600); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := ReadJSONAt(directory, "value.json", &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["hello"] != "world" {
		t.Fatalf("decoded = %#v", decoded)
	}
	if _, err := ReadAt(directory, "missing.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing read error = %v", err)
	}
	if err := ReadJSONAt(directory, "missing.json", &decoded); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing JSON read error = %v", err)
	}
	if err := ReadJSONAt(directory, "value.json", &struct{ Missing string }{}); err != nil {
		t.Fatalf("decode into an unrelated struct: %v", err)
	}

	if err := WriteBytesImmutableAt(directory, "immutable.txt", []byte("one"), 0o600, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := WriteBytesImmutableAt(directory, "immutable.txt", []byte("one"), 0o600, true, nil); err != nil {
		t.Fatalf("idempotent rewrite rejected: %v", err)
	}
	if err := WriteBytesImmutableAt(directory, "immutable.txt", []byte("two"), 0o600, true, nil); err == nil {
		t.Fatal("conflicting immutable rewrite was accepted")
	}
	if err := WriteJSONImmutableAt(directory, "immutable.json", payload, true, nil); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"", ".", "..", "../escape"} {
		if err := WriteBytesImmutableAt(directory, name, []byte("x"), 0o600, false, nil); err == nil {
			t.Fatalf("unsafe immutable name %q was accepted", name)
		}
		if err := WriteBytesAtomicAt(directory, name, []byte("x"), 0o600); err == nil {
			t.Fatalf("unsafe atomic name %q was accepted", name)
		}
	}
	if err := WriteBytesAtomicAt(nil, "x", nil, 0o600); err == nil {
		t.Fatal("nil directory atomic write was accepted")
	}
	if err := os.Symlink(filepath.Join(directory.Name(), "value.json"), filepath.Join(directory.Name(), "immutable-link")); err != nil {
		t.Fatal(err)
	}
	if err := WriteBytesImmutableAt(directory, "immutable-link", []byte("x"), 0o600, false, nil); err == nil {
		t.Fatal("immutable write accepted a symlinked destination")
	}
	badJSON := map[string]any{"bad": func() {}}
	if err := WriteJSONImmutableAt(directory, "bad.json", badJSON, false, nil); err == nil {
		t.Fatal("unsupported immutable JSON was accepted")
	}
	if err := WriteJSONAtomicAt(directory, "bad.json", badJSON, 0o600); err == nil {
		t.Fatal("unsupported descriptor JSON was accepted")
	}

	nested := filepath.Join(t.TempDir(), "nested")
	if err := WriteJSONAtomic(filepath.Join(nested, "value.json"), payload, 0o600); err != nil {
		t.Fatalf("WriteJSONAtomic into a new directory: %v", err)
	}
	if err := WriteBytesAtomic(nested, "bytes.bin", []byte("payload"), 0o600); err != nil {
		t.Fatalf("WriteBytesAtomic: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(nested, "bytes.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "payload" {
		t.Fatalf("bytes file = %q", contents)
	}
	if err := WriteJSONAtomic(filepath.Join(nested, "bad.json"), badJSON, 0o600); err == nil || !strings.Contains(err.Error(), "encode") {
		t.Fatalf("WriteJSONAtomic unsupported JSON = %v", err)
	}

	invalid := filepath.Join(directory.Name(), "invalid.json")
	if err := os.WriteFile(invalid, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReadJSONAt(directory, "invalid.json", &decoded); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
}

func TestReadAtRefusesSymlink(t *testing.T) {
	directory := openTestDir(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory.Name(), "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAt(directory, "link"); err == nil {
		t.Fatal("symlink read was accepted")
	}
}

func TestWriteBytesImmutableAtPreservesCompetingWriterContracts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		winner     []byte
		idempotent bool
		wantErr    bool
	}{
		{name: "different content reports rename collision", winner: []byte("winner"), idempotent: true, wantErr: true},
		{name: "matching content converges", winner: []byte("shared"), idempotent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := openTestDir(t)
			content := []byte("shared")
			if tc.wantErr {
				content = []byte("loser")
			}
			var beforeRename func(*os.File, string)
			beforeRename = func(hookDirectory *os.File, name string) {
				beforeRename = nil
				if err := WriteBytesImmutableAt(hookDirectory, name, tc.winner, 0o600, false, nil); err != nil {
					t.Fatalf("competing writer: %v", err)
				}
			}
			err := WriteBytesImmutableAt(directory, "contested", content, 0o600, tc.idempotent, beforeRename)
			if (err != nil) != tc.wantErr {
				t.Fatalf("WriteBytesImmutableAt error = %v, wantErr %t", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "file exists") {
				t.Fatalf("rename collision = %v", err)
			}
			stored, readErr := os.ReadFile(filepath.Join(directory.Name(), "contested"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(stored) != string(tc.winner) {
				t.Fatalf("stored = %q, want %q", stored, tc.winner)
			}
			assertNoAtomicTemps(t, directory.Name(), ".contested.tmp-*")
		})
	}
}

func TestWriteBytesImmutableAtInjectedFailures(t *testing.T) {
	for _, step := range []Step{StepOpenOrCreate, StepWrite, StepSync, StepClose, StepRenameNoReplace, StepDirSync} {
		t.Run(string(step), func(t *testing.T) {
			directory := openTestDir(t)
			err := WriteBytesImmutableAtInjected(directory, "f", []byte("x"), 0o600, false, &Injector{Step: step, Err: errBoom}, nil)
			if !errors.Is(err, errBoom) {
				t.Fatalf("WriteBytesImmutableAtInjected error = %v", err)
			}
			if step == StepDirSync {
				if _, statErr := os.Stat(filepath.Join(directory.Name(), "f")); statErr != nil {
					t.Fatalf("published file missing after dir sync failure: %v", statErr)
				}
			} else {
				assertNoAtomicTemps(t, directory.Name(), ".f.tmp-*")
			}
		})
	}
}

func TestWriteBytesAtomicAtInjectedFailures(t *testing.T) {
	for _, step := range []Step{StepOpenOrCreate, StepWrite, StepSync, StepClose, StepRename, StepDirSync} {
		t.Run(string(step), func(t *testing.T) {
			directory := openTestDir(t)
			err := WriteBytesAtomicAtInjected(directory, "f", []byte("x"), 0o600, &Injector{Step: step, Err: errBoom})
			if !errors.Is(err, errBoom) {
				t.Fatalf("WriteBytesAtomicAtInjected error = %v", err)
			}
			if step == StepDirSync {
				if _, statErr := os.Stat(filepath.Join(directory.Name(), "f")); statErr != nil {
					t.Fatalf("published file missing after dir sync failure: %v", statErr)
				}
			} else {
				assertNoAtomicTemps(t, directory.Name(), ".f.tmp-*")
			}
		})
	}
}

func TestWriteBytesAtomicInjectedFailures(t *testing.T) {
	for _, step := range []Step{StepOpenOrCreate, StepChmod, StepWrite, StepSync, StepClose, StepRename, StepOpenDirectory, StepDirSync} {
		t.Run(string(step), func(t *testing.T) {
			directory := t.TempDir()
			err := WriteBytesAtomicInjected(directory, "f", []byte("x"), 0o600, &Injector{Step: step, Err: errBoom})
			if !errors.Is(err, errBoom) {
				t.Fatalf("WriteBytesAtomicInjected error = %v", err)
			}
			if step == StepDirSync {
				if _, statErr := os.Stat(filepath.Join(directory, "f")); statErr != nil {
					t.Fatalf("published file missing after dir sync failure: %v", statErr)
				}
			} else {
				assertNoAtomicTemps(t, directory, ".f.tmp-*")
			}
		})
	}

	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteBytesAtomicInjected(parentFile, "f", []byte("x"), 0o600, nil); err == nil {
		t.Fatal("atomic write accepted a file as its parent directory")
	}

	directory := t.TempDir()
	openAfterRemoval := &Injector{Step: StepOpenDirectory, FailAfterHook: true}
	openAfterRemoval.Hook = func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Fatalf("remove published directory: %v", err)
		}
	}
	if err := WriteBytesAtomicInjected(directory, "f", []byte("x"), 0o600, openAfterRemoval); err == nil {
		t.Fatal("atomic write accepted a directory removed before final sync")
	}
}

func assertNoAtomicTemps(t *testing.T, directory, pattern string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temporary files: %v", matches)
	}
}
