package worktrees

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDirtyPathRecordsValidateDeduplicateAndSort(t *testing.T) {
	t.Parallel()
	paths, err := parseDirtyCapturePaths("z\x00a\x00\x00", "a\x00nested/file\x00")
	if err != nil || !reflect.DeepEqual(paths, []string{"a", "nested/file", "z"}) {
		t.Fatalf("normalized paths: %v %v", paths, err)
	}
	empty, err := parseDirtyCapturePaths("", "")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty records: %#v %v", empty, err)
	}
	for _, path := range []string{"../outside", "a/../outside", "./file", ".", filepath.Join(t.TempDir(), "absolute")} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			got, err := parseDirtyCapturePaths("valid\x00", path+"\x00")
			if err == nil || got != nil || !strings.Contains(err.Error(), "refusing") {
				t.Fatalf("arbitrary parser input: %v %v", got, err)
			}
		})
	}
}
