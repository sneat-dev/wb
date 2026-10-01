package sessionpark

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/unixcompat"
)

func TestFindBySourcePreservesDirectoryReadFailure(t *testing.T) {
	t.Parallel()
	store, bundle := spCovCreatedStore(t)
	var owned *os.File
	_, found, err := store.findBySourceWithEntries(bundle.Source.WBSessionID, func(root *os.File) ([]os.DirEntry, error) {
		owned = root
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		return readDirectoryEntries(root)
	})
	if found || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("find = %t, %v", found, err)
	}
	if _, err := owned.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("root retained: %v", err)
	}
}

func TestReadBoundedArtifactRefusesNativeReadRaces(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"closed", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "artifact")
			if err := os.WriteFile(path, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			raw, err := readBoundedRegularWithRead(f, 100, func(r io.Reader) ([]byte, error) {
				if mode == "closed" {
					if err := f.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Truncate(path, 0); err != nil {
						t.Fatal(err)
					}
				}
				return io.ReadAll(r)
			})
			if err == nil || raw != nil {
				t.Fatalf("changed read = %q, %v", raw, err)
			}
		})
	}
}

func TestPrivateArtifactsRefuseWrongModesAndNonRegularFiles(t *testing.T) {
	t.Parallel()
	root := spCovStoreRoot(t)
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	directory := os.NewFile(uintptr(fd), root)
	t.Cleanup(func() { _ = directory.Close() })
	if _, err := readPrivateFile(directory, 100); err == nil {
		t.Fatal("directory accepted as artifact")
	}
	if err := os.Mkdir(filepath.Join(root, "public"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := openPrivateDirectoryAt(directory, "public"); err == nil {
		t.Fatal("public directory accepted")
	}
	if _, err := readPrivateRegularAt(directory, "missing", 100); err == nil {
		t.Fatal("missing private artifact accepted")
	}
}

func TestNeutralLaunchRootSyncFailures(t *testing.T) {
	t.Parallel()
	for _, name := range []string{LocalNeutralDirName, "park-test"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bundle := testBundle(t)
			bundle.Worktrees = nil
			store := NewStore(t.TempDir())
			if _, err := store.Create(bundle); err != nil {
				t.Fatal(err)
			}
			lock := spCovAcquire(t, store, bundle.ParkedSessionID)
			if _, _, err := store.PrepareLocalUnderLock(lock, time.Unix(100, 0)); err != nil {
				t.Fatal(err)
			}
			refused := errors.New("neutral durability refused")
			path, err := store.localLaunchRootUnderLock(lock, &filewrite.Injector{Step: filewrite.StepDirSync, Name: name, Err: refused}, unix.Mkdirat)
			if path != "" || !errors.Is(err, refused) {
				t.Fatalf("neutral root = %q, %v", path, err)
			}
			if !lock.HeldForSession(store.Root, bundle.ParkedSessionID, "") {
				t.Fatal("sync failure discarded source authority")
			}
		})
	}
}

func TestResumeFenceWaitsOrCancels(t *testing.T) {
	t.Parallel()
	if err := waitResumeFence(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitResumeFence(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("fence = %v", err)
	}
}
