package worktrees

import (
	"errors"
	"os"
	"testing"
)

func TestCloseIncompleteInheritedFilesReleasesOnlyPartialGroups(t *testing.T) {
	t.Parallel()
	complete, err := os.CreateTemp(t.TempDir(), "complete")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = complete.Close() })
	if closeIncompleteInheritedFiles(complete) {
		t.Fatal("complete descriptor group was reported incomplete")
	}
	if _, err := complete.WriteString("retained"); err != nil {
		t.Fatalf("complete descriptor was closed: %v", err)
	}

	partial, err := os.CreateTemp(t.TempDir(), "partial")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = partial.Close() })
	if !closeIncompleteInheritedFiles(nil, partial) {
		t.Fatal("partial descriptor group was reported complete")
	}
	if _, err := partial.WriteString("closed"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("partial descriptor was not closed: %v", err)
	}
}
