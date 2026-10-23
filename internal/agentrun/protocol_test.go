package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/agents"
	"io"
	"strings"
	"testing"
	"time"
)

func TestProtocolOperationsPreserveConcreteRequestsAndStopShape(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(t.Context(), contextKey{}, "context")
	var stderr bytes.Buffer
	root := "private-root"
	ops := Operations{}
	seen := []string{}
	check := func(c context.Context, r string, verb string) {
		t.Helper()
		if c != ctx || r != root {
			t.Fatal(c, r)
		}
		seen = append(seen, verb)
	}
	ops.Dispatch = func(c context.Context, r DispatchRequest) (agents.Result, error) {
		check(c, r.ProjectsRoot, "dispatch")
		if r.Stderr != &stderr || r.Request.Timeout != 3*time.Second || r.Request.Task != "private" {
			t.Fatal(r)
		}
		if _, err := io.WriteString(r.Stderr, "bound marker warning\n"); err != nil {
			return agents.Result{}, err
		}
		return agents.Result{AgentID: "id"}, nil
	}
	ops.Status = func(c context.Context, r LookupRequest) (agents.Result, error) {
		check(c, r.ProjectsRoot, "status")
		if r.AgentID != "id" {
			t.Fatal(r)
		}
		return agents.Result{AgentID: r.AgentID}, nil
	}
	ops.Await = func(c context.Context, r AwaitRequest) (agents.Result, error) {
		check(c, r.ProjectsRoot, "await")
		if r.WaitTimeout != 2*time.Second {
			t.Fatal(r)
		}
		return agents.Result{Terminal: true}, nil
	}
	ops.List = func(c context.Context, r ListRequest) ([]agents.Result, error) {
		check(c, r.ProjectsRoot, "list")
		return []agents.Result{}, nil
	}
	ops.Logs = func(c context.Context, r LogsRequest) (string, error) {
		check(c, r.ProjectsRoot, "logs")
		if r.Tail != 4 || !r.Raw {
			t.Fatal(r)
		}
		return "transcript", nil
	}
	ops.Stop = func(c context.Context, r LookupRequest) (agents.Record, error) {
		check(c, r.ProjectsRoot, "stop")
		return agents.Record{AgentID: r.AgentID, WorkerPID: 42, State: agents.StateRunning, Task: "private", Worktree: "secret"}, nil
	}
	for _, verb := range []string{"dispatch", "status", "await", "list", "logs", "stop"} {
		var response agents.RemoteResponse
		if err := Handle(ctx, ops, root, &stderr, agents.RemoteRequest{Operation: verb, AgentID: "id", Task: "private", TimeoutMS: 3000, WaitTimeoutMS: 2000, Tail: 4, Raw: true}, &response); err != nil {
			t.Fatal(err)
		}
		switch verb {
		case "stop":
			if response.Result.AgentID != "id" || response.Result.WorkerPID != 42 || response.Result.Worktree != "" || response.Result.Terminal {
				t.Fatal(response)
			}
		case "list":
			if response.Results == nil {
				t.Fatal("null inventory")
			}
		case "logs":
			if response.Logs != "transcript" {
				t.Fatal(response)
			}
		}
	}
	if len(seen) != 6 || stderr.String() != "bound marker warning\n" {
		t.Fatal(seen, stderr.String())
	}
}
func TestProtocolOperationFailuresAndOversizedLogsRemainRefusals(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("operation failed")
	ops := Operations{Dispatch: func(context.Context, DispatchRequest) (agents.Result, error) { return agents.Result{}, sentinel }, Status: func(context.Context, LookupRequest) (agents.Result, error) { return agents.Result{}, sentinel }, Await: func(context.Context, AwaitRequest) (agents.Result, error) { return agents.Result{}, sentinel }, List: func(context.Context, ListRequest) ([]agents.Result, error) { return nil, sentinel }, Logs: func(context.Context, LogsRequest) (string, error) { return "", sentinel }, Stop: func(context.Context, LookupRequest) (agents.Record, error) { return agents.Record{}, sentinel }}
	for _, verb := range []string{"dispatch", "status", "await", "list", "logs", "stop"} {
		if err := Handle(t.Context(), ops, "root", io.Discard, agents.RemoteRequest{Operation: verb}, &agents.RemoteResponse{}); err != sentinel {
			t.Fatal(verb, err)
		}
	}
	ops.Logs = func(context.Context, LogsRequest) (string, error) {
		return strings.Repeat("x", agents.MaxRemoteLogBytes+1), nil
	}
	if err := Handle(t.Context(), ops, "", io.Discard, agents.RemoteRequest{Operation: "logs", AgentID: "id"}, &agents.RemoteResponse{}); err == nil || !strings.Contains(err.Error(), "fetch it on that machine") {
		t.Fatal(err)
	}
	if err := Handle(t.Context(), ops, "", io.Discard, agents.RemoteRequest{Operation: "unsupported"}, &agents.RemoteResponse{}); err == nil {
		t.Fatal("missing refusal")
	}
}
func TestServeDecodesBoundsRefusalsAndEncodingFailures(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("reader failed")
	if _, err := DecodeRequest(errorReader{sentinel}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	for _, raw := range []string{"not-json", `{"schema_version":99,"operation":"list"}`, strings.Repeat(" ", 8<<20) + `{"schema_version":1,"operation":"list"}`} {
		var out, errout bytes.Buffer
		code := Serve(t.Context(), Operations{}, "root", strings.NewReader(raw), &out, &errout)
		var response agents.RemoteResponse
		if code != 0 || json.Unmarshal(out.Bytes(), &response) != nil || response.Failure == "" {
			t.Fatal(code, out.String())
		}
	}
	ops := Operations{List: func(context.Context, ListRequest) ([]agents.Result, error) { return []agents.Result{}, nil }}
	var out, errout bytes.Buffer
	raw := `{"schema_version":1,"operation":"list"}`
	if code := Serve(t.Context(), ops, "root", strings.NewReader(raw), &out, &errout); code != 0 || !strings.Contains(out.String(), `"results": []`) {
		t.Fatal(code, out.String())
	}
	ops.List = func(context.Context, ListRequest) ([]agents.Result, error) { return nil, sentinel }
	out.Reset()
	if code := Serve(t.Context(), ops, "root", strings.NewReader(raw), &out, &errout); code != 0 || !strings.Contains(out.String(), sentinel.Error()) {
		t.Fatal(code, out.String())
	}
	if code := Serve(t.Context(), ops, "root", strings.NewReader(raw), failWriter{sentinel}, &errout); code != 1 || !strings.Contains(errout.String(), "encode response: reader failed") {
		t.Fatal(code, errout.String())
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }
