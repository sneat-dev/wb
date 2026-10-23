package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

// TestPersistRecordInjectedHonoursInjectedFailures covers persistRecord's
// fixed-name-temp write path. No leftover-temp assertion runs for a write,
// sync or close failure: the original code never removed the ".tmp" file on
// those failures (it is a fixed name reused by the next attempt, not a
// unique per-call temp name), so a survives-after-failure ".tmp" file is
// this call shape's pre-existing behaviour, unchanged by this migration.
// Only a create failure guarantees no ".tmp" file exists afterward.
func TestPersistRecordInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepWrite, filewrite.StepSync,
		filewrite.StepClose, filewrite.StepRename,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			service := &Service{directory: dir}
			item := &record{Schema: QueueSchema, Operation: &daemonv1.Operation{SchemaVersion: QueueSchema, OperationId: "pr7-" + string(step)}}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR7}
			if err := service.persistRecordInjected(item, inj); !errors.Is(err, errBoomPR7) {
				t.Fatalf("persistRecordInjected(%s failure) = %v, want errBoomPR7", step, err)
			}
			path := filepath.Join(dir, item.Operation.OperationId+".json")
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("failed persist published a visible record: %v", statErr)
			}
			if step == filewrite.StepOpenOrCreate {
				if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
					t.Fatalf("failed create left a visible temp file: %v", statErr)
				}
			}
		})
	}
}

func TestPersistRecordInjectedPublishesReadableJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	service := &Service{directory: dir}
	item := &record{Schema: QueueSchema, Operation: &daemonv1.Operation{SchemaVersion: QueueSchema, OperationId: "pr7-happy"}}
	if err := service.persistRecordInjected(item, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, item.Operation.OperationId+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persistRecordInjected did not publish: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("persistRecordInjected left a temp file behind on success: %v", err)
	}
}
