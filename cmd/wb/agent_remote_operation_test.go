package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/agents"
)

// cov-cgx-c1 trial lane: covers handleRemoteOperation branches named in
// scratchpad/seams/rwi/c1.txt for cmd/wb/agent_remote.go. Dispatch success
// and Stop success are not exercised here: they drive agents.Dispatch /
// agents.StopRun to completion, which respectively create a real worktree
// and signal a live OS process — out of unit-tier scope (see final report).

func TestHandleRemoteOperationStatusSuccess(t *testing.T) {
	home := agentTestEnv(t)
	record := seedAgentRun(t, home, func(r *agents.Record) { r.State = agents.StateCompleted })
	inv := &invocation{projectsRoot: os.Getenv("WB_PROJECTS_ROOT")}

	var response agents.RemoteResponse
	err := handleRemoteOperation(inv, context.Background(), agents.RemoteRequest{
		Operation: agents.RemoteStatus, AgentID: record.AgentID,
	}, &response)
	if err != nil {
		t.Fatal(err)
	}
	if response.Result == nil || response.Result.AgentID != record.AgentID {
		t.Fatalf("response = %#v", response)
	}
}

func TestHandleRemoteOperationAwaitSuccessCarriesWaitTimeout(t *testing.T) {
	home := agentTestEnv(t)
	record := seedAgentRun(t, home, func(r *agents.Record) { r.State = agents.StateCompleted })
	inv := &invocation{projectsRoot: os.Getenv("WB_PROJECTS_ROOT")}

	var response agents.RemoteResponse
	err := handleRemoteOperation(inv, context.Background(), agents.RemoteRequest{
		Operation: agents.RemoteAwait, AgentID: record.AgentID, WaitTimeoutMS: 5000,
	}, &response)
	if err != nil {
		t.Fatal(err)
	}
	if response.Result == nil || !response.Result.Terminal {
		t.Fatalf("response = %#v", response)
	}
}

func TestHandleRemoteOperationRefusesOversizedLogFetch(t *testing.T) {
	home := agentTestEnv(t)
	record := seedAgentRun(t, home, func(r *agents.Record) {})
	if err := os.MkdirAll(filepath.Dir(record.LogPath), 0o700); err != nil {
		t.Fatal(err)
	}
	oversized := make([]byte, agents.MaxRemoteLogBytes+1024)
	if err := os.WriteFile(record.LogPath, oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	inv := &invocation{projectsRoot: os.Getenv("WB_PROJECTS_ROOT")}

	var response agents.RemoteResponse
	err := handleRemoteOperation(inv, context.Background(), agents.RemoteRequest{
		Operation: agents.RemoteLogs, AgentID: record.AgentID, Raw: true,
	}, &response)
	if err == nil || !strings.Contains(err.Error(), "fetch it on that machine") {
		t.Fatalf("err = %v", err)
	}
}

func TestHandleRemoteOperationRejectsUnsupportedOperation(t *testing.T) {
	t.Parallel()
	var response agents.RemoteResponse
	err := handleRemoteOperation(&invocation{}, context.Background(), agents.RemoteRequest{
		Operation: "bogus",
	}, &response)
	if err == nil || !strings.Contains(err.Error(), `remote operation "bogus" is unsupported`) {
		t.Fatalf("err = %v", err)
	}
}
