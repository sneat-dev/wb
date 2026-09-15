package taskoffload

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tailCovRecord is a valid record the failure-path tests start from and then
// break in exactly one way, so each assertion names a single reason a parked
// task was refused.
func tailCovRecord(taskID string) Record {
	return Record{
		SchemaVersion: schemaVersion,
		TaskID:        taskID,
		Task:          "review-auth",
		WorktreeDir:   "/tmp/tailcov-review",
		Repository:    "acme/app",
		Status:        StatusParked,
		CreatedAt:     time.Unix(10, 0).UTC(),
	}
}

// tailCovWriteRaw lays down the on-disk layout a previous WB wrote, bypassing
// Store.Save, so the tests can reproduce a store that has been corrupted or
// hand-edited between runs.
func tailCovWriteRaw(t *testing.T, root, taskID, recordJSON, contextBody string) {
	t.Helper()
	dir := filepath.Join(root, taskID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if recordJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, recordFileName), []byte(recordJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if contextBody != "" {
		if err := os.WriteFile(filepath.Join(dir, contextFileName), []byte(contextBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTailCovStoreRejectsASchemaVersionItCannotRead pins forward compatibility:
// a store written by a newer WB must be refused rather than silently
// misinterpreted by an older binary.
func TestTailCovStoreRejectsASchemaVersionItCannotRead(t *testing.T) {
	store := NewStore(t.TempDir())
	record := tailCovRecord("task-future")
	record.SchemaVersion = schemaVersion + 1

	err := store.Save(record, "continue the review")
	if err == nil {
		t.Fatal("Save accepted a record with an unsupported schema_version")
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("Save error = %v, want it to name schema_version", err)
	}
	if _, statErr := os.Stat(filepath.Join(store.Root, record.TaskID)); !os.IsNotExist(statErr) {
		t.Fatalf("a refused record must not persist a directory: %v", statErr)
	}
}

// TestTailCovStoreRejectsAnIncompleteRecord covers each field validation
// refuses to lose: an empty continuation task, an empty worktree, and a task ID
// that would escape the store root. All three make the parked task unresumable.
func TestTailCovStoreRejectsAnIncompleteRecord(t *testing.T) {
	store := NewStore(t.TempDir())

	broken := map[string]func(*Record){
		"missing task id prefix": func(record *Record) { record.TaskID = "not-a-task" },
		"blank task":             func(record *Record) { record.Task = "   " },
		"blank worktree":         func(record *Record) { record.WorktreeDir = "\t" },
	}
	for name, breakIt := range broken {
		t.Run(name, func(t *testing.T) {
			record := tailCovRecord("task-incomplete")
			breakIt(&record)
			err := store.Save(record, "continue the review")
			if err == nil {
				t.Fatalf("Save accepted a record with a %s", name)
			}
			if !strings.Contains(err.Error(), "incomplete") {
				t.Fatalf("Save error = %v, want the incomplete-record refusal", err)
			}
		})
	}
}

// TestTailCovStoreRejectsAnOversizedContinuation is the upper bound of the
// context-size contract: a continuation larger than the documented maximum must
// not be parked, because the offload prompt could never carry it.
func TestTailCovStoreRejectsAnOversizedContinuation(t *testing.T) {
	store := NewStore(t.TempDir())
	err := store.Save(tailCovRecord("task-too-big"), strings.Repeat("x", maxContextBytes+1))
	if err == nil {
		t.Fatal("Save accepted a continuation above maxContextBytes")
	}
	if !strings.Contains(err.Error(), "between 1 and") {
		t.Fatalf("Save error = %v, want the size refusal", err)
	}
}

// TestTailCovStoreSurfacesStorageFailures proves Save reports the filesystem's
// own error instead of claiming success: a store root that is a regular file
// cannot host a task directory, and a task directory the process cannot write
// must fail before any record is reported as parked.
func TestTailCovStoreSurfacesStorageFailures(t *testing.T) {
	t.Run("root is not a directory", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root-file")
		if err := os.WriteFile(root, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		store := NewStore(root)
		if err := store.Save(tailCovRecord("task-root"), "continue"); err == nil {
			t.Fatal("Save succeeded with a regular file as the store root")
		}
	})

	t.Run("task directory is not writable", func(t *testing.T) {
		root := t.TempDir()
		unwritable := filepath.Join(root, "task-locked")
		if err := os.Mkdir(unwritable, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(unwritable, 0o700) })

		store := NewStore(root)
		err := store.Save(tailCovRecord("task-locked"), "continue")
		if err == nil {
			t.Fatal("Save succeeded into a directory with no write permission")
		}
		if !os.IsPermission(err) {
			t.Fatalf("Save error = %v, want a permission error", err)
		}
		if _, statErr := os.Stat(filepath.Join(unwritable, contextFileName)); !os.IsNotExist(statErr) {
			t.Fatalf("a failed save must not leave a context file: %v", statErr)
		}
	})
}

// TestTailCovStoreRefusesIDsOutsideTheNamespace keeps Load from being used as a
// path traversal: only IDs that carry the task- prefix may name a directory.
func TestTailCovStoreRefusesIDsOutsideTheNamespace(t *testing.T) {
	store := NewStore(t.TempDir())
	outside := filepath.Join(store.Root, "..", "elsewhere")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"../elsewhere", "  other-task  ", "task", ""} {
		record, body, err := store.Load(id)
		if err == nil {
			t.Fatalf("Load(%q) = %#v, %q; want the invalid-ID refusal", id, record, body)
		}
		if !strings.Contains(err.Error(), "invalid parked task ID") {
			t.Fatalf("Load(%q) error = %v, want the invalid-ID refusal", id, err)
		}
	}
}

// TestTailCovStoreReportsUnreadableAndMalformedRecords covers every way a
// directory under the store root can fail to be a parked task. Each case is a
// distinct filesystem state a crash, an editor, or a partial copy can leave
// behind, and each must be reported rather than half-loaded.
func TestTailCovStoreReportsUnreadableAndMalformedRecords(t *testing.T) {
	valid := `{"schema_version":1,"task_id":"task-ok","task":"review-auth","worktree_dir":"/tmp/review","status":"parked","created_at":"1970-01-01T00:00:10Z"}`

	t.Run("record file missing", func(t *testing.T) {
		store := NewStore(t.TempDir())
		_, _, err := store.Load("task-never-saved")
		if !os.IsNotExist(err) {
			t.Fatalf("Load error = %v, want the not-exist error", err)
		}
	})

	t.Run("record file is not JSON", func(t *testing.T) {
		root := t.TempDir()
		tailCovWriteRaw(t, root, "task-corrupt", "{not json", "continue")
		_, _, err := NewStore(root).Load("task-corrupt")
		if err == nil {
			t.Fatal("Load accepted a record file that is not JSON")
		}
		var syntaxErr *json.SyntaxError
		if !errors.As(err, &syntaxErr) {
			t.Fatalf("Load error = %#v (%v), want a *json.SyntaxError", err, err)
		}
	})

	t.Run("context file missing", func(t *testing.T) {
		root := t.TempDir()
		tailCovWriteRaw(t, root, "task-no-context", valid, "")
		_, _, err := NewStore(root).Load("task-no-context")
		if !os.IsNotExist(err) {
			t.Fatalf("Load error = %v, want the missing-context not-exist error", err)
		}
	})

	t.Run("record is incomplete", func(t *testing.T) {
		root := t.TempDir()
		incomplete := `{"schema_version":1,"task_id":"task-empty","task":"","worktree_dir":"/tmp/review","status":"parked"}`
		tailCovWriteRaw(t, root, "task-empty", incomplete, "continue")
		_, _, err := NewStore(root).Load("task-empty")
		if err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("Load error = %v, want the incomplete-record refusal", err)
		}
	})
}

// TestTailCovStoreSurfacesAnUnencodableRecord covers the serialization step:
// a timestamp outside the range RFC 3339 can express is refused rather than
// written as a truncated or silently corrected record, and the refusal happens
// before the record file is created.
func TestTailCovStoreSurfacesAnUnencodableRecord(t *testing.T) {
	store := NewStore(t.TempDir())
	record := tailCovRecord("task-out-of-range")
	record.CreatedAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)

	err := store.Save(record, "continue the review")
	if err == nil {
		t.Fatal("Save encoded a timestamp RFC 3339 cannot represent")
	}
	if !strings.Contains(err.Error(), "year outside of range") {
		t.Fatalf("Save error = %v, want the timestamp range refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(store.Root, record.TaskID, recordFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("an unencodable record must not be half-written: %v", statErr)
	}
}

// TestTailCovLoadRoundTripsTheExactBytesItSaved guards the read side of the
// contract the store exists for: the continuation body comes back byte for byte
// (trailing newlines included), because it is handed to another agent verbatim.
func TestTailCovLoadRoundTripsTheExactBytesItSaved(t *testing.T) {
	store := NewStore(t.TempDir())
	record := tailCovRecord("task-exact")
	body := "Continue the review.\n\n  * keep the indentation\n"

	if err := store.Save(record, body); err != nil {
		t.Fatal(err)
	}
	loaded, got, err := store.Load("  " + record.TaskID + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != body {
		t.Fatalf("Load body = %q, want the saved bytes %q", got, body)
	}
	if loaded != record {
		t.Fatalf("Load record = %#v, want %#v", loaded, record)
	}
}
