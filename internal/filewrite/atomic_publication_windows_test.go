//go:build windows

package filewrite

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsAtomicPublicationAndDirectoryFaults(t *testing.T) {
	t.Parallel()
	for _, step := range []Step{"", StepOpenDirectory, StepDirSync} {
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, "receipt.json")
			updated := []byte("new receipt\n")
			fault := errors.New("directory durability unavailable")
			if err := os.WriteFile(path, []byte("old receipt\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var inj *Injector
			if step != "" {
				inj = &Injector{Step: step, Err: fault}
			}
			err := WriteBytesAtomicInjected(directory, "receipt.json", updated, 0o600, inj)
			if step == "" {
				if err != nil {
					t.Fatalf("native Windows atomic publication: %v", err)
				}
			} else if !errors.Is(err, fault) {
				t.Fatalf("injected directory failure=%v,want %v", err, fault)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, updated) {
				t.Fatalf("published bytes=%q,error=%v,want=%q", got, err, updated)
			}
			assertNoAtomicTemps(t, directory, ".receipt.json.tmp-*")
		})
	}
}
