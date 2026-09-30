//go:build e2e && !windows

package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sneat-dev/wb/internal/checkoutmarker"
)

func TestE2EPrepareCanonicalWorktreesRootRefusesReboundPath(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "canonical")
	if err := os.MkdirAll(filepath.Join(root, ".git", "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(root, ".git", "info", "exclude")
	if err := unix.Mkfifo(exclude, 0o600); err != nil {
		t.Fatal(err)
	}
	canonical, err := openCanonicalRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(canonical.close)
	ctx := withCanonicalGitInterceptor(t.Context(), func(_ context.Context, _ []string, _ func() ([]byte, error)) ([]byte, error) {
		return nil, nil
	})
	type result struct {
		path      string
		directory *os.File
		err       error
	}
	done := make(chan result, 1)
	go func() {
		path, directory, openErr := prepareCanonicalWorktreesRoot(ctx, canonical, strings.Repeat("a", 40))
		done <- result{path: path, directory: directory, err: openErr}
	}()
	patterns := fmt.Sprintf("%s\n%s\n", checkoutmarker.ExcludePattern, checkoutmarker.WorktreesExcludePattern)
	writerFD := -1
	fifoPath := exclude
	joined := false
	defer func() {
		released := false
		if writerFD >= 0 {
			_, _ = unix.Write(writerFD, []byte(patterns))
			_ = unix.Close(writerFD)
			writerFD = -1
			released = true
		}
		if joined {
			return
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			select {
			case returned := <-done:
				if returned.directory != nil {
					_ = returned.directory.Close()
				}
				return
			default:
			}
			if !released {
				fd, openErr := unix.Open(fifoPath, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
				if openErr == nil {
					_, _ = unix.Write(fd, []byte(patterns))
					_ = unix.Close(fd)
					released = true // The reader was opened; no further writer retry is needed.
				} else if !errors.Is(openErr, unix.ENXIO) {
					t.Errorf("release exclude reader after early failure: %v", openErr)
					return
				}
			}
			if time.Now().After(deadline) {
				t.Error("canonical root preparation did not finish after bounded FIFO release")
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case returned := <-done:
			joined = true
			if returned.directory != nil {
				_ = returned.directory.Close()
			}
			t.Fatalf("canonical root preparation returned before reading exclude: %v", returned.err)
		default:
		}
		writerFD, err = unix.Open(exclude, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == nil {
			break // A nonblocking writer can open only after the reader has opened the FIFO.
		}
		writerFD = -1
		if !errors.Is(err, unix.ENXIO) || time.Now().After(deadline) {
			t.Fatalf("wait for exclude reader: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	moved := filepath.Join(parent, "moved-canonical")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	fifoPath = filepath.Join(moved, ".git", "info", "exclude")
	if n, err := unix.Write(writerFD, []byte(patterns)); err != nil || n != len(patterns) {
		t.Fatalf("release exclude reader: wrote %d/%d bytes: %v", n, len(patterns), err)
	}
	if err := unix.Close(writerFD); err != nil {
		t.Fatal(err)
	}
	writerFD = -1
	select {
	case returned := <-done:
		joined = true
		if returned.directory != nil {
			_ = returned.directory.Close()
		}
		if returned.path != "" || !strings.Contains(fmt.Sprint(returned.err), "canonical .worktrees root changed before creation") {
			t.Fatalf("rebound canonical path: path=%q err=%v", returned.path, returned.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canonical root preparation did not reject rebound path")
	}
}

func TestE2EPrepareCanonicalWorktreesRootRefusesSymlinkedGitInfo(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "canonical")
	info := filepath.Join(root, ".git", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := openCanonicalRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	parked := filepath.Join(root, ".git", "info-parked")
	if err := os.Rename(info, parked); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, info); err != nil {
		t.Fatal(err)
	}
	ctx := withCanonicalGitInterceptor(t.Context(), func(_ context.Context, _ []string, _ func() ([]byte, error)) ([]byte, error) {
		return nil, nil
	})
	_, directory, err := prepareCanonicalWorktreesRoot(ctx, canonical, strings.Repeat("a", 40))
	if directory != nil {
		_ = directory.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "open canonical Git info directory") || directory != nil {
		t.Fatalf("symlinked canonical Git info: directory=%v err=%v", directory, err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "exclude")); !os.IsNotExist(err) {
		t.Fatalf("symlinked Git info wrote outside canonical: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".worktrees")); !os.IsNotExist(err) {
		t.Fatalf("symlinked Git info created .worktrees: %v", err)
	}
}
