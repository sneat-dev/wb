package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/agents"
)

// remoteConfigHome writes a machine map pointing at a target, so a test can
// exercise the SSH paths without the operator's real configuration.
func remoteConfigHome(t *testing.T, body string) string {
	t.Helper()
	configHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configHome, "wb"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "wb", "wb.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	return configHome
}

const exampleRemoteConfig = `
session_move:
  targets:
    hetzner-vm1:
      default_courier: ssh
      ssh:
        host: 178.104.41.143
        user: ai
        wb_path: /home/ai/go/bin/wb
`

// fakeSSHOnPath installs a script named ssh that records the request it was
// given on stdin and replies with the file named by WB_TEST_SSH_RESPONSE.
func fakeSSHOnPath(t *testing.T, response string) (requestPath string) {
	t.Helper()
	directory := t.TempDir()
	requestPath = filepath.Join(directory, "request.json")
	responsePath := filepath.Join(directory, "response.json")
	if err := os.WriteFile(responsePath, []byte(response), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat > " + requestPath + "\ncat " + responsePath + "\n"
	if err := os.WriteFile(filepath.Join(directory, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return requestPath
}

func TestAgentRemoteEntryPointAnswersEveryOperation(t *testing.T) {
	home := agentTestEnv(t)

	// list with an empty inventory answers with an empty list, not a null.
	code, stdout, stderr := runAgentRemote(t, agents.RemoteRequest{SchemaVersion: 1, Operation: agents.RemoteList})
	if code != 0 {
		t.Fatalf("list exited %d: %s", code, stderr)
	}
	response := decodeRemoteResponseForTest(t, stdout)
	if response.Failure != "" || response.Results == nil || len(response.Results) != 0 {
		t.Fatalf("empty list response = %#v", response)
	}

	// A populated inventory renders the same Result shape the local command does.
	seedAgentRun(t, home, func(record *agents.Record) { record.State = agents.StateCompleted })
	code, stdout, _ = runAgentRemote(t, agents.RemoteRequest{SchemaVersion: 1, Operation: agents.RemoteList})
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
	agentTestEnv(t)

	// An unknown run is a refusal, not a transport failure.
	code, stdout, _ := runAgentRemote(t, agents.RemoteRequest{
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
	code, stdout, _ = runAgentRemote(t, agents.RemoteRequest{
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
	if _, err := os.Stat(filepath.Join(agentHomeForTest(t), "agents", "never-created")); err == nil {
		t.Fatal("a refused remote dispatch must not create anything")
	}
}

func TestAgentRemoteEntryPointRefusesAMalformedRequest(t *testing.T) {
	agentTestEnv(t)

	for name, payload := range map[string]string{
		"not json":          "hello\n",
		"wrong schema":      `{"schema_version":99,"operation":"list"}`,
		"unknown operation": `{"schema_version":1,"operation":"rm-rf"}`,
		"missing fields":    `{"schema_version":1,"operation":"dispatch"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := RunAgentRemote(strings.NewReader(payload), &stdout, &stderr)
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
	home := agentTestEnv(t)
	remoteConfigHome(t, exampleRemoteConfig)
	_ = home

	// status
	result := agents.Result{AgentID: "agt-00000000000000000000000000000000", State: agents.StateCompleted}
	requestPath := fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteStatus, Result: &result,
	}))
	code, stdout, stderr := runAgentCLI(t, "agent", "status", "--to", "hetzner-vm1", result.AgentID, "--json")
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
	fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteAwait, Result: &result,
	}))
	if code, _, stderr := runAgentCLI(t, "agent", "await", "hetzner-vm1:"+result.AgentID, "--wait-timeout", "30s"); code != exitOK {
		t.Fatalf("await with a machine-qualified reference exited %d: %s", code, stderr)
	}

	// list
	fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteList, Results: []agents.Result{result},
	}))
	code, stdout, stderr = runAgentCLI(t, "agent", "list", "--to", "hetzner-vm1")
	if code != exitOK || !strings.Contains(stdout, "hetzner-vm1:"+result.AgentID) {
		t.Fatalf("list --to: %d %s %s", code, stdout, stderr)
	}

	// stop
	fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteStop, Result: &result,
	}))
	code, stdout, stderr = runAgentCLI(t, "agent", "stop", "--to", "hetzner-vm1", result.AgentID)
	if code != exitOK || !strings.Contains(stdout, "hetzner-vm1:") {
		t.Fatalf("stop --to: %d %s %s", code, stdout, stderr)
	}

	// logs
	fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteLogs, Logs: "/home/ai/.wb/agents/x/events.jsonl\n  ran: ls",
	}))
	code, stdout, stderr = runAgentCLI(t, "agent", "logs", "--to", "hetzner-vm1", result.AgentID)
	if code != exitOK || !strings.Contains(stdout, "ran: ls") {
		t.Fatalf("logs --to: %d %s %s", code, stdout, stderr)
	}

	// dispatch, with the task and worktree mode travelling in the request
	dispatched := result
	dispatched.Worktree = "remote-task"
	fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteDispatch, Result: &dispatched,
	}))
	code, stdout, stderr = runAgentCLI(t, "agent", "dispatch", "--to", "hetzner-vm1",
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
	agentTestEnv(t)
	remoteConfigHome(t, exampleRemoteConfig)
	result := agents.Result{AgentID: "agt-00000000000000000000000000000000", State: agents.StateRunning}
	requestPath := fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteDispatch, Result: &result,
	}))
	code, _, stderr := runAgentCLI(t, "agent", "dispatch", "--to", "hetzner-vm1",
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
	agentTestEnv(t)
	remoteConfigHome(t, exampleRemoteConfig)

	// A remote refusal is a finding: the invocation was valid.
	fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteStatus, Failure: "no dispatched agent run",
	}))
	code, _, stderr := runAgentCLI(t, "agent", "status", "--to", "hetzner-vm1", "agt-00000000000000000000000000000000")
	if code != exitFindings {
		t.Fatalf("remote refusal exit = %d, want findings: %s", code, stderr)
	}

	// An unreachable machine is a finding too.
	fakeSSHOnPath(t, "")
	t.Setenv("PATH", t.TempDir()+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, _, stderr = runAgentCLI(t, "agent", "status", "--to", "hetzner-vm1", "agt-00000000000000000000000000000000")
	if code != exitFindings || !strings.Contains(stderr, "hetzner-vm1") {
		t.Fatalf("transport failure: %d %s", code, stderr)
	}

	// An unknown machine is a usage error: nothing was attempted.
	code, _, stderr = runAgentCLI(t, "agent", "status", "--to", "nowhere", "agt-00000000000000000000000000000000")
	if code != exitUsage || !strings.Contains(stderr, "configured machines") {
		t.Fatalf("unknown machine: %d %s", code, stderr)
	}

	// Conflicting machine names are a usage error.
	code, _, stderr = runAgentCLI(t, "agent", "status", "--to", "nowhere", "hetzner-vm1:agt-00000000000000000000000000000000")
	if code != exitUsage || !strings.Contains(stderr, "conflicts") {
		t.Fatalf("conflicting machine: %d %s", code, stderr)
	}

	// An unconfigured machine map is actionable rather than silent.
	remoteConfigHome(t, "remote:\n  machine: macbook\n")
	code, _, stderr = runAgentCLI(t, "agent", "list", "--to", "hetzner-vm1")
	if code == exitOK {
		t.Fatalf("an unconfigured machine map must fail: %s", stderr)
	}
}

func TestAgentHelpDocumentsTheRemoteFlags(t *testing.T) {
	for _, path := range []string{"dispatch", "status", "await", "list", "logs", "stop"} {
		t.Run(path, func(t *testing.T) {
			root := newRootCmd()
			found, _, err := root.Find([]string{"agent", path})
			if err != nil {
				t.Fatal(err)
			}
			var help bytes.Buffer
			found.SetOut(&help)
			if err := found.Help(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(help.String(), "--to") {
				t.Errorf("wb agent %s help does not offer --to", path)
			}
		})
	}
}

// ------------------------------------------------------------------- helpers

func runAgentRemote(t *testing.T, request agents.RemoteRequest) (int, string, string) {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := RunAgentRemote(bytes.NewReader(payload), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func decodeRemoteResponseForTest(t *testing.T, raw string) agents.RemoteResponse {
	t.Helper()
	var response agents.RemoteResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatalf("response is not a remote protocol document: %v\n%s", err, raw)
	}
	return response
}

func encodeRemoteResponseForTest(t *testing.T, response agents.RemoteResponse) string {
	t.Helper()
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func readRemoteRequestForTest(t *testing.T, path string) agents.RemoteRequest {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the fake ssh recorded no request: %v", err)
	}
	var request agents.RemoteRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatalf("the request on stdin is not a remote request document: %v\n%s", err, raw)
	}
	return request
}

func agentHomeForTest(t *testing.T) string {
	t.Helper()
	home, err := agentHomeForWrite()
	if err != nil {
		t.Fatal(err)
	}
	return home
}
