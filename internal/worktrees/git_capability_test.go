package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitFilesystemCapabilityRetainsValidatedDirectories(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	directory := openCapabilityDirectory(t, path)
	closed := openCapabilityDirectory(t, t.TempDir())
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(path, "evidence")
	if err := os.WriteFile(filePath, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = regular.Close() })
	for _, tc := range []struct {
		name       string
		roots      []gitFilesystemCapabilityRoot
		diagnostic string
		cause      error
	}{
		{"empty", nil, "at least one writable root", nil},
		{"absent", []gitFilesystemCapabilityRoot{{path: path}}, "descriptor is unavailable", nil},
		{"closed", []gitFilesystemCapabilityRoot{{path: path, directory: closed}}, "inspect git capability root", os.ErrClosed},
		{"regular", []gitFilesystemCapabilityRoot{{path: filePath, directory: regular}}, "not a directory", nil},
		{"relative", []gitFilesystemCapabilityRoot{{path: "relative", directory: directory}}, "must be absolute", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := newGitFilesystemCapability(tc.roots...)
			if err == nil || len(got.writeRoots) != 0 || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("capability = %+v, %v", got, err)
			}
			if len(tc.roots) > 0 && !strings.Contains(err.Error(), filepath.Clean(tc.roots[0].path)) {
				t.Fatalf("capability error omitted root path: %v", err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("capability error = %v, want %v", err, tc.cause)
			}
		})
	}
	got, err := newGitFilesystemCapability(gitFilesystemCapabilityRoot{path: path, directory: directory}, gitFilesystemCapabilityRoot{path: path + "/.", directory: directory})
	if err != nil || len(got.writeRoots) != 1 || got.writeRoots[0].directory != directory || got.writeRoots[0].path != path {
		t.Fatalf("deduplicated retained capability = %+v, %v", got, err)
	}
	// Parent cleanup runs after the parallel rejection cases, so this also
	// verifies that rejected file authority never altered the retained evidence.
	t.Cleanup(func() {
		if contents, err := os.ReadFile(filePath); err != nil || string(contents) != "retained" {
			t.Errorf("evidence = %q, %v", contents, err)
		}
	})
}

func openCapabilityDirectory(t *testing.T, path string) *os.File {
	t.Helper()
	directory, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return directory
}

func TestGitFilesystemCapabilitySortsRootsAndRetainsHandles(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	firstPath, secondPath := filepath.Join(base, "a"), filepath.Join(base, "z")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	first, second := openCapabilityDirectory(t, firstPath), openCapabilityDirectory(t, secondPath)
	got, err := newGitFilesystemCapability(
		gitFilesystemCapabilityRoot{path: secondPath, directory: second},
		gitFilesystemCapabilityRoot{path: firstPath, directory: first},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.writeRoots) != 2 || got.writeRoots[0].path != firstPath || got.writeRoots[0].directory != first || got.writeRoots[1].path != secondPath || got.writeRoots[1].directory != second {
		t.Fatalf("sorted retained roots = %+v", got.writeRoots)
	}
}
