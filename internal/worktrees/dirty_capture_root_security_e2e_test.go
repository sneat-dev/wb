//go:build e2e

package worktrees

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

//nolint:paralleltest // newJournalWorktree uses process-wide Git configuration.
func TestE2EDirtyCaptureRefusesParentLinkOutsideCheckout(t *testing.T) {
	worktree := newJournalWorktree(t)
	nested := filepath.Join(worktree, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(nested, "payload.txt")
	if err := os.WriteFile(tracked, []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "nested/payload.txt")
	gitTest(t, worktree, "commit", "-m", "seed")
	if err := os.WriteFile(tracked, []byte("dirty local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(filepath.Dir(worktree), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := []byte("outside owned marker\n")
	outsideFile := filepath.Join(outside, "payload.txt")
	if err := os.WriteFile(outsideFile, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(nested, nested+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, nested); err != nil {
		t.Fatal(err)
	}
	paths, err := dirtyCapturePaths(context.Background(), worktree)
	if err != nil || !slices.Contains(paths, "nested/payload.txt") {
		t.Fatalf("Git dirty paths = %q, %v", paths, err)
	}

	material, err := collectDirtyCapture(context.Background(), worktree)
	if err == nil {
		for _, entry := range material.Manifest.Entries {
			if bytes.Equal(material.Blobs[entry.Blob], marker) {
				t.Fatalf("captured outside-owned bytes from %s", entry.Path)
			}
		}
		t.Fatalf("outside parent link was accepted: %#v", material.Manifest.Entries)
	}
	if !strings.Contains(err.Error(), "inspect dirty path nested/payload.txt") {
		t.Fatalf("outside link refused at wrong phase: %v", err)
	}
	home := t.TempDir()
	if _, err := captureAndPersistDirtyWorktree(context.Background(), home, worktree, nil); err == nil || !strings.Contains(err.Error(), "inspect dirty path nested/payload.txt") {
		t.Fatalf("discard capture crossed the source-custody boundary: %v", err)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("private capture created artifacts after refusal: %v, %v", entries, err)
	}
	got, readErr := os.ReadFile(outsideFile)
	if readErr != nil || !bytes.Equal(got, marker) {
		t.Fatalf("outside bytes changed: %q, %v", got, readErr)
	}
}

//nolint:paralleltest // newJournalWorktree uses process-wide Git configuration.
func TestE2EDirtyCaptureRetainsSafeInRootAliasAndHardlink(t *testing.T) {
	worktree := newJournalWorktree(t)
	nested := filepath.Join(worktree, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(nested, "payload.txt")
	if err := os.WriteFile(tracked, []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "nested/payload.txt")
	gitTest(t, worktree, "commit", "-m", "seed")
	if err := os.WriteFile(tracked, []byte("dirty local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(nested, nested+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested-saved", nested); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(nested+"-saved", "payload.txt"), filepath.Join(worktree, "hardlink.txt")); err != nil {
		t.Fatal(err)
	}
	material, err := collectDirtyCapture(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"nested/payload.txt", "hardlink.txt"} {
		found := false
		for _, entry := range material.Manifest.Entries {
			if entry.Path == path {
				found = true
				if entry.Kind != "file" || !bytes.Equal(material.Blobs[entry.Blob], []byte("dirty local\n")) {
					t.Fatalf("%s = %#v, blob %q", path, entry, material.Blobs[entry.Blob])
				}
			}
		}
		if !found {
			t.Fatalf("missing %s from %#v", path, material.Manifest.Entries)
		}
	}
}

//nolint:paralleltest // newJournalWorktree uses process-wide Git configuration.
func TestE2EDirtyCaptureClassifiesMissingParentAsDeleted(t *testing.T) {
	worktree := newJournalWorktree(t)
	nested := filepath.Join(worktree, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(nested, "payload.txt")
	if err := os.WriteFile(tracked, []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "nested/payload.txt")
	gitTest(t, worktree, "commit", "-m", "seed")
	if err := os.RemoveAll(nested); err != nil {
		t.Fatal(err)
	}
	material, err := collectDirtyCapture(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(material.Manifest.Entries) != 1 || material.Manifest.Entries[0].Path != "nested/payload.txt" || material.Manifest.Entries[0].Kind != "deleted" {
		t.Fatalf("missing parent = %#v", material.Manifest.Entries)
	}
}

//nolint:paralleltest // newJournalWorktree uses process-wide Git configuration.
func TestE2EDirtyCaptureRefusesRootSymlink(t *testing.T) {
	worktree := newJournalWorktree(t)
	alias := filepath.Join(filepath.Dir(worktree), "alias")
	if err := os.Symlink(worktree, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := collectDirtyCapture(context.Background(), alias); err == nil || !strings.Contains(err.Error(), "non-directory dirty worktree root") {
		t.Fatalf("symlink root refusal = %v", err)
	}
}

func TestE2EDirtyCaptureRootIdentityRefusesReplacement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initial, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := confirmDirtyCaptureRoot(root, initial); err == nil || !strings.Contains(err.Error(), "changed during capture") {
		t.Fatalf("replacement root accepted: %v", err)
	}
}

func TestE2EDirtyCaptureRootAndSymlinkErrorBoundaries(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := collectDirtyCapture(context.Background(), missing); err == nil || !strings.Contains(err.Error(), "inspect dirty worktree root") {
		t.Fatalf("missing root = %v", err)
	}
	ordinary := t.TempDir()
	if _, err := collectDirtyCapture(context.Background(), ordinary); err == nil || !strings.Contains(err.Error(), "inspect tracked dirty paths") {
		t.Fatalf("non-Git directory = %v", err)
	}
	initial, err := os.Lstat(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ordinary); err != nil {
		t.Fatal(err)
	}
	if err := confirmDirtyCaptureRoot(ordinary, initial); err == nil || !strings.Contains(err.Error(), "recheck dirty worktree root") {
		t.Fatalf("removed root = %v", err)
	}
	rootDirectory := t.TempDir()
	if err := os.Symlink("missing", filepath.Join(rootDirectory, "link")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if _, _, err := readDirtyCaptureEntry(root, "link", maxDirtyCaptureTotalBytes, nil); err == nil || !strings.Contains(err.Error(), "exceeds bounded") {
		t.Fatalf("symlink total bound = %v", err)
	}
}

//nolint:paralleltest // newJournalWorktree uses process-wide Git configuration.
func TestE2EDirtyCaptureEmptyChangedSet(t *testing.T) {
	worktree := newJournalWorktree(t)
	gitTest(t, worktree, "commit", "--allow-empty", "-m", "seed")
	material, err := collectDirtyCapture(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(material.Manifest.Entries) != 0 || material.Manifest.Receipt.Files != 0 || material.Manifest.Receipt.SHA256 != dirtyCaptureDigest(nil) {
		t.Fatalf("empty capture = %#v", material.Manifest)
	}
}
