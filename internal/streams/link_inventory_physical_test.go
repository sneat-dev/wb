package streams

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPhysicalWorkspaceLinkInventoryUsesGoSyntaxAndRefusesUnreadableEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, contents string
		want           []string
		failure        bool
	}{
		{name: "single", contents: "go 1.27\nuse ./module // ordinary inline comment\n", want: []string{"./module"}},
		{name: "block quoted spaces", contents: "go 1.27\nuse (\n \"./module with spaces\" // quoted entry\n ./a\n)\n", want: []string{"./a", "./module with spaces"}},
		{name: "empty", contents: "go 1.27\nuse (\n)\n", want: []string{}},
		{name: "malformed", contents: "{malformed\n", failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			worktree := t.TempDir()
			path := filepath.Join(worktree, GoWorkFile)
			if err := os.WriteFile(path, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			entries, err := GoWorkUseEntries(worktree)
			if tc.failure {
				if err == nil || !strings.Contains(err.Error(), path) {
					t.Fatal("malformed workspace reported no link", entries, err)
				}
			} else if err != nil || !slices.Equal(entries, tc.want) {
				t.Fatal(entries, err)
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil || string(raw) != tc.contents {
				t.Fatal("read changed workspace", readErr)
			}
		})
	}
	for _, fault := range []string{"absent", "dangling", "directory"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			worktree := t.TempDir()
			path := filepath.Join(worktree, GoWorkFile)
			if fault == "dangling" {
				if err := os.Symlink("missing-workspace", path); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			_, original := os.ReadFile(path)
			entries, err := GoWorkUseEntries(worktree)
			if fault == "absent" {
				if err != nil || len(entries) != 0 {
					t.Fatal(entries, err)
				}
				return
			}
			var originalPath, currentPath *os.PathError
			if err == nil || !errors.As(original, &originalPath) || !errors.As(err, &currentPath) || currentPath.Path != originalPath.Path || !errors.Is(err, originalPath.Err) {
				t.Fatalf("physical read failure lost: %v -> %v", original, err)
			}
		})
	}
}

func TestStreamLoadDistinguishesMissingAndDanglingRecords(t *testing.T) {
	t.Parallel()
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Load("../outside"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid stream name reached an absence decision: %v", err)
	}
	if _, err := store.Load("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	path := store.statePath("broken")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-state", path); err != nil {
		t.Fatal(err)
	}
	_, original := os.ReadFile(path)
	_, err := store.Load("broken")
	var originalPath, currentPath *os.PathError
	if errors.Is(err, ErrNotFound) || !errors.As(original, &originalPath) || !errors.As(err, &currentPath) || currentPath.Path != originalPath.Path || !errors.Is(err, originalPath.Err) {
		t.Fatalf("dangling path reported absent: %v", err)
	}
	all, unreadable, listErr := store.List()
	if listErr != nil || len(all) != 0 || len(unreadable) != 1 || unreadable[0].Path != path {
		t.Fatalf("existing dangling path dropped: %+v %v", unreadable, listErr)
	}
}
