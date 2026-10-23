package cmdagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCommandsTranslateLocalRequestsAfterParsingWithIndependentStreams(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"dispatch", "status", "await", "list", "logs", "stop"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			called := 0
			root := "before"
			ctx := context.WithValue(t.Context(), contextKey{}, "invocation")
			check := func(got context.Context, r string) {
				t.Helper()
				called++
				if got != ctx || r != "after" {
					t.Fatal(got, r)
				}
			}
			deps.Dispatch = func(c context.Context, r agentrun.DispatchRequest) (agents.Result, error) {
				check(c, r.ProjectsRoot)
				if !reflect.DeepEqual(r.Request, agents.DispatchRequest{Mode: agents.ModeNew, Worktree: "w", Profile: "p", Task: "task", Repository: "a/b", Branch: "feature", Base: "main", Timeout: 3 * time.Minute}) {
					t.Fatal(r)
				}
				_, err := io.WriteString(r.Stderr, "marker warning\n")
				return agents.Result{AgentID: "id", State: agents.StateRunning, Worktree: "w"}, err
			}
			deps.Status = func(c context.Context, r agentrun.LookupRequest) (agents.Result, error) {
				check(c, r.ProjectsRoot)
				if r.AgentID != "id" {
					t.Fatal(r)
				}
				return agents.Result{AgentID: "id", State: agents.StateCompleted}, nil
			}
			deps.Await = func(c context.Context, r agentrun.AwaitRequest) (agents.Result, error) {
				check(c, r.ProjectsRoot)
				if r.AgentID != "id" || r.WaitTimeout != 2*time.Second {
					t.Fatal(r)
				}
				return agents.Result{AgentID: "id", State: agents.StateCompleted, Terminal: true}, nil
			}
			deps.List = func(c context.Context, r agentrun.ListRequest) ([]agents.Result, error) {
				check(c, r.ProjectsRoot)
				return []agents.Result{}, nil
			}
			deps.Logs = func(c context.Context, r agentrun.LogsRequest) (string, error) {
				check(c, r.ProjectsRoot)
				if r.AgentID != "id" || r.Tail != 4 || !r.Raw {
					t.Fatal(r)
				}
				return "transcript", nil
			}
			deps.Stop = func(c context.Context, r agentrun.LookupRequest) (agents.Record, error) {
				check(c, r.ProjectsRoot)
				return agents.Record{AgentID: r.AgentID, WorkerPID: 42}, nil
			}
			runtime := testRuntime()
			runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: root} }
			cmd := New(runtime, deps)
			cmd.SilenceUsage = true
			var out, errout bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errout)
			args := []string{verb}
			switch verb {
			case "dispatch":
				args = append(args, "--new-worktree=w", "--profile=p", "--task=task", "--repo=a/b", "--branch=feature", "--base=main", "--timeout=3m")
			case "await":
				args = append(args, "id", "--wait-timeout=2s")
			case "logs":
				args = append(args, "id", "--tail=4", "--raw")
			case "status", "stop":
				args = append(args, "id")
			}
			cmd.SetArgs(args)
			root = "after"
			if err := cmd.ExecuteContext(ctx); err != nil || called != 1 {
				t.Fatal(err, called)
			}
			if out.Len() == 0 {
				t.Fatal("no output")
			}
			if verb == "dispatch" && errout.String() != "marker warning\n" {
				t.Fatal(errout.String())
			}
		})
	}
}
func TestLocalOperationsPreserveErrorIdentityAndLookupClassification(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"dispatch", "status", "await", "list", "logs", "stop"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			for _, unknown := range []bool{false, true} {
				deps := fakeDependencies()
				sentinel := error(errors.New("operation refused"))
				if unknown {
					sentinel = &agents.UnknownAgentError{AgentID: "id"}
				}
				deps.Dispatch = func(context.Context, agentrun.DispatchRequest) (agents.Result, error) {
					return agents.Result{}, sentinel
				}
				deps.Status = func(context.Context, agentrun.LookupRequest) (agents.Result, error) { return agents.Result{}, sentinel }
				deps.Await = func(context.Context, agentrun.AwaitRequest) (agents.Result, error) { return agents.Result{}, sentinel }
				deps.List = func(context.Context, agentrun.ListRequest) ([]agents.Result, error) { return nil, sentinel }
				deps.Logs = func(context.Context, agentrun.LogsRequest) (string, error) { return "", sentinel }
				deps.Stop = func(context.Context, agentrun.LookupRequest) (agents.Record, error) { return agents.Record{}, sentinel }
				cmd := New(testRuntime(), deps)
				cmd.SilenceUsage = true
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				args := []string{verb, "id"}
				if verb == "list" {
					args = []string{verb}
				}
				if verb == "dispatch" {
					args = []string{verb, "--new-worktree=w", "--profile=p", "--task=t"}
				}
				cmd.SetArgs(args)
				err := cmd.Execute()
				if unknown && verb != "dispatch" && verb != "list" {
					var coded *codedError
					if !errors.As(err, &coded) || coded.code != 1 {
						t.Fatal(err)
					}
				} else if err != sentinel {
					t.Fatal(err, sentinel)
				}
			}
		})
	}
}
func TestTaskSourcesPreserveBytesAndRejectReadFailuresBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, task, file string
		data             []byte
		readerr          error
		want             string
	}{{"inline", " raw task ", "", nil, nil, " raw task "}, {"file", "", " task.md ", []byte("file\n"), nil, "file\n"}, {"emptyfile", "", "empty", []byte(" \n"), nil, ""}, {"unreadable", "", "missing", nil, errors.New("read failure"), ""}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			deps.ReadTaskFile = func(path string) ([]byte, error) {
				if path != strings.TrimSpace(tc.file) {
					t.Fatal(path)
				}
				return tc.data, tc.readerr
			}
			got, err := readAgentTask(testRuntime(), deps, &cobra.Command{}, tc.task, tc.file)
			if tc.want != "" {
				if err != nil || got != tc.want {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("missing refusal")
			}
		})
	}
	cmd := &cobra.Command{}
	sentinel := errors.New("stdin failed")
	cmd.SetIn(errorReader{sentinel})
	if _, err := readAgentTask(testRuntime(), fakeDependencies(), cmd, "", "-"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
func TestRemoteFailuresKeepUsageAndFindingsAndListRawResolutionPolicy(t *testing.T) {
	t.Parallel()
	_, requestErr := (agents.Config{}).Resolve("missing")
	for _, tc := range []struct {
		verb          string
		resolve, call error
		want          int
	}{{"status", requestErr, nil, 2}, {"status", errors.New("config failed"), nil, 1}, {"status", nil, requestErr, 2}, {"status", nil, errors.New("ssh failed"), 1}, {"list", requestErr, nil, 1}} {
		t.Run(tc.verb, func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			deps.ResolveRemote = func(string) (agents.RemoteTarget, error) { return agents.RemoteTarget{Machine: "machine"}, tc.resolve }
			deps.CallRemote = func(context.Context, agents.RemoteTarget, agents.RemoteRequest) (agents.RemoteResponse, error) {
				return agents.RemoteResponse{}, tc.call
			}
			args := []string{"agent", tc.verb, "--to=machine"}
			if tc.verb != "list" {
				args = append(args, "id")
			}
			code, _, stderr := runFake(t, deps, args...)
			if code != tc.want || stderr == "" {
				t.Fatal(code, stderr)
			}
		})
	}
}
func TestEveryRemoteCommandStopsOnAddressOrTransportFailures(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"dispatch", "status", "await", "list", "logs", "stop"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			for _, address := range []bool{false, true} {
				deps := fakeDependencies()
				sentinel := errors.New("remote failed")
				deps.ResolveRemote = func(string) (agents.RemoteTarget, error) {
					if address {
						return agents.RemoteTarget{}, sentinel
					}
					return agents.RemoteTarget{Machine: "machine"}, nil
				}
				deps.CallRemote = func(context.Context, agents.RemoteTarget, agents.RemoteRequest) (agents.RemoteResponse, error) {
					return agents.RemoteResponse{}, sentinel
				}
				args := []string{"agent", verb, "--to=machine"}
				if verb == "dispatch" {
					args = append(args, "--new-worktree=w", "--profile=p", "--task=t")
				} else if verb != "list" {
					args = append(args, "id")
				}
				code, out, errout := runFake(t, deps, args...)
				if code != 1 || out != "" || !strings.Contains(errout, sentinel.Error()) {
					t.Fatal(code, out, errout)
				}
			}
		})
	}
	if _, _, err := agentRemoteTarget(testRuntime(), fakeDependencies(), "", ":id"); err == nil {
		t.Fatal("expected malformed reference")
	}
	if _, _, err := agentRemoteTarget(testRuntime(), fakeDependencies(), "one", "two:id"); err == nil {
		t.Fatal("expected conflicting machine")
	}
}
func TestAwaitRenderingPropagatesInitialWriterFailure(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("writer failed")
	cmd := &cobra.Command{}
	cmd.SetOut(failingWriter{sentinel})
	if err := finishAgentAwait(testRuntime(), fakeDependencies(), cmd, false, agents.Result{}); err != sentinel {
		t.Fatal(err)
	}
}
func TestRemoteResultsNormalizeStateAndRenderWithoutLocalOperations(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"dispatch", "status", "await", "list", "logs", "stop"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			deps.CallRemote = func(_ context.Context, target agents.RemoteTarget, request agents.RemoteRequest) (agents.RemoteResponse, error) {
				if target.Machine != "machine" || request.Operation != verb {
					t.Fatal(target, request)
				}
				result := agents.Result{AgentID: "id", State: agents.StateCompleted, Worktree: "work", WorkerPID: 42}
				return agents.RemoteResponse{Result: &result, Results: []agents.Result{result}, Logs: "remote transcript"}, nil
			}
			args := []string{"agent", verb, "--to=machine"}
			if verb == "dispatch" {
				args = append(args, "--new-worktree=work", "--profile=p", "--task=t")
			} else if verb != "list" {
				args = append(args, "id")
			}
			if verb == "status" || verb == "await" || verb == "list" {
				args = append(args, "--json")
			}
			code, out, stderr := runFake(t, deps, args...)
			if code != 0 || out == "" || stderr != "" {
				t.Fatal(code, out, stderr)
			}
			if verb == "status" || verb == "await" || verb == "list" {
				if !strings.Contains(out, `"machine": "machine"`) || !strings.Contains(out, `"terminal": true`) {
					t.Fatal(out)
				}
			}
		})
	}
}

type contextKey struct{}

func TestStatusTextNamesLiveOwnerAndWorker(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := printAgentStatusText(&out, agents.Result{OwnerAlive: true, WorkerAlive: true}); err != nil || !strings.Contains(out.String(), "owner alive, worker alive") {
		t.Fatal(err, out.String())
	}
}
func TestBoundedAwaitJSONWritesObservationBeforeFindings(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := finishAgentAwait(testRuntime(), fakeDependencies(), cmd, true, agents.Result{AgentID: "id", State: agents.StateRunning})
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != 1 || !strings.Contains(out.String(), `"state": "running"`) {
		t.Fatal(err, out.String())
	}
}
func TestListPreservesOperationOrderInTextJSONAndRemoteText(t *testing.T) {
	t.Parallel()
	for _, remote := range []bool{false, true} {
		for _, jsonOut := range []bool{false, true} {
			t.Run(strings.Join([]string{fmt.Sprint(remote), fmt.Sprint(jsonOut)}, "/"), func(t *testing.T) {
				t.Parallel()
				deps := fakeDependencies()
				results := []agents.Result{{AgentID: "first", State: agents.StateCompleted}, {AgentID: "second", State: agents.StateFailed}}
				deps.List = func(context.Context, agentrun.ListRequest) ([]agents.Result, error) { return results, nil }
				deps.CallRemote = func(context.Context, agents.RemoteTarget, agents.RemoteRequest) (agents.RemoteResponse, error) {
					return agents.RemoteResponse{Results: results}, nil
				}
				args := []string{"agent", "list"}
				if remote {
					args = append(args, "--to=machine")
				}
				if jsonOut {
					args = append(args, "--json")
				}
				code, out, stderr := runFake(t, deps, args...)
				if code != 0 || stderr != "" || strings.Index(out, "first") > strings.Index(out, "second") {
					t.Fatal(code, out, stderr)
				}
				if remote && !strings.Contains(out, "machine") {
					t.Fatal(out)
				}
			})
		}
	}
}
