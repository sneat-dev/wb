package streams

import (
	"errors"
	"github.com/sneat-dev/wb/internal/filewrite"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEventAppendPreservesBytesOnEncodingAndWriteFailure(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"encode", "write", "lock"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "events.jsonl")
			before := []byte("retained evidence\n")
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			log := FileEventLog{Path: path}
			event := Event{Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			var owned *os.File
			open := func(path string, mode os.FileMode, inj *filewrite.Injector) (*os.File, error) {
				f, err := filewrite.OpenAppend(path, mode, inj)
				owned = f
				if err == nil && stage == "lock" {
					if err := f.Close(); err != nil {
						t.Fatal(err)
					}
				}
				return f, err
			}
			var inj *filewrite.Injector
			cause := errors.New("append denied")
			if stage == "encode" {
				event.Timestamp = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			if stage == "write" {
				inj = &filewrite.Injector{Step: filewrite.StepWrite, Err: cause}
			}
			err := log.appendInjected(event, inj, open)
			if err == nil || !strings.Contains(err.Error(), map[string]string{"encode": "encode stream event", "write": "append stream event", "lock": "lock stream event"}[stage]) {
				t.Fatalf("error=%v", err)
			}
			if stage == "write" && !errors.Is(err, cause) {
				t.Fatalf("cause lost: %v", err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != string(before) {
				t.Fatalf("bytes=%q error=%v", after, readErr)
			}
			if owned != nil {
				if _, err := owned.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("owned descriptor still live: %v", err)
				}
			}
		})
	}
}

func TestStoreLockClosesDescriptorWhenNativeFlockFails(t *testing.T) {
	t.Parallel()
	for _, wholeStore := range []bool{false, true} {
		t.Run(map[bool]string{false: "stream", true: "store"}[wholeStore], func(t *testing.T) {
			t.Parallel()
			store := OpenAt(filepath.Join(t.TempDir(), "streams"))
			var owned *os.File
			open := func(path string, flags int, mode os.FileMode) (*os.File, error) {
				f, err := os.OpenFile(path, flags, mode)
				owned = f
				if err == nil {
					if err := f.Close(); err != nil {
						t.Fatal(err)
					}
				}
				return f, err
			}
			var release func()
			var err error
			if wholeStore {
				release, err = store.lockStoreWithOpen(open)
			} else {
				release, err = store.lockWithOpen("fixture", open)
			}
			if err == nil || release != nil || !strings.Contains(err.Error(), "lock stream") {
				t.Fatalf("release nil=%v error=%v", release == nil, err)
			}
			if owned == nil {
				t.Fatal("native open not attempted")
			}
			if _, err := owned.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("descriptor live: %v", err)
			}
		})
	}
}
