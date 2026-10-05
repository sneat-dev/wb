package streams

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkInventoriesRefuseUnreadablePhysicalRecords(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"malformed", "dangling record", "store is file"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			store := OpenAt(filepath.Join(t.TempDir(), "streams"))
			evidence := store.Root
			if fault == "store is file" {
				if err := os.WriteFile(evidence, []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				evidence = filepath.Join(store.Root, "broken", "stream.json")
				if err := os.MkdirAll(filepath.Dir(evidence), 0700); err != nil {
					t.Fatal(err)
				}
				if fault == "dangling record" {
					if err := os.Symlink("missing-record", evidence); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(evidence, []byte("{malformed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, _, original := store.List()
			links, err := store.LiveLinksForWorktree("/private/consumer")
			sources, sourceErr := store.LinkSourcesForWorktree("/private/library")
			if err == nil || sourceErr == nil || len(links) != 0 || len(sources) != 0 || !strings.Contains(err.Error(), evidence) || !strings.Contains(sourceErr.Error(), evidence) {
				t.Fatalf("unverified %s reported absent: %v %v", fault, err, sourceErr)
			}
			if original != nil {
				var originalPath, currentPath *os.PathError
				if !errors.As(original, &originalPath) || !errors.As(err, &currentPath) || currentPath.Op != originalPath.Op || currentPath.Path != originalPath.Path || !errors.Is(err, originalPath.Err) {
					t.Fatalf("read error identity changed: %v -> %v", original, err)
				}
			}
			if _, err := os.Lstat(evidence); err != nil {
				t.Fatal("link query deleted evidence", err)
			}
		})
	}
}

func TestLinkInventoriesPreserveAbsentAndEventOnlyStores(t *testing.T) {
	t.Parallel()
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	for _, eventsOnly := range []bool{false, true} {
		if eventsOnly {
			if err := os.MkdirAll(store.Dir(".fleet"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store.Dir(".fleet"), "events.jsonl"), []byte("event only\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		links, err := store.LiveLinksForWorktree("/private/consumer")
		sources, sourceErr := store.LinkSourcesForWorktree("/private/library")
		if err != nil || sourceErr != nil || len(links) != 0 || len(sources) != 0 {
			t.Fatalf("empty/event-only invented links: %v %v", err, sourceErr)
		}
	}
}
