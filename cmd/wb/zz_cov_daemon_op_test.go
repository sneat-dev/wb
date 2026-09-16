package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gitops"
)

func TestCwCovDaemonOperationTerminalAndState(t *testing.T) {
	for state, want := range map[daemonv1.OperationState]bool{
		daemonv1.OperationState_OPERATION_STATE_SUCCEEDED:         true,
		daemonv1.OperationState_OPERATION_STATE_FAILED:            true,
		daemonv1.OperationState_OPERATION_STATE_CANCELLED:         true,
		daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED: true,
		daemonv1.OperationState_OPERATION_STATE_QUEUED:            false,
		daemonv1.OperationState_OPERATION_STATE_RUNNING:           false,
	} {
		if got := daemonOperationTerminal(state); got != want {
			t.Errorf("daemonOperationTerminal(%v) = %t, want %t", state, got, want)
		}
	}
	if got := daemonOperationState(daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED); got != "recovery_required" {
		t.Errorf("daemonOperationState = %q", got)
	}
	if got := daemonOperationState(daemonv1.OperationState_OPERATION_STATE_QUEUED); got != "queued" {
		t.Errorf("daemonOperationState(queued) = %q", got)
	}
}

func TestCwCovWriteDaemonOperationTextAndJSON(t *testing.T) {
	operation := &daemonv1.Operation{
		OperationId: "wbo-1", IdempotencyKey: "key-1",
		State: daemonv1.OperationState_OPERATION_STATE_SUCCEEDED, Cursor: "c-1",
		CommandKind: "raw", ArgsSha256: "abc", ArgumentCount: 2, CpuUnits: 4,
		ExitCode: 0, SubmittedUnixMilli: 1, StartedUnixMilli: 2, FinishedUnixMilli: 3,
		QueueWaitMilliseconds: 1, WallMilliseconds: 1,
		StdoutTail: []byte("hello\n"), StderrTail: []byte("warning\n"),
		TargetWorkerId: "worker-1",
	}
	var out bytes.Buffer
	if err := writeDaemonOperation(&out, "text", operation); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"operation wbo-1: state=succeeded, cursor=c-1, cpu_units=4",
		", target_worker=worker-1", ", exit_code=0",
		"hello", "stderr:", "warning",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("daemon operation text missing %q:\n%s", want, text)
		}
	}

	// An unfinished operation with no worker and no tails prints one short line.
	out.Reset()
	if err := writeDaemonOperation(&out, "text", &daemonv1.Operation{
		OperationId: "wbo-2", State: daemonv1.OperationState_OPERATION_STATE_QUEUED, Cursor: "c-2", CpuUnits: 1,
	}); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(out.String())
	if line != "operation wbo-2: state=queued, cursor=c-2, cpu_units=1" {
		t.Fatalf("queued operation line = %q", line)
	}

	out.Reset()
	if err := writeDaemonOperation(&out, "json", operation); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		OperationID    string `json:"operation_id"`
		State          string `json:"state"`
		TargetWorkerID string `json:"target_worker_id"`
		StdoutTail     string `json:"stdout_tail"`
		StderrTail     string `json:"stderr_tail"`
		ArgumentCount  int    `json:"argument_count"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("daemon operation JSON: %v\n%s", err, out.String())
	}
	if payload.OperationID != "wbo-1" || payload.State != "succeeded" || payload.TargetWorkerID != "worker-1" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.StdoutTail != "hello\n" || payload.StderrTail != "warning\n" || payload.ArgumentCount != 2 {
		t.Fatalf("payload tails = %+v", payload)
	}
}

func TestCwCovStatusHiddenNoteAndDetails(t *testing.T) {
	if got := statusHiddenNote(1); !strings.Contains(got, "1 clean repository hidden") || !strings.Contains(got, "include it") {
		t.Errorf("statusHiddenNote(1) = %q", got)
	}
	if got := statusHiddenNote(4); !strings.Contains(got, "4 clean repositories hidden") || !strings.Contains(got, "include them") {
		t.Errorf("statusHiddenNote(4) = %q", got)
	}

	var out strings.Builder
	writeStatusDetails(&out, repositoryStatusInfo{
		Repository: "acme/app",
		Modified:   []string{"a.go"},
		Untracked:  []string{"notes.txt"},
		Conflicted: []string{"merge.go"},
		Stashed:    []string{"stash@{0}"},
	})
	text := out.String()
	for _, want := range []string{
		"acme/app — Modified:", "- `a.go`",
		"acme/app — Untracked:", "- `notes.txt`",
		"acme/app — Conflicted:", "- `merge.go`",
		"acme/app — Stashed:", "- `stash@{0}`",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status details missing %q:\n%s", want, text)
		}
	}

	// Unpushed commits render as a group when there is no branch detail, and
	// as per-branch sections when there is.
	out.Reset()
	writeStatusDetails(&out, repositoryStatusInfo{Repository: "acme/app", Unpushed: []string{"deadbee"}})
	if !strings.Contains(out.String(), "acme/app — Unpushed:") || !strings.Contains(out.String(), "- `deadbee`") {
		t.Errorf("plain unpushed details = %q", out.String())
	}

	out.Reset()
	writeStatusDetails(&out, repositoryStatusInfo{
		Repository: "acme/app",
		UnpushedBranches: []gitops.UnpushedBranch{
			{Branch: "task/one", Worktree: "/tmp/wt/task-one", Commits: []string{"c1", "c2"}},
			{Branch: "task/two"},
		},
	})
	text = out.String()
	for _, want := range []string{
		"acme/app — Unpushed:",
		"- Branch `task/one` in worktree `/tmp/wt/task-one`:",
		"- `c1`", "- `c2`",
		"- Branch `task/two`:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unpushed-branch details missing %q:\n%s", want, text)
		}
	}

	// An empty detail set writes nothing at all.
	out.Reset()
	writeStatusDetails(&out, repositoryStatusInfo{Repository: "acme/clean"})
	if out.Len() != 0 {
		t.Errorf("empty details wrote %q", out.String())
	}
}

func TestCwCovWaitForDaemonOperationStopsOnTerminalState(t *testing.T) {
	// A terminal operation is returned unchanged without any client call.
	operation := &daemonv1.Operation{OperationId: "wbo-3", State: daemonv1.OperationState_OPERATION_STATE_SUCCEEDED}
	var progress bytes.Buffer
	result, err := waitForDaemonOperation(t.Context(), &progress, nil, operation)
	if err != nil {
		t.Fatal(err)
	}
	if result != operation || progress.Len() != 0 {
		t.Fatalf("terminal wait = (%+v, %q)", result, progress.String())
	}
}
