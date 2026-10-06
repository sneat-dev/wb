package runexec

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryReadsManagedWorktreeAndRejectsUnmanagedDirectory(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	testenv.Git(t, root, "init", "-b", "main")
	manifest := worktrees.Manifest{Version: 1, EffortID: "cw-deps", EffortKind: worktrees.EffortKindFeature, Repository: "acme/app", Worktree: root, Branch: "cw-deps", Base: "main", BaseSHA: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(), RunID: "run-1", ClaimID: strings.Repeat("b", 64), Provenance: worktrees.ProvenanceCreated}
	if err := worktrees.WriteManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	started := now.Add(-time.Hour)
	events := []runlog.Event{{SchemaVersion: runlog.EventSchemaVersion, Timestamp: started, OperationID: "wbo-1", State: "requested", Kind: "go test"}, {SchemaVersion: runlog.EventSchemaVersion, Timestamp: started.Add(time.Minute), OperationID: "wbo-1", State: "succeeded", Kind: "go test", DurationMS: 1000, UserCPUMS: 10, SystemCPUMS: 5}, {SchemaVersion: runlog.EventSchemaVersion, Timestamp: started.Add(2 * time.Minute), OperationID: "wbo-2", State: "failed", Kind: "go build"}}
	var lines bytes.Buffer
	for _, event := range events {
		if err := json.NewEncoder(&lines).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, ".wb", "local", "run", "events.jsonl")
	writeFixture(t, path, lines.String())
	// Parent owns immutable manifest/events; each read has independent operation
	// state. ReadCurrent never writes this fixture.
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ops := HistoryOperations{Getwd: func() (string, error) { return root, nil }, Read: runlog.ReadCurrent, Now: func() time.Time { return now }}
			result, err := ops.Run(context.Background(), HistoryRequest{Days: 14})
			if err != nil || result.Summary.Operations != 2 || result.Summary.Failed != 1 || result.Path != path {
				t.Fatalf("summary=%+v err=%v", result, err)
			}
			if len(result.Summary.Kinds) != 2 {
				t.Fatal(result.Summary.Kinds)
			}
		})
	}
	plain := t.TempDir()
	ops := HistoryOperations{Getwd: func() (string, error) { return plain, nil }, Read: runlog.ReadCurrent, Now: time.Now}
	if _, err := ops.Run(context.Background(), HistoryRequest{Days: 7}); err == nil || !strings.Contains(err.Error(), "not inside a managed WB worktree") {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
