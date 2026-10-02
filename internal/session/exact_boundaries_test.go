package session

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func TestExactLookupObservationFailures(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"directory stat", "directory type", "record stat", "first read", "oversized read", "size changed", "rewind", "verification read", "second stat", "content changed"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir, record, prior := registeredBoundarySession(t)
			native := nativeExactRecordIO()
			access := native
			stats, reads := 0, 0
			failure := errors.New("observation failed")
			var held *os.File
			access.stat = func(file *os.File, stat *unix.Stat_t) error {
				stats++
				held = file
				if (name == "directory stat" && stats == 1) || (name == "record stat" && stats == 2) || (name == "second stat" && stats == 3) {
					return failure
				}
				if err := native.stat(file, stat); err != nil {
					return err
				}
				if name == "directory type" && stats == 1 {
					stat.Mode = stat.Mode&^unix.S_IFMT | unix.S_IFREG
				}
				return nil
			}
			access.read = func(file *os.File) ([]byte, error) {
				reads++
				held = file
				if (name == "first read" && reads == 1) || (name == "verification read" && reads == 2) {
					return nil, failure
				}
				if name == "oversized read" && reads == 1 {
					return bytes.Repeat([]byte("x"), maxExactSessionRecordBytes+1), nil
				}
				raw, err := native.read(file)
				if name == "size changed" && reads == 1 && len(raw) > 0 {
					raw = raw[:len(raw)-1]
				}
				if name == "content changed" && reads == 2 && len(raw) > 0 {
					raw[0] ^= 1
				}
				return raw, err
			}
			access.rewind = func(file *os.File) (int64, error) {
				held = file
				if name == "rewind" {
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				}
				return native.rewind(file)
			}
			got, live, err := lookupExactWithIO(dir, record.PID, access)
			if err == nil || live || got.PID != 0 {
				t.Fatalf("lookup = %+v %v %v", got, live, err)
			}
			expected := map[string]string{"directory stat": "inspect exact session directory", "directory type": "not a directory", "record stat": "inspect exact session record", "first read": "read exact session record", "oversized read": "exceeds", "size changed": "size changed", "rewind": "rewind exact session record", "verification read": "verify exact session record", "second stat": "reinspect exact session record", "content changed": "changed while verified"}[name]
			if !strings.Contains(err.Error(), expected) {
				t.Fatalf("error = %v, want %q", err, expected)
			}
			if held == nil {
				t.Fatal("no descriptor observed")
			}
			if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("descriptor leaked: %v", err)
			}
			raw, err := os.ReadFile(recordPath(dir, record.PID))
			if err != nil || !bytes.Equal(raw, prior) {
				t.Fatalf("registration changed: %v", err)
			}
		})
	}
}

func TestExactLookupDetectsNativeConcurrentRecordChanges(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"growth before read", "shrink before read", "rewrite before verification"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			dir, record, _ := registeredBoundarySession(t)
			native := nativeExactRecordIO()
			access := native
			reads := 0
			path := recordPath(dir, record.PID)
			access.read = func(file *os.File) ([]byte, error) {
				reads++
				if (stage == "growth before read" && reads == 1) || (stage == "shrink before read" && reads == 1) || (stage == "rewrite before verification" && reads == 2) {
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					switch stage {
					case "growth before read":
						raw = append(raw, bytes.Repeat([]byte(" "), maxExactSessionRecordBytes)...)
					case "shrink before read":
						raw = raw[:len(raw)-1]
					case "rewrite before verification":
						raw[0] = ' '
					}
					if err := os.WriteFile(path, raw, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				return native.read(file)
			}
			_, live, err := lookupExactWithIO(dir, record.PID, access)
			if err == nil || live {
				t.Fatalf("changed record accepted: %v %v", live, err)
			}
			want := map[string]string{"growth before read": "exceeds", "shrink before read": "size changed", "rewrite before verification": "changed while verified"}[stage]
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
