package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func TestManagedHookSnapshotRefusesClosedOwnedDescriptor(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"stat", "identity", "content"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "pre-commit")
			original := []byte("#!/bin/sh\necho retained\n")
			if err := testenv.WriteExecutableFile(path, original, 0700); err != nil {
				t.Fatal(err)
			}
			directory := openOwnedHookTestDirectory(t, root)
			var held *os.File
			snapshot, err := readManagedHookObserved(directory, "pre-commit", func(at string, file *os.File) {
				if at == stage {
					held = file
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				}
			})
			if err == nil || snapshot.identity.exists || held == nil {
				t.Fatalf("snapshot=%+v error=%v held=%v", snapshot, err, held)
			}
			if stage == "identity" && !strings.Contains(err.Error(), "inspect managed hook identity pre-commit") {
				t.Fatalf("identity context=%v", err)
			}
			if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("owned descriptor remains live: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(original) {
				t.Fatalf("owned hook=%q error=%v", after, err)
			}
		})
	}
}

func TestTemporaryHookIdentityFailurePreservesActiveHook(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	managed, err := openManagedHooksDirectory("", filepath.Join(root, "hooks"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(managed.close)
	path := filepath.Join(managed.path, "pre-commit")
	original := []byte("#!/bin/sh\necho retained\n")
	if err := testenv.WriteExecutableFile(path, original, 0700); err != nil {
		t.Fatal(err)
	}
	snapshot, err := readManagedHook(managed.directory, "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	var held *os.File
	err = writeExecutableAtObserved(managed, "pre-commit", []byte("replacement"), snapshot.identity, nil, nil, func(file *os.File) {
		held = file
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "inspect temporary hook pre-commit") {
		t.Fatalf("temporary identity=%v", err)
	}
	if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("temporary descriptor live: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatalf("active hook=%q error=%v", after, err)
	}
}

func openOwnedHookTestDirectory(t *testing.T, path string) *os.File {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	directory := os.NewFile(uintptr(fd), path)
	t.Cleanup(func() { _ = directory.Close() })
	return directory
}
