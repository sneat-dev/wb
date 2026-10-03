package integration

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentRemoteEntryPointAnswersEveryOperation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	home := f.home

	// list with an empty inventory answers with an empty list, not a null.
	code, stdout, stderr := f.remote(t, agents.RemoteRequest{SchemaVersion: 1, Operation: agents.RemoteList})
	if code != 0 {
		t.Fatalf("list exited %d: %s", code, stderr)
	}
	response := decodeRemoteResponseForTest(t, stdout)
	if response.Failure != "" || response.Results == nil || len(response.Results) != 0 {
		t.Fatalf("empty list response = %#v", response)
	}

	// A populated inventory renders the same Result shape the local command does.
	seedAgentRun(t, home, func(record *agents.Record) { record.State = agents.StateCompleted })
	code, stdout, _ = f.remote(t, agents.RemoteRequest{SchemaVersion: 1, Operation: agents.RemoteList})
	if code != 0 {
		t.Fatal("list exited non-zero")
	}
	response = decodeRemoteResponseForTest(t, stdout)
	if len(response.Results) != 1 || response.Results[0].State != agents.StateCompleted {
		t.Fatalf("list response = %#v", response)
	}
	if response.Results[0].Machine != "" {
		t.Fatal("the remote must not claim a machine name it was not told")
	}
}

func TestAgentRemoteEntryPointReportsRefusalsInsideTheResponse(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	// An unknown run is a refusal, not a transport failure.
	code, stdout, _ := f.remote(t, agents.RemoteRequest{
		SchemaVersion: 1, Operation: agents.RemoteStatus,
		AgentID: "agt-00000000000000000000000000000000",
	})
	if code != 0 {
		t.Fatal("a refusal must still be answered on stdout")
	}
	response := decodeRemoteResponseForTest(t, stdout)
	if !strings.Contains(response.Failure, "no dispatched agent run") {
		t.Fatalf("refusal = %#v", response)
	}

	// An unknown profile is refused by the same resolution the local command uses.
	code, stdout, _ = f.remote(t, agents.RemoteRequest{
		SchemaVersion: 1, Operation: agents.RemoteDispatch,
		Mode: agents.ModeNew, Worktree: "never-created", Profile: "nope",
		Task: "x", TimeoutMS: 60000,
	})
	if code != 0 {
		t.Fatal("a refusal must still be answered on stdout")
	}
	response = decodeRemoteResponseForTest(t, stdout)
	if !strings.Contains(response.Failure, "unknown agent profile") {
		t.Fatalf("dispatch refusal = %#v", response)
	}
	if _, err := os.Stat(filepath.Join(f.home, "agents", "never-created")); err == nil {
		t.Fatal("a refused remote dispatch must not create anything")
	}
}

func TestAgentRemoteEntryPointAnswersAwaitLogsAndStop(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	home := f.home

	completed := seedAgentRun(t, home, func(record *agents.Record) {
		record.State = agents.StateCompleted
		record.LogPath = filepath.Join(t.TempDir(), "run.log")
		event := `{"type":"item.completed","item":{"type":"command_execution","command":"ls"}}` + "\n"
		if err := os.WriteFile(record.LogPath, []byte(event), 0o600); err != nil {
			t.Fatal(err)
		}
	})

	// await: a terminal run answers immediately, without blocking on the
	// poll loop, proving the store lookup at the top of the await case runs.
	code, stdout, stderr := f.remote(t, agents.RemoteRequest{
		SchemaVersion: 1, Operation: agents.RemoteAwait, AgentID: completed.AgentID,
	})
	if code != 0 {
		t.Fatalf("await exited %d: %s", code, stderr)
	}
	response := decodeRemoteResponseForTest(t, stdout)
	if response.Failure != "" || response.Result == nil || response.Result.State != agents.StateCompleted {
		t.Fatalf("await response = %#v", response)
	}

	// logs: the recorded transcript is rendered back inside the response.
	code, stdout, stderr = f.remote(t, agents.RemoteRequest{
		SchemaVersion: 1, Operation: agents.RemoteLogs, AgentID: completed.AgentID,
	})
	if code != 0 {
		t.Fatalf("logs exited %d: %s", code, stderr)
	}
	response = decodeRemoteResponseForTest(t, stdout)
	if response.Failure != "" || !strings.Contains(response.Logs, "ran: ls") {
		t.Fatalf("logs response = %#v", response)
	}

	// stop: a completed run is already terminal, so the store lookup runs and
	// StopRun refuses it - a real refusal, not a transport failure.
	code, stdout, stderr = f.remote(t, agents.RemoteRequest{
		SchemaVersion: 1, Operation: agents.RemoteStop, AgentID: completed.AgentID,
	})
	if code != 0 {
		t.Fatalf("stop exited %d: %s", code, stderr)
	}
	response = decodeRemoteResponseForTest(t, stdout)
	if !strings.Contains(response.Failure, "already") {
		t.Fatalf("stop response = %#v", response)
	}
}

func TestAgentRemoteEntryPointRefusesAMalformedRequest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	for name, payload := range map[string]string{
		"not json":          "hello\n",
		"wrong schema":      `{"schema_version":99,"operation":"list"}`,
		"unknown operation": `{"schema_version":1,"operation":"rm-rf"}`,
		"missing fields":    `{"schema_version":1,"operation":"dispatch"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := agentrun.Serve(t.Context(), f.service.Operations(), f.root, strings.NewReader(payload), &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit = %d; a refusal must still be a well-formed answer", code)
			}
			response := decodeRemoteResponseForTest(t, stdout.String())
			if response.Failure == "" {
				t.Fatalf("%s produced no failure: %s", name, stdout.String())
			}
		})
	}
}

func TestAgentCommandsAddressAnotherMachineOverSSH(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	home := f.home
	f.remoteConfig(t, exampleRemoteConfig)
	_ = home

	// status
	result := agents.Result{AgentID: "agt-00000000000000000000000000000000", State: agents.StateCompleted}
	requestPath := f.ssh(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteStatus, Result: &result,
	}))
	code, stdout, stderr := f.run(t, "agent", "status", "--to", "hetzner-vm1", result.AgentID, "--json")
	if code != exitOK {
		t.Fatalf("status --to exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, `"machine": "hetzner-vm1"`) {
		t.Fatalf("a remote result must name its machine:\n%s", stdout)
	}
	sent := readRemoteRequestForTest(t, requestPath)
	if sent.Operation != agents.RemoteStatus || sent.AgentID != result.AgentID {
		t.Fatalf("request = %#v", sent)
	}

	// await, with the wait bound travelling in the request
	f.ssh(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteAwait, Result: &result,
	}))
	if code, _, stderr := f.run(t, "agent", "await", "hetzner-vm1:"+result.AgentID, "--wait-timeout", "30s"); code != exitOK {
		t.Fatalf("await with a machine-qualified reference exited %d: %s", code, stderr)
	}

	// list
	f.ssh(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteList, Results: []agents.Result{result},
	}))
	code, stdout, stderr = f.run(t, "agent", "list", "--to", "hetzner-vm1")
	if code != exitOK || !strings.Contains(stdout, "hetzner-vm1:"+result.AgentID) {
		t.Fatalf("list --to: %d %s %s", code, stdout, stderr)
	}

	// stop
	f.ssh(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteStop, Result: &result,
	}))
	code, stdout, stderr = f.run(t, "agent", "stop", "--to", "hetzner-vm1", result.AgentID)
	if code != exitOK || !strings.Contains(stdout, "hetzner-vm1:") {
		t.Fatalf("stop --to: %d %s %s", code, stdout, stderr)
	}

	// logs
	f.ssh(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteLogs, Logs: "/home/ai/.wb/agents/x/events.jsonl\n  ran: ls",
	}))
	code, stdout, stderr = f.run(t, "agent", "logs", "--to", "hetzner-vm1", result.AgentID)
	if code != exitOK || !strings.Contains(stdout, "ran: ls") {
		t.Fatalf("logs --to: %d %s %s", code, stdout, stderr)
	}

	// dispatch, with the task and worktree mode travelling in the request
	dispatched := result
	dispatched.Worktree = "remote-task"
	f.ssh(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteDispatch, Result: &dispatched,
	}))
	code, stdout, stderr = f.run(t, "agent", "dispatch", "--to", "hetzner-vm1",
		"--new-worktree", "remote-task", "--repo", "acme/app", "--profile", "cheap", "--task", "do the thing")
	if code != exitOK {
		t.Fatalf("dispatch --to exited %d: %s", code, stderr)
	}
	// The output must carry the machine-qualified reference a caller passes back.
	if !strings.Contains(stdout, "hetzner-vm1:"+result.AgentID) {
		t.Fatalf("dispatch --to output must carry the run reference:\n%s", stdout)
	}
	if !strings.Contains(stdout, "hetzner-vm1:remote-task") {
		t.Fatalf("dispatch --to output must locate the remote artefact:\n%s", stdout)
	}
}

func TestAgentRemoteDispatchSendsTheTaskOnStdinAndNothingInArgv(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.remoteConfig(t, exampleRemoteConfig)
	result := agents.Result{AgentID: "agt-00000000000000000000000000000000", State: agents.StateRunning}
	requestPath := f.ssh(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteDispatch, Result: &result,
	}))
	code, _, stderr := f.run(t, "agent", "dispatch", "--to", "hetzner-vm1",
		"--new-worktree", "remote-task", "--repo", "acme/app", "--profile", "cheap",
		"--task", "a task nobody should see in a process table")
	if code != exitOK {
		t.Fatalf("dispatch --to exited %d: %s", code, stderr)
	}
	sent := readRemoteRequestForTest(t, requestPath)
	if sent.Task != "a task nobody should see in a process table" {
		t.Fatalf("the task did not travel on stdin: %#v", sent)
	}
	if sent.Worktree != "remote-task" || sent.Mode != agents.ModeNew || sent.Profile != "cheap" || sent.Repository != "acme/app" {
		t.Fatalf("request = %#v", sent)
	}
	if sent.TimeoutMS <= 0 {
		t.Fatalf("a remote dispatch must carry a positive bound: %#v", sent)
	}
}

func TestAgentRemoteFailuresAreFindingsAndBadMachinesAreUsage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.remoteConfig(t, exampleRemoteConfig)

	// A remote refusal is a finding: the invocation was valid.
	f.ssh(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteStatus, Failure: "no dispatched agent run",
	}))
	code, _, stderr := f.run(t, "agent", "status", "--to", "hetzner-vm1", "agt-00000000000000000000000000000000")
	if code != exitFindings {
		t.Fatalf("remote refusal exit = %d, want findings: %s", code, stderr)
	}

	// An unreachable machine is a finding too.
	f.ssh(t, "")
	code, _, stderr = f.run(t, "agent", "status", "--to", "hetzner-vm1", "agt-00000000000000000000000000000000")
	if code != exitFindings || !strings.Contains(stderr, "hetzner-vm1") {
		t.Fatalf("transport failure: %d %s", code, stderr)
	}

	// An unknown machine is a usage error: nothing was attempted.
	code, _, stderr = f.run(t, "agent", "status", "--to", "nowhere", "agt-00000000000000000000000000000000")
	if code != exitUsage || !strings.Contains(stderr, "configured machines") {
		t.Fatalf("unknown machine: %d %s", code, stderr)
	}

	// Conflicting machine names are a usage error.
	code, _, stderr = f.run(t, "agent", "status", "--to", "nowhere", "hetzner-vm1:agt-00000000000000000000000000000000")
	if code != exitUsage || !strings.Contains(stderr, "conflicts") {
		t.Fatalf("conflicting machine: %d %s", code, stderr)
	}

	// An unconfigured machine map is actionable rather than silent.
	f.remoteConfig(t, "remote:\n  machine: macbook\n")
	code, _, stderr = f.run(t, "agent", "list", "--to", "hetzner-vm1")
	if code == exitOK {
		t.Fatalf("an unconfigured machine map must fail: %s", stderr)
	}
}
