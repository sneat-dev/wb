package cmdrun

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/operationreceipt"
	"github.com/sneat-dev/wb/internal/runexec"
	"github.com/sneat-dev/wb/internal/runqueue"
	"reflect"
	"strings"
	"testing"
)

type testContextKey struct{}

func executeTestCommand(t *testing.T, args []string, deps Dependencies) (string, string, error) {
	t.Helper()
	command := New(testRuntime(), deps)
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs(args)
	command.SilenceUsage = true
	command.SilenceErrors = true
	err := command.Execute()
	return out.String(), errOut.String(), err
}
func TestModeRefusalsCallNoOperation(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"queue apply": {"--queue", "--apply"}, "queue async": {"--queue", "--async"}, "queue config": {"--queue", "--config", "x"}, "queue key": {"--queue", "--idempotency-key", "x"}, "queue list": {"--queue", "--list"}, "queue history": {"--queue", "--history"}, "queue quiet": {"--queue", "--quiet"}, "queue changed": {"--queue", "--changed"}, "queue target": {"--queue", "--target", "main"},
		"history apply": {"--history", "--apply"}, "history async": {"--history", "--async"}, "history config": {"--history", "--config", "x"}, "history key": {"--history", "--idempotency-key", "x"}, "history list": {"--history", "--list"}, "history quiet": {"--history", "--quiet"}, "history changed": {"--history", "--changed"}, "history target": {"--history", "--target", "x"}, "history days": {"--history", "--days", "0"},
		"command apply": {"--apply", "--", "true"}, "command config": {"--config", "x", "--", "true"}, "command list": {"--list", "--", "true"}, "command days": {"--days", "2", "--", "true"}, "command format": {"--format", "json", "--", "true"}, "changed async": {"--changed", "--async", "--", "true"}, "command target": {"--target", "main", "--", "true"}, "async no worker": {"--async", "--worker", " ", "--", "true"}, "worker sync": {"--worker", "w", "--", "true"}, "key sync": {"--idempotency-key", "k", "--", "true"},
		"recipe async": {"--async"}, "recipe worker": {"--worker", "w"}, "recipe key": {"--idempotency-key", "k"}, "recipe saturated": {"--allow-saturated-host"}, "recipe quiet": {"--quiet"}, "recipe changed": {"--changed"}, "recipe target": {"--target", "x"}, "recipe days": {"--days", "2"}, "recipe format": {"--json"}, "history argument": {"--history", "unexpected"}, "queue argument": {"--queue", "unexpected"}, "empty command": {"--"}, "two recipes": {"a", "b"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			called := false
			deps.Execute = func(context.Context, runexec.ExecuteRequest) (runexec.ExecuteResult, error) {
				called = true
				return runexec.ExecuteResult{}, nil
			}
			deps.Changed = func(context.Context, runexec.ChangedRequest) (runexec.ChangedResult, error) {
				called = true
				return runexec.ChangedResult{}, nil
			}
			deps.History = func(context.Context, runexec.HistoryRequest) (runexec.HistoryResult, error) {
				called = true
				return runexec.HistoryResult{}, nil
			}
			deps.Queue = func(runexec.QueueRequest) runqueue.QueueListing { called = true; return runqueue.QueueListing{} }
			deps.Recipes = func(context.Context, runexec.RecipeRequest) (runexec.RecipeResult, error) {
				called = true
				return runexec.RecipeResult{}, nil
			}
			deps.Submit = func(context.Context, runexec.SubmitRequest) (operationreceipt.Receipt, error) {
				called = true
				return operationreceipt.Receipt{}, nil
			}
			_, _, err := executeTestCommand(t, args, deps)
			if err == nil || called {
				t.Fatalf("error=%v operation called=%v", err, called)
			}
		})
	}
}
func TestExecuteBindsContextParsedFlagsStreamsAndChildExit(t *testing.T) {
	t.Parallel()
	for _, code := range []int{0, 7, -1} {
		t.Run(string(rune('a'+code+1)), func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{ProjectsRoot: "before"}
			runtime := testRuntime()
			runtime.Flags = func() shared.Flags { return flags }
			deps := fakeDependencies()
			ctx := context.WithValue(context.Background(), testContextKey{}, "context")
			var out, errOut bytes.Buffer
			input := strings.NewReader("input")
			deps.Execute = func(got context.Context, request runexec.ExecuteRequest) (runexec.ExecuteResult, error) {
				if got != ctx || request.ProjectsRoot != "parsed" || !request.AllowSaturatedHost || request.Stdin != input || request.Stdout != &out || request.Stderr != &errOut || !reflect.DeepEqual(request.Argv, []string{"child", "arg"}) {
					t.Fatalf("request=%+v", request)
				}
				request.Observe(runexec.QueueEvent{Kind: runexec.ImmediatelyAdmitted})
				return runexec.ExecuteResult{ExitCode: code, ChildFailed: code != 0}, nil
			}
			command := New(runtime, deps)
			command.Flags().StringVar(&flags.ProjectsRoot, "projects-root", flags.ProjectsRoot, "root")
			command.SetContext(ctx)
			command.SetIn(input)
			command.SetOut(&out)
			command.SetErr(&errOut)
			command.SetArgs([]string{"--projects-root", "parsed", "--allow-saturated-host", "--", "child", "arg"})
			command.SilenceErrors = true
			command.SilenceUsage = true
			err := command.Execute()
			if code == 0 && err != nil {
				t.Fatal(err)
			}
			if code != 0 {
				coded, ok := err.(*codedError)
				if !ok || coded.code != code || coded.message != "child exited with status "+map[int]string{7: "7", -1: "-1"}[code] {
					t.Fatalf("exit=%v", err)
				}
			}
			if !strings.Contains(errOut.String(), "admitted (queue empty)") {
				t.Fatal(errOut.String())
			}
		})
	}
}
func TestExecuteAndHistoryErrorsPreserveIdentity(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("operation failed")
	for _, mode := range []string{"execute", "history", "submit", "recipes"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			args := []string{"--", "child"}
			switch mode {
			case "execute":
				deps.Execute = func(context.Context, runexec.ExecuteRequest) (runexec.ExecuteResult, error) {
					return runexec.ExecuteResult{}, sentinel
				}
			case "history":
				args = []string{"--history"}
				deps.History = func(context.Context, runexec.HistoryRequest) (runexec.HistoryResult, error) {
					return runexec.HistoryResult{}, sentinel
				}
			case "submit":
				args = []string{"--async", "--worker", "w", "--", "child"}
				deps.Submit = func(context.Context, runexec.SubmitRequest) (operationreceipt.Receipt, error) {
					return operationreceipt.Receipt{}, sentinel
				}
			case "recipes":
				args = []string{"name"}
				deps.Recipes = func(context.Context, runexec.RecipeRequest) (runexec.RecipeResult, error) {
					return runexec.RecipeResult{}, sentinel
				}
			}
			_, _, err := executeTestCommand(t, args, deps)
			if err != sentinel {
				t.Fatal(err)
			}
		})
	}
}
func TestChangedOrderingNoWorkAndResolvedArguments(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.Changed = func(context.Context, runexec.ChangedRequest) (runexec.ChangedResult, error) {
		return runexec.ChangedResult{NoWork: true, Target: "main", MergeBase: "abc"}, nil
	}
	deps.Execute = func(context.Context, runexec.ExecuteRequest) (runexec.ExecuteResult, error) {
		t.Fatal("no work must not execute")
		return runexec.ExecuteResult{}, nil
	}
	out, _, err := executeTestCommand(t, []string{"--changed", "--worker", "later-refusal", "--", "go", "test"}, deps)
	if err != nil || out != "wb run --changed: no changed Go packages against main (merge base abc); nothing to run\n" {
		t.Fatalf("output=%q err=%v", out, err)
	}
	deps.Changed = func(context.Context, runexec.ChangedRequest) (runexec.ChangedResult, error) {
		return runexec.ChangedResult{MissingTarget: true}, nil
	}
	_, _, err = executeTestCommand(t, []string{"--changed", "--", "go", "test"}, deps)
	if err == nil || !strings.Contains(err.Error(), "could not detect") {
		t.Fatal(err)
	}
	sentinel := errors.New("diff failed")
	deps.Changed = func(context.Context, runexec.ChangedRequest) (runexec.ChangedResult, error) {
		return runexec.ChangedResult{}, sentinel
	}
	_, _, err = executeTestCommand(t, []string{"--changed", "--", "go", "test"}, deps)
	if err != sentinel {
		t.Fatal(err)
	}
	deps.Changed = func(_ context.Context, r runexec.ChangedRequest) (runexec.ChangedResult, error) {
		if r.Target != "base" || !reflect.DeepEqual(r.Argv, []string{"go", "test"}) {
			t.Fatal(r)
		}
		return runexec.ChangedResult{Argv: []string{"go", "test", "./app"}}, nil
	}
	deps.Execute = func(_ context.Context, r runexec.ExecuteRequest) (runexec.ExecuteResult, error) {
		if !reflect.DeepEqual(r.Argv, []string{"go", "test", "./app"}) {
			t.Fatal(r.Argv)
		}
		return runexec.ExecuteResult{}, nil
	}
	_, _, err = executeTestCommand(t, []string{"--changed", "--target", "base", "--", "go", "test"}, deps)
	if err != nil {
		t.Fatal(err)
	}
}
func TestAsyncTrimsWorkerAndIdempotencyAndPrintsActualReceipt(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.Submit = func(_ context.Context, r runexec.SubmitRequest) (operationreceipt.Receipt, error) {
		if r.WorkerID != "worker" || r.IdempotencyKey != "key" || !reflect.DeepEqual(r.Argv, []string{"go", "test"}) || r.Stderr == nil {
			t.Fatal(r)
		}
		return operationreceipt.Receipt{OperationID: "actual", State: "queued", TargetWorkerID: r.WorkerID}, nil
	}
	out, _, err := executeTestCommand(t, []string{"--async", "--worker", " worker ", "--idempotency-key", " key ", "--", "go", "test"}, deps)
	if err != nil || !strings.Contains(out, "\n  \"operation_id\": \"actual\"") || !strings.Contains(out, `"target_worker_id": "worker"`) {
		t.Fatalf("output=%q err=%v", out, err)
	}
}
func TestReadModesBindParsedRootDaysAndPreserveExistingFlagMatrix(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"history", "queue"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			flags := shared.Flags{ProjectsRoot: "before"}
			runtime := testRuntime()
			runtime.Flags = func() shared.Flags { return flags }
			deps.History = func(_ context.Context, r runexec.HistoryRequest) (runexec.HistoryResult, error) {
				if r.Days != 3 {
					t.Fatal(r)
				}
				return runexec.HistoryResult{Days: r.Days}, nil
			}
			deps.Queue = func(r runexec.QueueRequest) runqueue.QueueListing {
				if r.ProjectsRoot != "parsed" {
					t.Fatal(r)
				}
				return runqueue.QueueListing{Budget: 4}
			}
			command := New(runtime, deps)
			command.Flags().StringVar(&flags.ProjectsRoot, "projects-root", flags.ProjectsRoot, "")
			args := []string{"--" + mode, "--worker", "ignored-existing-policy", "--allow-saturated-host", "--format", "json", "--projects-root", "parsed"}
			if mode == "history" {
				args = append(args, "--days", "3")
			}
			command.SetArgs(args)
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetErr(&bytes.Buffer{})
			if err := command.Execute(); err != nil || !strings.HasPrefix(out.String(), "{") {
				t.Fatalf("output=%q err=%v", out.String(), err)
			}
		})
	}
}
func TestAsyncJSONWriterErrorIsObservable(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("receipt writer refused")
	command := New(testRuntime(), fakeDependencies())
	command.SetArgs([]string{"--async", "--worker", "w", "--", "child"})
	command.SetOut(failingWriter{sentinel})
	command.SetErr(&bytes.Buffer{})
	command.SilenceErrors = true
	command.SilenceUsage = true
	if err := command.Execute(); err != sentinel {
		t.Fatal(err)
	}
}
