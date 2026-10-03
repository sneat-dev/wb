package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/cmdrun"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/runexec"
	"github.com/sneat-dev/wb/internal/runqueue"
	"os"
	"strings"
	"testing"
)

func TestQueueNativeListsRealRunningAndWaitingEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	lease, _, err := runqueue.Acquire(context.Background(), root, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	// Registered PIDs are liveness-checked (a killed process's record must
	// not linger forever), so both the holder and the waiter need a real,
	// live PID here; this test process's own PID qualifies for the test.
	pid := os.Getpid()
	announcement := lease.Announce(runqueue.Participant{PID: pid, Summary: "go build", Worktree: "/w/one"})
	t.Cleanup(announcement.Cleanup)
	waiter := runqueue.Register(root, runqueue.Participant{PID: pid, Summary: "go test", Worktree: "/w/two"})
	t.Cleanup(waiter.Forget)

	execute := func(jsonOutput bool) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		flags := shared.Flags{ProjectsRoot: root}
		command := cmdrun.New(shared.Runtime{Flags: func() shared.Flags { return flags }}, cmdrun.Dependencies{Queue: runexec.Queue})
		command.SetOut(&stdout)
		command.SetErr(&stderr)
		args := []string{"--queue"}
		if jsonOutput {
			args = append(args, "--json")
		}
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatalf("queue: %v stderr=%s", err, stderr.String())
		}
		return stdout.String()
	}
	jsonOutput := execute(true)

	var listing runqueue.QueueListing
	if err := json.Unmarshal([]byte(jsonOutput), &listing); err != nil {
		t.Fatalf("decode --queue --json output: %v\n%s", err, jsonOutput)
	}
	if len(listing.Running) != 1 || listing.Running[0].PID != pid || listing.Running[0].Summary != "go build" {
		t.Fatalf("listing.Running = %+v", listing.Running)
	}
	if len(listing.Waiting) != 1 || listing.Waiting[0].PID != pid || listing.Waiting[0].Summary != "go test" {
		t.Fatalf("listing.Waiting = %+v", listing.Waiting)
	}

	plainOutput := execute(false)
	for _, want := range []string{"running (1)", "waiting (1)", fmt.Sprint(pid), "go build", "go test"} {
		if !strings.Contains(plainOutput, want) {
			t.Errorf("--queue text output missing %q: %q", want, plainOutput)
		}
	}
}
