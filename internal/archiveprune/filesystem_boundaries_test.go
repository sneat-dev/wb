package archiveprune

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	unixcompat "github.com/sneat-dev/wb/internal/unixcompat"
)

func TestCleanPreservesAbsolutePathFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("working directory disappeared")
	called := false
	outcome, err := cleanWithAbs(context.Background(), Options{ProjectsRoot: "relative", Apply: true}, func(path string) (string, error) {
		called = true
		if path != "relative" {
			t.Fatalf("path=%q", path)
		}
		return "", failure
	})
	if !called || !errors.Is(err, failure) || outcome.Results != nil {
		t.Fatalf("result=%+v error=%v called=%t", outcome, err, called)
	}
}

func TestOpenedEntryFailuresCloseTheirOwnedDescriptors(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"directory stat", "file stat", "file hash", "directory listing", "planning listing", "deletion listing"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			mustWriteFile(t, filepath.Join(root, "file.txt"), "preserved content\n")
			mustWriteFile(t, filepath.Join(root, "nested", "child.txt"), "preserved child\n")
			parentFD, err := unixcompat.Open(root, unixcompat.O_RDONLY|unixcompat.O_DIRECTORY|unixcompat.O_NOFOLLOW, 0)
			if err != nil {
				t.Fatal(err)
			}
			parent := os.NewFile(uintptr(parentFD), root)
			t.Cleanup(func() { _ = parent.Close() })
			failure := errors.New("filesystem became unavailable")
			var owned *os.File
			inspect := func(file *os.File, _ *unixcompat.Stat_t) error { owned = file; return failure }
			listing := func(file *os.File, _ string) ([]string, error) { owned = file; return nil, failure }
			var stat unixcompat.Stat_t
			name := "file.txt"
			if operation == "directory stat" {
				name = "nested"
			}
			if err := unixcompat.Fstatat(int(parent.Fd()), name, &stat, unixcompat.AT_SYMLINK_NOFOLLOW); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "directory stat":
				file, e := openDirectoryAtWithStat(parent, "nested", "nested", stat, inspect)
				err = e
				if file != nil {
					t.Fatal("failed stat returned open directory")
				}
			case "file stat":
				_, err = hashFileAtWithIO(parent, "file.txt", "file.txt", stat, inspect, io.Copy)
			case "file hash":
				_, err = hashFileAtWithIO(parent, "file.txt", "file.txt", stat, statOpenedFile, func(_ io.Writer, reader io.Reader) (int64, error) { owned = reader.(*os.File); return 0, failure })
			case "directory listing":
				_, err = directoryNamesWithRead(parent, root, func(file *os.File) ([]string, error) { owned = file; return nil, failure })
			case "planning listing":
				var entries []UntrackedEntry
				err = collectEntryAtWithNames(parent, root, "nested", "nested", &entries, 0, listing)
				if len(entries) != 1 || entries[0].Kind != "directory" {
					t.Fatalf("partial plan=%+v", entries)
				}
			case "deletion listing":
				plan, e := planUntracked(root, []string{"nested"})
				if e != nil {
					t.Fatal(e)
				}
				manifest := make(map[string]UntrackedEntry)
				for _, entry := range plan {
					manifest[entry.Path] = entry
				}
				err = removeExactPathAtWithNames(parent, root, "nested", manifest, 0, listing)
			}
			if !errors.Is(err, failure) || owned == nil {
				t.Fatalf("error=%v descriptor=%v", err, owned)
			}
			if _, err := owned.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("owned descriptor survived failure: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(root, "file.txt")); err != nil || string(got) != "preserved content\n" {
				t.Fatalf("file changed: %q %v", got, err)
			}
			if got, err := os.ReadFile(filepath.Join(root, "nested", "child.txt")); err != nil || string(got) != "preserved child\n" {
				t.Fatalf("child changed: %q %v", got, err)
			}
		})
	}
}

func TestReceiptRefusesInvalidTimestampBeforePublishing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	receipt := archiveCleanReceipt{Repository: "acme/widgets", CreatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}
	path := filepath.Join(root, "receipt.json")
	mustWriteFile(t, path, "prior durable evidence\n")
	if err := overwriteArchiveCleanReceipt(path, receipt); err == nil || !strings.Contains(err.Error(), "encode archive-clean receipt") {
		t.Fatalf("invalid timestamp accepted: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "prior durable evidence\n" {
		t.Fatalf("evidence changed: %q %v", got, err)
	}
	if published, err := writeArchiveCleanReceipt(root, receipt); err == nil || published != "" {
		t.Fatalf("invalid receipt published: %q %v", published, err)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".wb", "reports", "archive-clean", "*.json"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("invalid evidence appeared: %v %v", matches, err)
	}
}

func TestReceiptDirectoryReadFailurePreservesPublishedEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "receipt.json")
	failure := errors.New("receipt directory disappeared after publication")
	opens := 0
	err := overwriteArchiveCleanReceiptWithOpen(path, archiveCleanReceipt{Repository: "acme/widgets"}, nil, func(dir string) (*os.File, error) {
		opens++
		if dir != root {
			t.Fatalf("directory=%q", dir)
		}
		if _, err := os.ReadFile(path); err != nil {
			t.Fatalf("publication has not happened: %v", err)
		}
		return nil, failure
	})
	if !errors.Is(err, failure) || opens != 1 || !strings.Contains(err.Error(), "open archive-clean receipt directory") {
		t.Fatalf("error=%v opens=%d", err, opens)
	}
	if got, err := os.ReadFile(path); err != nil || !strings.Contains(string(got), `"repository": "acme/widgets"`) {
		t.Fatalf("published evidence=%q %v", got, err)
	}
}
