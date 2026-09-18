//go:build windows

package archiveprune

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsPlanUntrackedSimpleFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const contents = "ordinary untracked file\n"
	if err := os.WriteFile(filepath.Join(root, "plain.txt"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := planUntracked(root, []string{"plain.txt"})
	if err != nil {
		t.Fatalf("plan unchanged Windows file: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "plain.txt" || entries[0].Size != int64(len(contents)) {
		t.Fatalf("planned entries = %+v", entries)
	}
}
