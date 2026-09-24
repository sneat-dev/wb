package streams

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoWorkUseEntriesReadsTheFileAndReportsAnUnreadableOne(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	if entries, err := GoWorkUseEntries(worktree); err != nil || len(entries) != 0 {
		t.Fatalf("GoWorkUseEntries without a go.work = %v, %v; want none", entries, err)
	}

	if err := os.WriteFile(filepath.Join(worktree, GoWorkFile), []byte("go 1.22\n\nuse ./libs/alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := GoWorkUseEntries(worktree)
	if err != nil || len(entries) != 1 || entries[0] != "./libs/alpha" {
		t.Fatalf("GoWorkUseEntries = %v, %v; want the single use entry", entries, err)
	}

	// A go.work that cannot be read is an error, not an absent workspace: a
	// hand-written workspace is exactly what the refusal exists to catch.
	if err := os.Remove(filepath.Join(worktree, GoWorkFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(worktree, GoWorkFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := GoWorkUseEntries(worktree); err == nil || !strings.Contains(err.Error(), GoWorkFile) {
		t.Fatalf("unreadable go.work error = %v, want it to name the file", err)
	}
}

func TestParseGoWorkUseEntriesReadsBothSpellingsAndSkipsComments(t *testing.T) {
	t.Parallel()
	contents := `go 1.22

use ./libs/alpha

// use ./libs/commented

use (
	./libs/gamma
	"./libs/beta"

	// ./libs/inside-comment
)

use ./libs/delta
`
	got := ParseGoWorkUseEntries(contents)
	want := []string{"./libs/alpha", "./libs/beta", "./libs/delta", "./libs/gamma"}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("entries = %v, want the sorted, unquoted %v", got, want)
		}
	}
}

func TestParseGoWorkUseEntriesIgnoresACommentedBlock(t *testing.T) {
	t.Parallel()
	contents := "// use (\n//\t./libs/not-linked\n// )\n"
	if entries := ParseGoWorkUseEntries(contents); len(entries) != 0 {
		t.Fatalf("entries = %v, want a commented block to contribute nothing", entries)
	}
}
