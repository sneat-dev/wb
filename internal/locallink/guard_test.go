package locallink

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

type fakeLinkStore struct {
	links map[string][]streams.StreamLink
	err   error
}

func (store fakeLinkStore) LiveLinksForWorktree(worktree string) ([]streams.StreamLink, error) {
	if store.err != nil {
		return nil, store.err
	}
	return store.links[worktree], nil
}

// AC: merge-refuses-while-a-link-is-live — a recorded link refuses, naming the
// link and the command that clears it.
func TestHasLiveLinkReportsARecordedLink(t *testing.T) {
	worktree := t.TempDir()
	store := fakeLinkStore{links: map[string][]streams.StreamLink{
		worktree: {{
			Stream: "checkout-rewrite", Repository: "acme/app",
			Link: streams.Link{
				Library: "/work/library", LibraryRepository: "acme/library",
				Mechanism: streams.MechanismPnpmLink, Identity: "@acme/core",
			},
		}},
	}}
	links, err := HasLiveLink(store, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Source != "stream-state" {
		t.Fatalf("links = %#v", links)
	}
	message := RefusalMessage(worktree, links)
	if !strings.Contains(message, "@acme/core") {
		t.Errorf("refusal does not name the link: %s", message)
	}
	if !strings.Contains(message, "wb deps propagate local /work/library --to "+worktree+" --undo") {
		t.Errorf("refusal does not name the exact clearing command: %s", message)
	}
}

// AC: merge-refuses-while-a-link-is-live — the second half: a hand-written
// go.work with a `use` entry and NO stream record still refuses. State alone
// would miss it, which is why the two signals are independent.
func TestHasLiveLinkReportsAHandWrittenGoWorkWithNoStreamRecord(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "go.work"), []byte("go 1.27\n\nuse (\n\t./backend\n\t/elsewhere/library/backend\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	links, err := HasLiveLink(fakeLinkStore{links: map[string][]streams.StreamLink{}}, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Source != "go.work" {
		t.Fatalf("links = %#v, want the go.work signal alone", links)
	}
	if !strings.Contains(links[0].Detail, "/elsewhere/library/backend") {
		t.Errorf("detail does not name the use entry: %s", links[0].Detail)
	}
}

// "I could not tell" must not be spelled the same way as "there is no link".
func TestHasLiveLinkPropagatesAnUnreadableStore(t *testing.T) {
	if _, err := HasLiveLink(fakeLinkStore{err: errors.New("state is unreadable")}, t.TempDir()); err == nil {
		t.Fatal("an unreadable store reported no links")
	}
}

func TestHasLiveLinkOnACleanWorktreeReportsNothing(t *testing.T) {
	links, err := HasLiveLink(fakeLinkStore{links: map[string][]streams.StreamLink{}}, t.TempDir())
	if err != nil || len(links) != 0 {
		t.Fatalf("links = %#v, err = %v", links, err)
	}
	if RefusalMessage(t.TempDir(), nil) != "" {
		t.Error("a clean worktree produced a refusal message")
	}
}

// A go.work with an empty use block is not a link.
func TestGoWorkWithNoUseEntriesIsNotALink(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "go.work"), []byte("go 1.27\n\nuse (\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	links, err := HasLiveLink(nil, worktree)
	if err != nil || len(links) != 0 {
		t.Fatalf("links = %#v, err = %v", links, err)
	}
}

func TestGoWorkUseEntriesReadsBothSpellingsAndSkipsComments(t *testing.T) {
	worktree := t.TempDir()
	contents := "// generated\ngo 1.27\n\nuse ./backend\n\nuse (\n\t// a comment inside the block\n\t./tools/lint\n\t/abs/library/backend\n)\n"
	if err := os.WriteFile(filepath.Join(worktree, "go.work"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := GoWorkUseEntries(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(entries, "./backend", "./tools/lint", "/abs/library/backend") {
		t.Fatalf("entries = %v", entries)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry, "//") {
			t.Errorf("a comment was read as a use entry: %q", entry)
		}
	}
}

func TestGoWorkUseEntriesOnAWorktreeWithoutOneIsEmpty(t *testing.T) {
	entries, err := GoWorkUseEntries(t.TempDir())
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %v, err = %v", entries, err)
	}
}
