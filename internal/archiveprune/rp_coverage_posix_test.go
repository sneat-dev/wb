//go:build !windows

package archiveprune

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// rpCovRestrict makes path unreadable (or unwritable) and restores its
// permissions when the test ends so temporary-directory cleanup can proceed.
func rpCovRestrict(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
}

func TestRPCovCleanReportsARemovalFailureAfterEligibility(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()
	rpCovRestrict(t, f.canonical, 0o500)

	result := cleanOne(context.Background(), t, f, true)

	if result.Applied {
		t.Fatalf("clone was reported deleted despite the removal failure: %+v", result)
	}
	if result.Error == "" || !strings.Contains(result.Error, "permission denied") {
		t.Fatalf("result = %+v, want the removal failure recorded", result)
	}
	if !result.Eligible {
		t.Fatalf("eligibility was changed by a removal failure: %+v", result)
	}
}

func TestRPCovPlanUntrackedRejectsUnreadableAndSpecialEntries(t *testing.T) {
	t.Parallel()
	t.Run("unreadable file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustWriteFile(t, filepath.Join(root, "secret.txt"), "secret\n")
		rpCovRestrict(t, filepath.Join(root, "secret.txt"), 0o000)
		if _, err := planUntracked(root, []string{"secret.txt"}); err == nil ||
			!strings.Contains(err.Error(), "open untracked file") {
			t.Fatalf("error = %v, want the unreadable-file refusal", err)
		}
	})

	t.Run("unreadable directory", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustMkdirAll(t, filepath.Join(root, "locked"))
		rpCovRestrict(t, filepath.Join(root, "locked"), 0o000)
		if _, err := planUntracked(root, []string{"locked"}); err == nil ||
			!strings.Contains(err.Error(), "open untracked directory") {
			t.Fatalf("error = %v, want the unreadable-directory refusal", err)
		}
		if _, err := planUntracked(root, []string{"locked/child.txt"}); err == nil {
			t.Fatal("planUntracked descended through an unreadable parent directory")
		}
	})

	t.Run("unreadable nested file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustWriteFile(t, filepath.Join(root, "nested", "secret.txt"), "secret\n")
		rpCovRestrict(t, filepath.Join(root, "nested", "secret.txt"), 0o000)
		if _, err := planUntracked(root, []string{"nested"}); err == nil {
			t.Fatal("planUntracked accepted an unreadable nested file")
		}
	})

	t.Run("special file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		fifo := filepath.Join(root, "pipe")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := planUntracked(root, []string{"pipe"}); err == nil ||
			!strings.Contains(err.Error(), "not a regular file or directory") {
			t.Fatalf("error = %v, want the special-file refusal", err)
		}
	})
}

func TestRPCovCleanAuthorizedUntrackedReportsReceiptWriteFailure(t *testing.T) {
	t.Parallel()
	repo := discover.Repo{Org: "acme", Name: "widgets", Path: t.TempDir()}
	planned := Result{
		Repository: repo.Slug(), Path: repo.Path,
		Untracked: []UntrackedEntry{{Path: "untracked.txt", Kind: "file"}},
	}
	result := cleanAuthorizedUntracked(context.Background(), rpCovUnusableProjectsRoot(t), repo, planned, Options{Apply: true, DeleteUntracked: true})
	if result.Error == "" || !strings.Contains(result.Error, "record untracked deletion receipt") {
		t.Fatalf("result = %+v, want the unwritable-receipt refusal", result)
	}
	if result.Applied {
		t.Fatal("a clone was pruned without a durable deletion receipt")
	}
}

func TestRPCovCleanAuthorizedUntrackedRefusesADeletionThatFails(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()
	mustWriteFile(t, filepath.Join(f.canonical, "cache", "nested.txt"), "cached\n")
	// The directory is restricted before the plan is taken, so the manifest
	// itself records the exact mode on disk and the deletion reaches the
	// unlink rather than being refused as plan drift.
	rpCovRestrict(t, filepath.Join(f.canonical, "cache"), 0o500)
	planned, err := planUntracked(f.canonical, []string{"cache"})
	if err != nil {
		t.Fatal(err)
	}

	result := cleanAuthorizedUntracked(context.Background(), f.projectsRoot,
		discover.Repo{Org: "acme", Name: "widgets", Path: f.canonical},
		Result{Repository: f.slug(), Path: f.canonical, Untracked: planned},
		Options{Apply: true, DeleteUntracked: true})

	if result.Reason != "" {
		t.Fatalf("result = %+v, want a hard deletion error rather than a drift refusal", result)
	}
	if !strings.Contains(result.Error, "remove authorised untracked file") {
		t.Fatalf("error = %q, want the failed unlink explained", result.Error)
	}
	if _, err := os.Stat(filepath.Join(f.canonical, "cache", "nested.txt")); err != nil {
		t.Fatalf("untracked file was deleted despite the reported failure: %v", err)
	}
}

func TestRPCovCleanAuthorizedUntrackedReportsAnUnwritableFinalReceipt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "acme", "widgets")
	f.archived()
	mustWriteFile(t, filepath.Join(f.canonical, "untracked.txt"), "delete me\n")
	planned, err := planUntracked(f.canonical, []string{"untracked.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// The receipt is finalized under the fixture root's own state home now.
	receiptDirectory := filepath.Join(f.projectsRoot, ".wb", "reports", "archive-clean")

	result := cleanAuthorizedUntracked(context.Background(), f.projectsRoot,
		discover.Repo{Org: "acme", Name: "widgets", Path: f.canonical},
		Result{Repository: f.slug(), Path: f.canonical, Untracked: planned},
		Options{Apply: true, DeleteUntracked: true, beforeUntrackedRevalidation: func() {
			rpCovRestrict(t, receiptDirectory, 0o500)
		}})

	if result.Error == "" || !strings.Contains(result.Error, "finalize untracked deletion receipt") {
		t.Fatalf("result = %+v, want the finalize failure recorded", result)
	}
	if _, err := os.Stat(filepath.Join(f.canonical, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("authorized deletion did not happen before the receipt was finalized: %v", err)
	}
}

func TestRPCovCleanAuthorizedUntrackedReportsACloneThatIsNoLongerEligible(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()
	mustWriteFile(t, filepath.Join(f.canonical, "untracked.txt"), "delete me\n")
	planned, err := planUntracked(f.canonical, []string{"untracked.txt"})
	if err != nil {
		t.Fatal(err)
	}

	result := cleanAuthorizedUntracked(context.Background(), f.projectsRoot,
		discover.Repo{Org: "acme", Name: "widgets", Path: f.canonical},
		Result{Repository: f.slug(), Path: f.canonical, Untracked: planned},
		Options{Apply: true, DeleteUntracked: true, beforeUntrackedRevalidation: func() {
			mustWriteFile(t, filepath.Join(f.canonical, "README.md"), "now dirty\n")
		}})

	if result.Applied {
		t.Fatalf("clone was pruned after becoming ineligible: %+v", result)
	}
	if !strings.Contains(result.Reason, "clone is no longer eligible") {
		t.Fatalf("reason = %q, want the explicit no-longer-eligible explanation", result.Reason)
	}
	if len(result.Untracked) != 1 {
		t.Fatalf("itemized untracked paths were lost from the result: %+v", result)
	}
}

func TestRPCovCleanAuthorizedUntrackedReportsARemovalFailure(t *testing.T) {
	isolateWBHome(t)
	f := newFixture(t, "acme", "widgets")
	f.archived()
	mustWriteFile(t, filepath.Join(f.canonical, "untracked.txt"), "delete me\n")
	planned, err := planUntracked(f.canonical, []string{"untracked.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// A read-only directory nested inside .git is invisible to every git
	// status check, so Evaluate still reports the clone as clean and only the
	// final removal can discover it.
	blocked := filepath.Join(f.canonical, ".git", "rp-cov-blocked")
	mustWriteFile(t, filepath.Join(blocked, "keep"), "x\n")
	rpCovRestrict(t, blocked, 0o500)

	result := cleanAuthorizedUntracked(context.Background(), f.projectsRoot,
		discover.Repo{Org: "acme", Name: "widgets", Path: f.canonical},
		Result{Repository: f.slug(), Path: f.canonical, Untracked: planned},
		Options{Apply: true, DeleteUntracked: true})

	if result.Applied {
		t.Fatalf("clone was reported pruned despite the removal failure: %+v", result)
	}
	if result.Error == "" || !strings.Contains(result.Error, "permission denied") {
		t.Fatalf("result = %+v, want the removal failure recorded", result)
	}
}

func TestRPCovWriteArchiveCleanReceiptReportsAnUnwritableDirectory(t *testing.T) {
	home := isolateWBHome(t)
	directory := filepath.Join(home, "reports", "archive-clean")
	mustMkdirAll(t, directory)
	rpCovRestrict(t, directory, 0o500)

	if _, err := writeArchiveCleanReceipt("/p", archiveCleanReceipt{Repository: "acme/widgets"}); err == nil {
		t.Fatal("writeArchiveCleanReceipt reported success into a directory it cannot write")
	}
}

func TestRPCovRemoveExactPathAtReportsADirectoryThatCannotBeOpened(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	// The manifest must describe the exact on-disk mode, so the entry is
	// recorded after the directory is made unopenable.
	rpCovRestrict(t, locked, 0o000)
	var stat unix.Stat_t
	if err := unix.Lstat(locked, &stat); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]UntrackedEntry{"locked": {
		Path: "locked", Kind: "directory", Size: stat.Size,
		device: uint64(stat.Dev), inode: uint64(stat.Ino), mode: uint32(stat.Mode),
	}}
	rootFile, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rootFile.Close() }()

	err = removeExactPathAt(rootFile, root, "locked", manifest, 0)
	if err == nil || !strings.Contains(err.Error(), "open untracked directory") {
		t.Fatalf("error = %v, want the unopenable-directory refusal", err)
	}
}

func TestRPCovRemoveExactPathAtReportsUnlinkFailures(t *testing.T) {
	t.Parallel()
	t.Run("file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustWriteFile(t, filepath.Join(root, "sub", "file.txt"), "content\n")
		rpCovRestrict(t, filepath.Join(root, "sub"), 0o500)
		planned, err := planUntracked(root, []string{"sub"})
		if err != nil {
			t.Fatal(err)
		}
		manifest := map[string]UntrackedEntry{}
		for _, entry := range planned {
			manifest[entry.Path] = entry
		}
		rootFile, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rootFile.Close() }()

		err = removeExactPathAt(rootFile, root, "sub", manifest, 0)
		if err == nil || !strings.Contains(err.Error(), "remove authorised untracked file sub/file.txt") {
			t.Fatalf("error = %v, want the unlink failure", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustMkdirAll(t, filepath.Join(root, "sub", "inner"))
		rpCovRestrict(t, filepath.Join(root, "sub"), 0o500)
		planned, err := planUntracked(root, []string{"sub"})
		if err != nil {
			t.Fatal(err)
		}
		manifest := map[string]UntrackedEntry{}
		for _, entry := range planned {
			manifest[entry.Path] = entry
		}
		rootFile, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rootFile.Close() }()

		err = removeExactPathAt(rootFile, root, "sub", manifest, 0)
		if err == nil || !strings.Contains(err.Error(), "remove authorised untracked directory sub/inner") {
			t.Fatalf("error = %v, want the directory-removal failure", err)
		}
	})
}
