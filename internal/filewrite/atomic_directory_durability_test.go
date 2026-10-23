package filewrite

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicPublicationKeepsNewBytesVisibleWhenDirectoryDurabilityFails(t *testing.T) {
	t.Parallel()
	for _, step := range []Step{StepOpenDirectory, StepDirSync} {
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, "receipt.json")
			old := []byte("old receipt\n")
			updated := []byte("new receipt\n")
			fault := errors.New("directory durability unavailable")
			if err := os.WriteFile(path, old, 0o600); err != nil {
				t.Fatal(err)
			}
			err := WriteBytesAtomicInjected(directory, "receipt.json", updated, 0o600, &Injector{Step: step, Err: fault})
			if !errors.Is(err, fault) {
				t.Fatalf("durability error=%v,want %v", err, fault)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, updated) {
				t.Fatalf("published bytes=%q,read error=%v,want=%q", got, err, updated)
			}
			stat, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if stat.Mode().Perm() != 0o600 {
				t.Fatalf("published mode=%o,want600", stat.Mode().Perm())
			}
			assertNoAtomicTemps(t, directory, ".receipt.json.tmp-*")
		})
	}
}
