package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func registeredBoundarySession(t *testing.T) (string, Record, []byte) {
	t.Helper()
	dir := t.TempDir()
	record, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-boundary", Machine: "test-machine"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(recordPath(dir, record.PID))
	if err != nil {
		t.Fatal(err)
	}
	return dir, record, raw
}

func TestRegistrationPublicationFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("hostname unavailable")
	for _, tc := range []struct {
		name     string
		hostname func() (string, error)
		want     string
	}{
		{"hostname error", func() (string, error) { return "", failure }, "resolve session machine"},
		{"empty hostname", func() (string, error) { return " \t", nil }, "hostname is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			_, err := registerWithMachineAndMode(dir, Record{PID: os.Getpid()}, tc.hostname, os.Chmod)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			if _, err := os.Stat(recordPath(dir, os.Getpid())); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected registration: %v", err)
			}
		})
	}
	t.Run("invalid timestamp preserves registration", func(t *testing.T) {
		t.Parallel()
		dir, record, prior := registeredBoundarySession(t)
		record.StartedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		if _, err := Register(dir, record); err == nil {
			t.Fatal("invalid year accepted")
		}
		got, err := os.ReadFile(recordPath(dir, record.PID))
		if err != nil || !bytes.Equal(got, prior) {
			t.Fatalf("registration changed: %v", err)
		}
	})
	t.Run("mode failure after native write", func(t *testing.T) {
		t.Parallel()
		dir, record, _ := registeredBoundarySession(t)
		moved := filepath.Join(dir, "retained.json")
		_, err := registerWithMachineAndMode(dir, record, os.Hostname, func(path string, mode os.FileMode) error {
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			return os.Chmod(path, mode)
		})
		if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "set session record mode") {
			t.Fatalf("error = %v", err)
		}
		raw, err := os.ReadFile(moved)
		if err != nil {
			t.Fatal(err)
		}
		var got Record
		if err := json.Unmarshal(raw, &got); err != nil || got.WBSessionID != record.WBSessionID {
			t.Fatalf("written record lost: %v", err)
		}
	})
}

func TestLifecycleTimestampFailuresPreserveHistory(t *testing.T) {
	t.Parallel()
	for _, resumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "park", true: "resume"}[resumed], func(t *testing.T) {
			t.Parallel()
			dir, record, prior := registeredBoundarySession(t)
			at := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			var err error
			var target string
			if resumed {
				if _, err := MarkParked(dir, record.PID, "parked"); err != nil {
					t.Fatal(err)
				}
				target = resumedMarkerPath(dir, record.WBSessionID)
				_, err = markResumedAtWithSync(dir, record.PID, "parked", "wbs-successor", nil, func() time.Time { return at }, syncDirectory)
			} else {
				target = parkedMarkerPath(dir, record.WBSessionID)
				_, err = markParkedAt(dir, record.PID, "parked", nil, func() time.Time { return at })
			}
			if err == nil {
				t.Fatal("invalid timestamp accepted")
			}
			if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("marker published: %v", err)
			}
			got, err := os.ReadFile(recordPath(dir, record.PID))
			if err != nil || !bytes.Equal(got, prior) {
				t.Fatalf("history changed: %v", err)
			}
		})
	}
}

func TestResumedPublicationBoundaries(t *testing.T) {
	t.Parallel()
	t.Run("marker directory unavailable", func(t *testing.T) {
		t.Parallel()
		dir, record, prior := registeredBoundarySession(t)
		if _, err := MarkParked(dir, record.PID, "parked"); err != nil {
			t.Fatal(err)
		}
		lifecycle := filepath.Join(dir, "lifecycle")
		inj := &filewrite.Injector{Step: filewrite.StepClose, Hook: func() {
			if err := os.Rename(lifecycle, lifecycle+"-retained"); err != nil {
				t.Fatal(err)
			}
		}}
		_, err := markResumedInjected(dir, record.PID, "parked", "wbs-successor", inj)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(lifecycle+"-retained", record.WBSessionID+".resumed.json")); err != nil {
			t.Fatalf("published marker lost: %v", err)
		}
		got, err := os.ReadFile(recordPath(dir, record.PID))
		if err != nil || !bytes.Equal(got, prior) {
			t.Fatalf("history changed: %v", err)
		}
	})
	t.Run("directory sync failure", func(t *testing.T) {
		t.Parallel()
		dir, record, prior := registeredBoundarySession(t)
		if _, err := MarkParked(dir, record.PID, "parked"); err != nil {
			t.Fatal(err)
		}
		failure := errors.New("directory sync failure")
		var held *os.File
		_, err := markResumedAtWithSync(dir, record.PID, "parked", "wbs-successor", nil, time.Now, func(file *os.File) error { held = file; return failure })
		if !errors.Is(err, failure) {
			t.Fatalf("error = %v", err)
		}
		if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("directory leaked: %v", err)
		}
		if marker, ok := readResumedMarker(dir, record.WBSessionID); !ok || marker.SuccessorWBSessionID != "wbs-successor" {
			t.Fatal("published marker lost")
		}
		got, err := os.ReadFile(recordPath(dir, record.PID))
		if err != nil || !bytes.Equal(got, prior) {
			t.Fatalf("history changed: %v", err)
		}
	})
	t.Run("identical concurrent publication", func(t *testing.T) {
		t.Parallel()
		dir, record, prior := registeredBoundarySession(t)
		if _, err := MarkParked(dir, record.PID, "parked"); err != nil {
			t.Fatal(err)
		}
		at := time.Now().UTC()
		marker := resumedLifecycleMarker{SchemaVersion: 1, WBSessionID: record.WBSessionID, ParkedSessionID: "parked", SuccessorWBSessionID: "wbs-successor", At: at}
		raw, err := json.MarshalIndent(marker, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, '\n')
		inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Hook: func() {
			if err := os.WriteFile(resumedMarkerPath(dir, record.WBSessionID), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}}
		got, err := markResumedAtWithSync(dir, record.PID, "parked", "wbs-successor", inj, func() time.Time { return at }, syncDirectory)
		if err != nil || got.Lifecycle != "resumed" {
			t.Fatalf("retry = %+v, %v", got, err)
		}
		stored, err := os.ReadFile(resumedMarkerPath(dir, record.WBSessionID))
		if err != nil || !bytes.Equal(stored, raw) {
			t.Fatalf("concurrent marker changed: %v", err)
		}
		stored, err = os.ReadFile(recordPath(dir, record.PID))
		if err != nil || !bytes.Equal(stored, prior) {
			t.Fatalf("history changed: %v", err)
		}
	})
}

func TestResumedMarkerDirectoryReplacementIsRefused(t *testing.T) {
	t.Parallel()
	dir, record, prior := registeredBoundarySession(t)
	if _, err := MarkParked(dir, record.PID, "parked"); err != nil {
		t.Fatal(err)
	}
	_, err := markResumedWithIO(dir, record.PID, "parked", "wbs-successor", nil, time.Now, syncDirectory, func(path string, mode os.FileMode) error {
		if err := os.Rename(path, path+"-retained"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		return os.MkdirAll(path, mode)
	})
	if err == nil {
		t.Fatal("directory replacement accepted")
	}
	got, err := os.ReadFile(recordPath(dir, record.PID))
	if err != nil || !bytes.Equal(got, prior) {
		t.Fatalf("history changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lifecycle-retained", record.WBSessionID+".resumed.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resumed marker published: %v", err)
	}
}
