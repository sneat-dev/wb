package githubobserver

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetReportsLockReleaseFailureWithSuccessfulResponse(t *testing.T) {
	t.Parallel()
	failure := errors.New("lock release failed")
	observer := &Observer{StateDir: t.TempDir(), Run: func(context.Context, string, ...string) commandResult {
		return commandResult{Stdout: []byte("HTTP/2 200 OK\n\n{}")}
	}}
	released := 0
	response, err := observer.getWithLock(context.Background(), gpCovRequest("a", "repos/acme/app"), func(string) (func() error, error) { return func() error { released++; return failure }, nil })
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "release GitHub observer lock") || released != 1 || string(response.Body) != "{}" {
		t.Fatalf("response=%+v,error=%v,releases=%d", response, err, released)
	}
}

func TestCacheSerializationFailurePublishesNothing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "entry.json")
	err := writeCacheEntryInjected(path, &cacheEntry{ObservedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}, nil)
	if err == nil || !strings.Contains(err.Error(), "encode GitHub observer cache") {
		t.Fatalf("serialization error=%v", err)
	}
	entries, readErr := os.ReadDir(filepath.Dir(path))
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed serialization left %v,%v", entries, readErr)
	}
}

func TestLockClosesDescriptorOnAcquireAndReleaseFailures(t *testing.T) {
	t.Parallel()
	for _, operation := range []int{unix.LOCK_EX, unix.LOCK_UN} {
		t.Run(map[int]string{unix.LOCK_EX: "acquire", unix.LOCK_UN: "release"}[operation], func(t *testing.T) {
			t.Parallel()
			failure := errors.New("flock failure")
			descriptor := -1
			closed := -1
			closeCalls := 0
			unlock, err := acquireLockWithOps(filepath.Join(t.TempDir(), "lock"), func(fd, op int) error {
				descriptor = fd
				if op == operation {
					return failure
				}
				return nil
			}, func(fd int) error {
				closed = fd
				closeCalls++
				err := unix.Close(fd)
				if err != nil {
					t.Errorf("close descriptor: %v", err)
				}
				return err
			})
			if operation == unix.LOCK_UN {
				if err != nil {
					t.Fatal(err)
				}
				err = unlock()
			} else if unlock != nil {
				t.Fatal("failed acquisition returned unlock")
			}
			if !errors.Is(err, failure) {
				t.Fatalf("failure=%v", err)
			}
			if closed != descriptor || closeCalls != 1 {
				t.Fatalf("descriptor=%d,closed=%d,calls=%d", descriptor, closed, closeCalls)
			}
		})
	}
}
