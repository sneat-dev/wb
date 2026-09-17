package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// agentDispatchDeps builds the dispatch seams shared by the local command and
// the private remote entry point, so a remote dispatch performs exactly the
// same work a local one does.
func agentDispatchDeps(markerStderr io.Writer, base string) (agents.Store, agents.DispatchDeps, error) {
	home, err := agentHomeForWrite()
	if err != nil {
		return agents.Store{}, agents.DispatchDeps{}, err
	}
	store := agents.NewStore(home)
	marker := &cobra.Command{}
	marker.SetErr(markerStderr)
	return store, agents.DispatchDeps{
		ConfigPath:   agentConfigPath(),
		LoadConfig:   loadAgentConfig,
		ProjectsRoot: projectsRoot,
		Home:         home,
		BeforeCreate: refreshManagedHooksBeforeWorktreeCreate,
		AfterCreate: func(repositories []string, results []worktrees.CreateResult) {
			markCreatedCheckouts(marker, base, results)
		},
		SpawnOwner: func(agentID string) (int, error) {
			return agents.SpawnOwner(store.Dir(agentID), os.Executable)
		},
		Now: time.Now,
	}, nil
}

// RunAgentRemote is the private entry point a machine at the other end of an SSH
// connection invokes:
//
//	wb --non-interactive --wb-internal-agent-remote
//
// The request arrives as a JSON document on standard input and is validated
// exactly as a local command line is, so a remote caller can never reach a
// state a local caller could not. Every outcome — including a refusal — is
// reported inside the response document, so the caller can tell "the remote
// said no" apart from "the remote never answered".
func RunAgentRemote(stdin io.Reader, stdout, stderr io.Writer) int {
	// main handles this before cobra, so the persistent-flag defaults have not
	// run; establish the one global the worktree side effects read.
	if projectsRoot == "" {
		projectsRoot = defaultProjectsRoot()
	}
	request, err := decodeRemoteRequest(stdin)
	response := agents.RemoteResponse{SchemaVersion: 1, Operation: request.Operation}
	if err == nil {
		err = handleRemoteOperation(context.Background(), request, &response)
	}
	if err != nil {
		response.Failure = err.Error()
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(response); encodeErr != nil {
		_, _ = fmt.Fprintln(stderr, "wb agent remote: encode response:", encodeErr)
		return 1
	}
	return 0
}

func decodeRemoteRequest(stdin io.Reader) (agents.RemoteRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(stdin, 8<<20))
	if err != nil {
		return agents.RemoteRequest{}, fmt.Errorf("read remote request: %w", err)
	}
	var request agents.RemoteRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return agents.RemoteRequest{}, fmt.Errorf("remote request is not a wb agent protocol document: %w", err)
	}
	if err := request.Validate(); err != nil {
		return request, err
	}
	return request, nil
}

func handleRemoteOperation(ctx context.Context, request agents.RemoteRequest, response *agents.RemoteResponse) error {
	switch request.Operation {
	case agents.RemoteDispatch:
		store, deps, err := agentDispatchDeps(os.Stderr, request.Base)
		if err != nil {
			return err
		}
		record, err := agents.Dispatch(ctx, agents.DispatchRequest{
			Mode: request.Mode, Worktree: request.Worktree, Profile: request.Profile,
			Task: request.Task, Repository: request.Repository,
			Branch: request.Branch, Base: request.Base,
			Timeout: time.Duration(request.TimeoutMS) * time.Millisecond,
		}, deps)
		if err != nil {
			return err
		}
		result := store.Render(record)
		response.Result = &result
		return nil
	case agents.RemoteStatus:
		_, _, result, err := loadAgentResult(request.AgentID)
		if err != nil {
			return err
		}
		response.Result = &result
		return nil
	case agents.RemoteAwait:
		store, err := agentStoreForRead()
		if err != nil {
			return err
		}
		deadline := time.Time{}
		if request.WaitTimeoutMS > 0 {
			deadline = time.Now().Add(time.Duration(request.WaitTimeoutMS) * time.Millisecond)
		}
		result, err := awaitAgentRun(ctx, store, request.AgentID, deadline)
		if err != nil {
			return err
		}
		response.Result = &result
		return nil
	case agents.RemoteList:
		store, err := agentStoreForRead()
		if err != nil {
			return err
		}
		records, err := store.List()
		if err != nil {
			return err
		}
		results := make([]agents.Result, 0, len(records))
		for _, record := range records {
			results = append(results, store.Render(record))
		}
		response.Results = results
		return nil
	case agents.RemoteLogs:
		_, record, _, err := loadAgentResult(request.AgentID)
		if err != nil {
			return err
		}
		logs, err := renderAgentLogs(record, request.Tail, request.Raw)
		if err != nil {
			return err
		}
		if len(logs) > agents.MaxRemoteLogBytes {
			return fmt.Errorf("transcript for %s is %d bytes; fetch it on that machine with `wb agent logs %s --raw` instead", request.AgentID, len(logs), request.AgentID)
		}
		response.Logs = logs
		return nil
	case agents.RemoteStop:
		store, err := agentStoreForRead()
		if err != nil {
			return err
		}
		record, err := agents.StopRun(store, request.AgentID)
		if err != nil {
			return err
		}
		response.Result = &agents.Result{AgentID: record.AgentID, WorkerPID: record.WorkerPID, State: record.State}
		return nil
	default:
		return fmt.Errorf("remote operation %q is unsupported", request.Operation)
	}
}
