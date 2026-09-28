package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorktreePathsFromPorcelainPreservesRawRecords(t *testing.T) {
	t.Parallel()
	output := "worktree /tmp/with space\nHEAD abc\nbranch refs/heads/main\n\nworktree /tmp/with space\nworktree /tmp/other\r\nworktree \n"
	want := []string{"/tmp/with space", "/tmp/with space", "/tmp/other\r", ""}
	if got := worktreePathsFromPorcelain(output); !reflect.DeepEqual(got, want) {
		t.Fatalf("worktree paths = %#v, want %#v", got, want)
	}
}

func newQuarantineCheckout(t *testing.T) (string, *os.File, *os.File) {
	t.Helper()
	root := t.TempDir()
	checkoutPath := filepath.Join(root, "checkout")
	if err := os.Mkdir(checkoutPath, 0o700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	checkout, err := os.Open(checkoutPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = checkout.Close() })
	return root, parent, checkout
}

func TestQuarantineDirectoryEntryNamedReturnsMovedHandle(t *testing.T) {
	t.Parallel()
	root, parent, checkout := newQuarantineCheckout(t)
	checkoutPath := filepath.Join(root, "checkout")
	moved, name, err := quarantineDirectoryEntryNamed(parent, "checkout", checkout, ".retired-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = moved.Close() })
	if !strings.HasPrefix(name, ".retired-") || len(name) != len(".retired-")+32 {
		t.Fatalf("retirement name = %q", name)
	}
	if _, err := moved.Stat(); err != nil {
		t.Fatalf("returned moved handle is closed: %v", err)
	}
	if _, err := os.Stat(checkoutPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old checkout path remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, name)); err != nil {
		t.Fatalf("retired directory missing: %v", err)
	}
}

func TestQuarantineSecureStageCheckoutMovesAndRejectsIdentityDrift(t *testing.T) {
	t.Parallel()
	for _, stale := range []bool{false, true} {
		root, parent, checkout := newQuarantineCheckout(t)
		checkoutPath := filepath.Join(root, "checkout")
		if stale {
			if err := os.Rename(checkoutPath, filepath.Join(root, "original")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(checkoutPath, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		err := quarantineSecureStageCheckout(parent, checkout)
		if stale {
			if err == nil || !strings.Contains(err.Error(), "quarantine staged checkout: directory entry checkout changed after inspection; refusing mutation") {
				t.Fatalf("identity drift error = %v", err)
			}
			if _, err := os.Stat(checkoutPath); err != nil {
				t.Fatalf("successor checkout was moved: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(checkoutPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old checkout path remains: %v", err)
		}
		matches, err := filepath.Glob(filepath.Join(root, ".wb-retired-checkout-*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("retired checkouts = %v, error = %v", matches, err)
		}
	}
}
