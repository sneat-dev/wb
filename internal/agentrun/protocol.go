package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/agents"
	"io"
	"time"
)

// Serve reports refusals inside the response; only encoding failure exits nonzero.
func Serve(ctx context.Context, ops Operations, root string, stdin io.Reader, stdout, stderr io.Writer) int {
	request, err := DecodeRequest(stdin)
	response := agents.RemoteResponse{SchemaVersion: 1, Operation: request.Operation}
	if err == nil {
		err = Handle(ctx, ops, root, stderr, request, &response)
	}
	if err != nil {
		response.Failure = err.Error()
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(response); err != nil {
		_, _ = fmt.Fprintln(stderr, "wb agent remote: encode response:", err)
		return 1
	}
	return 0
}
func DecodeRequest(stdin io.Reader) (agents.RemoteRequest, error) {
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
func Handle(ctx context.Context, ops Operations, root string, stderr io.Writer, r agents.RemoteRequest, response *agents.RemoteResponse) error {
	lookup := LookupRequest{root, r.AgentID}
	switch r.Operation {
	case agents.RemoteDispatch:
		result, err := ops.Dispatch(ctx, DispatchRequest{root, stderr, agents.DispatchRequest{Mode: r.Mode, Worktree: r.Worktree, Profile: r.Profile, Task: r.Task, Repository: r.Repository, Branch: r.Branch, Base: r.Base, Timeout: time.Duration(r.TimeoutMS) * time.Millisecond}})
		if err != nil {
			return err
		}
		response.Result = &result
	case agents.RemoteStatus:
		result, err := ops.Status(ctx, lookup)
		if err != nil {
			return err
		}
		response.Result = &result
	case agents.RemoteAwait:
		result, err := ops.Await(ctx, AwaitRequest{root, r.AgentID, time.Duration(r.WaitTimeoutMS) * time.Millisecond})
		if err != nil {
			return err
		}
		response.Result = &result
	case agents.RemoteList:
		results, err := ops.List(ctx, ListRequest{root})
		if err != nil {
			return err
		}
		response.Results = results
	case agents.RemoteLogs:
		logs, err := ops.Logs(ctx, LogsRequest{root, r.AgentID, r.Tail, r.Raw})
		if err != nil {
			return err
		}
		if len(logs) > agents.MaxRemoteLogBytes {
			return fmt.Errorf("transcript for %s is %d bytes; fetch it on that machine with `wb agent logs %s --raw` instead", r.AgentID, len(logs), r.AgentID)
		}
		response.Logs = logs
	case agents.RemoteStop:
		record, err := ops.Stop(ctx, lookup)
		if err != nil {
			return err
		}
		response.Result = &agents.Result{AgentID: record.AgentID, WorkerPID: record.WorkerPID, State: record.State}
	default:
		return fmt.Errorf("remote operation %q is unsupported", r.Operation)
	}
	return nil
}
