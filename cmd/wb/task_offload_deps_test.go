package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/taskoffload"
	"github.com/sneat-dev/wb/internal/taskrun"
)

// The genuine task factory delegates pickup to the actual session Move service.
// Its projects root is read during execution rather than snapshotted here.
func TestDefaultTaskOffloadDependenciesLaunchDelegatesToSessionMove(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	deps := taskrun.DefaultDependencies(nil)
	store, err := deps.Store(root)
	if err != nil {
		t.Fatal(err)
	}
	record := taskoffload.Record{SchemaVersion: 1, TaskID: "task-abc123", Task: "review-auth", WorktreeDir: filepath.Join(root, "no-such-worktree"), Repository: "acme/app", Status: taskoffload.StatusParked, CreatedAt: time.Unix(10, 0).UTC()}
	if err := store.Save(record, "Review auth."); err != nil {
		t.Fatal(err)
	}
	inv := testInvocation(t, t.TempDir())
	command := newTaskCmd(inv)
	inv.projectsRoot = root
	var out, diagnostics bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&diagnostics)
	command.SetArgs([]string{"pickup", record.TaskID})
	command.SilenceErrors = true
	command.SilenceUsage = true
	err = command.ExecuteContext(t.Context())
	var unconfigured *remotestate.UnconfiguredError
	if !errors.As(err, &unconfigured) || !strings.HasPrefix(err.Error(), "resolve this machine for a local move: ") {
		t.Fatalf("want actual Move-stage machine configuration refusal, got %T: %v", err, err)
	}
	if out.Len() != 0 {
		t.Fatalf("failed Move wrote stdout: %q", out.String())
	}
	loaded, brief, err := store.Load(record.TaskID)
	if err != nil || loaded.Status != taskoffload.StatusParked || brief != "Review auth." {
		t.Fatalf("failed real Move changed saved task: %#v %q %v", loaded, brief, err)
	}
}
