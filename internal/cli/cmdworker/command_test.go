package cmdworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/workerrun"
)

type contextKey struct{}
type refusingWriter struct{ err error }

func (w refusingWriter) Write([]byte) (int, error) { return 0, w.err }
func TestConnectReadsCurrentFlagsContextWritersAndDefaultBudgetPerExecution(t *testing.T) {
	t.Parallel()
	root := "first"
	budget := 3
	canonicalCalls, budgetCalls := 0, 0
	runtime := testRuntime()
	runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: root} }
	deps := testDependencies()
	deps.CanonicalRoots = func(roots []string) ([]string, error) {
		canonicalCalls++
		if !reflect.DeepEqual(roots, []string{"/raw", "/other"}) {
			t.Fatalf("raw roots=%v", roots)
		}
		return []string{"/canonical"}, nil
	}
	deps.Budget = func() int { budgetCalls++; return budget }
	var out, diagnostics bytes.Buffer
	ctx := context.WithValue(t.Context(), contextKey{}, "current")
	sentinel := errors.New("connect boundary")
	// Dependencies are immutable at construction, so build with the final fake.
	deps.Connect = func(got context.Context, r workerrun.ConnectRequest, render func(workerrun.ConnectResult) error, errOut io.Writer) error {
		if got != ctx || errOut != &diagnostics || r.ProjectsRoot != root || r.WorkerID != "worker" || r.CPUCapacity != uint32(budget) || !reflect.DeepEqual(r.Roots, []string{"/canonical"}) {
			t.Fatalf("request=%+v context/writer=%v/%v", r, got, errOut)
		}
		if err := render(workerrun.ConnectResult{WorkerID: r.WorkerID, WorkerGeneration: "g", SchedulerGeneration: "s", CPUCapacity: r.CPUCapacity, PermittedRoots: r.Roots}); err != nil {
			return err
		}
		return sentinel
	}
	command := newConnect(runtime, deps)
	command.SilenceErrors, command.SilenceUsage = true, true
	command.SetContext(ctx)
	command.SetOut(&out)
	command.SetErr(&diagnostics)
	command.SetArgs([]string{"--id", " worker ", "--root", "/raw", "--root", "/other"})
	for _, pair := range []struct {
		root   string
		budget int
	}{{"first", 3}, {"second", 7}} {
		if pair.root == "second" {
			// Cobra StringArray flags append when reparsed; retain the bound
			// flags and exercise fresh execution state without reparsing argv.
			command.SetArgs([]string{})
		}
		root, budget = pair.root, pair.budget
		out.Reset()
		if err := command.Execute(); !errors.Is(err, sentinel) {
			t.Fatalf("delegated identity=%v", err)
		}
		if !strings.Contains(out.String(), "worker worker connected: generation=g scheduler=s cpu=") {
			t.Fatalf("announcement=%q", out.String())
		}
	}
	if canonicalCalls != 2 || budgetCalls != 2 {
		t.Fatalf("canonical/budget=%d/%d", canonicalCalls, budgetCalls)
	}
}
func TestConnectExplicitCapacityJSONBytesAndWriterErrors(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			deps.Budget = func() int { t.Fatal("explicit capacity read budget"); return 0 }
			result := workerrun.ConnectResult{WorkerID: "id", WorkerGeneration: "worker-generation", SchedulerGeneration: "scheduler", Build: "v@rev+modified", ProtocolVersion: 1, OS: "test", Arch: "test", CPUCapacity: 4, PermittedRoots: []string{"/root"}, HeartbeatMilliseconds: 50}
			deps.Connect = func(_ context.Context, r workerrun.ConnectRequest, render func(workerrun.ConnectResult) error, _ io.Writer) error {
				if r.CPUCapacity != 4 {
					t.Fatalf("capacity=%d", r.CPUCapacity)
				}
				return render(result)
			}
			command := newConnect(testRuntime(), deps)
			command.SilenceErrors, command.SilenceUsage = true, true
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetErr(io.Discard)
			command.SetArgs([]string{"--id", "id", "--root", "/root", "--cpu-capacity", "4", "--format", format})
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if format == "json" {
				expected, _ := json.MarshalIndent(result, "", "  ")
				if out.String() != string(expected)+"\n" {
					t.Fatalf("JSON=%s", out.String())
				}
			} else if out.String() != "worker id connected: generation=worker-generation scheduler=scheduler cpu=4 roots=1\n" {
				t.Fatalf("text=%q", out.String())
			}
			sentinel := errors.New("output refused")
			command.SetOut(refusingWriter{sentinel})
			if err := command.Execute(); !errors.Is(err, sentinel) {
				t.Fatalf("writer identity=%v", err)
			}
		})
	}
}
func TestConnectInvalidArgumentsDoNotDelegateAndPreserveUsageFactory(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"extra"}, {"--json", "--format", "toml"}, {"--id", " "}, {"--id", "id"}, {"--id", "id", "--root", "relative"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			sentinel := errors.New("typed root usage")
			runtime := testRuntime()
			calls := 0
			runtime.ExitError = func(code int, message string) error {
				calls++
				if code != 2 || message == "" {
					t.Fatalf("code/message=%d/%s", code, message)
				}
				return sentinel
			}
			command := newConnect(runtime, testDependencies())
			command.SilenceErrors, command.SilenceUsage = true, true
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SetArgs(args)
			err := command.Execute()
			if err == nil {
				t.Fatal("invalid arguments accepted")
			}
			if len(args) > 0 && args[0] == "extra" {
				if calls != 0 {
					t.Fatal("Cobra arg policy reached RunE")
				}
			} else if !errors.Is(err, sentinel) || calls != 1 {
				t.Fatalf("usage identity=%v calls=%d", err, calls)
			}
		})
	}
}
