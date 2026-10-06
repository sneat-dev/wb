package integration

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleRemoteOperationStatusSuccess(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	home := f.home
	record := seedAgentRun(t, home, func(r *agents.Record) { r.State = agents.StateCompleted })

	var response agents.RemoteResponse
	err := agentrun.Handle(context.Background(), f.service.Operations(), f.root, io.Discard, agents.RemoteRequest{
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
	t.Parallel()
	f := newFixture(t)
	home := f.home
	record := seedAgentRun(t, home, func(r *agents.Record) { r.State = agents.StateCompleted })

	var response agents.RemoteResponse
	err := agentrun.Handle(context.Background(), f.service.Operations(), f.root, io.Discard, agents.RemoteRequest{
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
	t.Parallel()
	f := newFixture(t)
	home := f.home
	record := seedAgentRun(t, home, func(r *agents.Record) {})
	if err := os.MkdirAll(filepath.Dir(record.LogPath), 0o700); err != nil {
		t.Fatal(err)
	}
	oversized := make([]byte, agents.MaxRemoteLogBytes+1024)
	if err := os.WriteFile(record.LogPath, oversized, 0o600); err != nil {
		t.Fatal(err)
	}

	var response agents.RemoteResponse
	err := agentrun.Handle(context.Background(), f.service.Operations(), f.root, io.Discard, agents.RemoteRequest{
		Operation: agents.RemoteLogs, AgentID: record.AgentID, Raw: true,
	}, &response)
	if err == nil || !strings.Contains(err.Error(), "fetch it on that machine") {
		t.Fatalf("err = %v", err)
	}
}

func TestHandleRemoteOperationRejectsUnsupportedOperation(t *testing.T) {
	f := newFixture(t)
	t.Parallel()
	var response agents.RemoteResponse
	err := agentrun.Handle(context.Background(), f.service.Operations(), f.root, io.Discard, agents.RemoteRequest{
		Operation: "bogus",
	}, &response)
	if err == nil || !strings.Contains(err.Error(), `remote operation "bogus" is unsupported`) {
		t.Fatalf("err = %v", err)
	}
}

func TestAwaitAgentRunReturnsContextErrorWhenCancelled(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	home := f.home
	store := agents.NewStore(home)
	record := seedAgentRun(t, home, func(r *agents.Record) { r.State = agents.StateRunning })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (agentrun.Awaiter{Load: store.Load, Render: store.Render, Now: time.Now, Wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}).Await(ctx, record.AgentID, time.Time{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want %v", err, context.Canceled)
	}
}
