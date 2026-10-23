//go:build !windows

package worktrees

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirtyCapturePublicationRefusesOccupiedDestinations(t *testing.T) {
	t.Parallel()
	claim := strings.Repeat("a", 64)
	for _, phase := range []string{"root", "claim", "blob", "manifest"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			runPath := t.TempDir()
			capturePath := filepath.Join(runPath, "dirty-discard", claim)
			material := dirtyCaptureMaterial{Manifest: dirtyCaptureManifest{Version: 1}, Blobs: map[string][]byte{"1-blob.bin": []byte("b")}}
			occupied := filepath.Join(runPath, "dirty-discard")
			switch phase {
			case "claim":
				occupied = capturePath
			case "blob":
				occupied = filepath.Join(capturePath, "1-blob.bin")
			case "manifest":
				occupied = filepath.Join(capturePath, "manifest.json")
			}
			if err := os.MkdirAll(filepath.Dir(occupied), 0o700); err != nil {
				t.Fatal(err)
			}
			marker := []byte("preserve existing occupant")
			if err := os.WriteFile(occupied, marker, 0o600); err != nil {
				t.Fatal(err)
			}
			runDir, err := os.Open(runPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runDir.Close() })
			receipt, err := materializeDirtyCapture(runDir, claim, material)
			if err == nil || receipt != (DirtyWorktreeEvidence{}) {
				t.Fatalf("occupied %s accepted: %+v, %v", phase, receipt, err)
			}
			if phase == "blob" && !strings.Contains(err.Error(), "write dirty capture blob") || phase == "manifest" && !strings.Contains(err.Error(), "write dirty capture manifest") {
				t.Fatalf("wrong refusal phase: %v", err)
			}
			got, err := os.ReadFile(occupied)
			if err != nil || !bytes.Equal(got, marker) {
				t.Fatalf("existing occupant changed: %q, %v", got, err)
			}
			if phase == "blob" {
				if _, err := os.Lstat(filepath.Join(capturePath, "manifest.json")); !os.IsNotExist(err) {
					t.Fatalf("failed blob published manifest: %v", err)
				}
			}
		})
	}
}

func TestDirtyCapturePublicationReplaysExactBytesAndConflictsWithoutReplacement(t *testing.T) {
	t.Parallel()
	runPath := t.TempDir()
	runDir, err := os.Open(runPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runDir.Close() })
	claim := strings.Repeat("b", 64)
	content := []byte("target")
	digest := sha256.Sum256(content)
	sha := hex.EncodeToString(digest[:])
	name := dirtyCaptureBlobName(len(content), sha)
	entry := dirtyCaptureEntry{Path: "link", Kind: "symlink", Mode: 0o777, Bytes: int64(len(content)), SHA256: sha, Blob: name}
	want := DirtyWorktreeEvidence{SHA256: dirtyCaptureDigest([]dirtyCaptureEntry{entry}), Bytes: int64(len(content)), Files: 1}
	material := dirtyCaptureMaterial{Manifest: dirtyCaptureManifest{Version: 1, Receipt: want, Entries: []dirtyCaptureEntry{entry}}, Blobs: map[string][]byte{name: content}}
	for range 2 {
		got, err := materializeDirtyCapture(runDir, claim, material)
		if err != nil || got != want {
			t.Fatalf("capture/replay: %+v, %v", got, err)
		}
	}
	directory := filepath.Join(runPath, "dirty-discard", claim)
	raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest dirtyCaptureManifest
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest.Receipt != want || len(manifest.Entries) != 1 || manifest.Entries[0] != entry {
		t.Fatalf("durable manifest: %+v, %v", manifest, err)
	}
	material.Blobs[name] = []byte("change")
	if _, err := materializeDirtyCapture(runDir, claim, material); err == nil || !strings.Contains(err.Error(), "write dirty capture blob") {
		t.Fatalf("conflicting replay accepted: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("published blob changed: %q, %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("published manifest changed: %q, %v", got, err)
	}
}

func TestDirtyCaptureRechecksSymlinkBytesAfterIdentityObservation(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"replace", "remove", "unchanged"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, "link")
			if err := os.Symlink("target", path); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = root.Close() })
			fired := false
			entry, content, err := readDirtyCaptureEntry(root, "link", 0, func(stage string, _ *os.Root, _ *os.File) {
				if stage != "symlink-stat-after" {
					return
				}
				fired = true
				if action == "unchanged" {
					return
				}
				if err := os.Rename(path, path+"-retained"); err != nil {
					t.Fatal(err)
				}
				if action == "replace" {
					// Equal-length targets also defeat a size-only check. The last
					// identity observation is deliberately from the original link,
					// which models the identity-match outcome of inode reuse.
					if err := os.Symlink("change", path); err != nil {
						t.Fatal(err)
					}
				}
			})
			if !fired {
				t.Fatal("target verification boundary was not reached")
			}
			if action == "unchanged" {
				if err != nil || string(content) != "target" || entry.Kind != "symlink" || entry.Bytes != 6 {
					t.Fatalf("stable symlink: %+v, %q, %v", entry, content, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "dirty symlink changed") || content != nil || entry != (dirtyCaptureEntry{}) {
				t.Fatalf("stale symlink accepted: %+v, %q, %v", entry, content, err)
			}
		})
	}
}
