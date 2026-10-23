package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/cmddeps"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/locallink"
)

func TestPropagationFamilyBindsActualStoreUndoAndGitFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	service := depsrun.NewPropagation(depsrun.DefaultPropagationDependencies())
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }, ExitError: func(_ int, message string) error { return &propagationNativeExit{message} }}
	command := cmddeps.NewPropagate(runtime, cmddeps.PropagationOperations(service))
	command.SilenceUsage = true
	command.SilenceErrors = true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetContext(context.Background())
	command.SetArgs([]string{"local", "--undo", "--to", root, "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result locallink.Result
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.Plan) != 3 || len(result.Consumers) != 1 {
		t.Fatalf("actual undo=%s %v", out.String(), err)
	}
	out.Reset()
	command.SetArgs([]string{"local", t.TempDir(), "--to", root, "--undo=false"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "git") || out.Len() != 0 {
		t.Fatalf("actual prehash failure=%v output=%s", err, out.String())
	}
}

type propagationNativeExit struct{ message string }

func (err *propagationNativeExit) Error() string { return err.message }
