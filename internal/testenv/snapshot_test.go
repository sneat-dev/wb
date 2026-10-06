package testenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotTreesPreservesExactBytesAndMissingRoots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "nested", "record")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{'x', 0, '\n'}, 0600); err != nil {
		t.Fatal(err)
	}
	got := SnapshotTrees(t, root, filepath.Join(root, "absent"))
	if len(got) != 1 || string(got[path]) != "x\x00\n" {
		t.Fatalf("snapshot=%#v", got)
	}
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if string(got[path]) != "x\x00\n" {
		t.Fatal("snapshot bytes changed with the file")
	}
}
func TestSnapshotTreesReportsNativeReadAndWalkFailures(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"read", "walk"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if kind == "read" {
				if err := os.Symlink(filepath.Join(root, "absent"), filepath.Join(root, "broken")); err != nil {
					t.Fatal(err)
				}
			} else {
				root = strings.Repeat("x", 4096)
			}
			recorder := &recordingTB{}
			done := make(chan struct{})
			go func() { defer close(done); SnapshotTrees(recorder, root) }()
			<-done
			recorder.mu.Lock()
			t.Cleanup(recorder.mu.Unlock)
			if !recorder.fataled || !strings.Contains(recorder.fatalMsg, "snapshot trees") {
				t.Fatalf("fatal=%v message=%q", recorder.fataled, recorder.fatalMsg)
			}
		})
	}
}
