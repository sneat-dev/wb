package cmddeps

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/spf13/cobra"
)

func propagationChild(t *testing.T, runtime shared.Runtime, ops PropagationDependencies) *cobra.Command {
	t.Helper()
	family := NewPropagate(runtime, ops)
	child, _, err := family.Find([]string{"local"})
	if err != nil {
		t.Fatal(err)
	}
	family.RemoveCommand(child)
	child.SilenceErrors = true
	child.SilenceUsage = true
	return child
}
func TestDepsPropagateLocalRequiresAConsumer(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	command := propagationChild(t, testRuntime(), PropagationDependencies{Run: func(context.Context, depsrun.PropagationRequest) (locallink.Result, error) {
		t.Fatal("operation before required consumer")
		return locallink.Result{}, nil
	}})
	command.SetOut(&out)
	command.SetArgs([]string{t.TempDir()})
	err := command.Execute()
	if err == nil {
		t.Fatal("propagating to nothing succeeded")
	}
	if !strings.Contains(err.Error(), "--to") {
		t.Errorf("error = %q, want missing flag named", err)
	}
}
func TestPropagationArgumentsErrorsAndOutputCustody(t *testing.T) {
	t.Parallel()
	boom := errors.New("operation refused")
	for _, row := range []struct {
		name   string
		args   []string
		result locallink.Result
		opErr  error
		want   string
		writer bool
	}{
		{name: "format", args: []string{"--format", "xml"}, want: "format"},
		{name: "library", args: []string{"--to", "consumer"}, want: "library worktree"},
		{name: "argc", args: []string{"one", "two"}, want: "accepts at most"},
		{name: "operation", args: []string{"library", "--to", "consumer"}, opErr: boom, want: "operation refused"},
		{name: "refusal", args: []string{"library", "--to", "consumer"}, opErr: &locallink.Refusal{Message: "guard"}, want: "guard"},
		{name: "writer", args: []string{"library", "--to", "consumer"}, result: locallink.Result{Plan: []string{"real report row"}, Consumers: []locallink.ConsumerResult{{Errors: []string{"findings"}}}}, writer: true, want: "rwi01: write refused"},
		{name: "findings", args: []string{"library", "--to", "consumer"}, result: locallink.Result{Consumers: []locallink.ConsumerResult{{Consumer: "consumer", Errors: []string{"broken"}}}}, want: "local propagation reported findings"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			command := propagationChild(t, testRuntime(), PropagationDependencies{Run: func(context.Context, depsrun.PropagationRequest) (locallink.Result, error) {
				calls++
				return row.result, row.opErr
			}})
			var out bytes.Buffer
			command.SetOut(&out)
			if row.writer {
				command.SetOut(&rwi01FailAtWriter{n: 1})
			}
			command.SetArgs(row.args)
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("error=%v want %s", err, row.want)
			}
			if row.writer && !errors.Is(err, errRwi01WriteAt) {
				t.Fatal("writer identity lost to findings")
			}
			if row.opErr == boom && !errors.Is(err, boom) {
				t.Fatal("error identity lost")
			}
			if row.name == "findings" && !strings.Contains(out.String(), "broken") {
				t.Fatal("findings before report")
			}
			if row.name == "format" || row.name == "library" || row.name == "argc" {
				if calls != 0 {
					t.Fatal("operation before validation")
				}
			}
		})
	}
}
func TestPropagationReadsCurrentFlagsContextWritersAndUndo(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "old"}
	runtime := testRuntime()
	runtime.Flags = func() shared.Flags { return flags }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var got depsrun.PropagationRequest
	var gotCtx context.Context
	command := NewPropagate(runtime, PropagationDependencies{Run: func(ctx context.Context, request depsrun.PropagationRequest) (locallink.Result, error) {
		got = request
		gotCtx = ctx
		return locallink.Result{ContentHash: "hash"}, nil
	}})
	if command.Annotations["wb.dev/discovery-terms"] == "" || command.Commands()[0].Annotations["wb.dev/discovery-terms"] == "" {
		t.Fatal("missing discovery annotations")
	}
	flags.ProjectsRoot = "current"
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetContext(ctx)
	command.SetArgs([]string{"local", "--undo", "--to", "a", "--to", "b", "--verify", "--stream", "stream", "--timeout", "3s", "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotCtx != ctx || got.ProjectsRoot != "current" || !reflect.DeepEqual(got.Options, locallink.Options{Consumers: []string{"a", "b"}, Undo: true, Verify: true, Timeout: 3 * time.Second, Stream: "stream"}) || !strings.Contains(out.String(), "hash") {
		t.Fatalf("request=%+v out=%s", got, out.String())
	}
}
