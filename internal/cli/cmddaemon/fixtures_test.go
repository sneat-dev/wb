package cmddaemon

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/daemonoperation"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/spf13/cobra"
)

func testRuntime() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "root"} }, ExitError: func(code int, message string) error { return fmt.Errorf("code %d: %s", code, message) }}
}
func testDependencies() Dependencies {
	return Dependencies{
		Serve: func(context.Context, ServeRequest, io.Writer, io.Writer) error { return nil },
		Start: func(context.Context, StartRequest, func(string)) (daemonruntime.Result, error) {
			return daemonruntime.Result{}, nil
		},
		Restart: func(context.Context, RestartRequest, func(string)) (daemonruntime.Result, error) {
			return daemonruntime.Result{}, nil
		},
		Status: func(context.Context, string) (daemonruntime.Result, error) { return daemonruntime.Result{}, nil },
		Stop:   func(context.Context, string) (daemonruntime.Result, error) { return daemonruntime.Result{}, nil },
		Recover: func(context.Context, string, bool) (daemonruntime.RecoveryResult, error) {
			return daemonruntime.RecoveryResult{}, nil
		},
		Submit: func(context.Context, daemonoperation.SubmitRequest, io.Writer) (*daemonv1.Operation, error) {
			return &daemonv1.Operation{}, nil
		},
		Get: func(context.Context, string, string, io.Writer) (*daemonv1.Operation, error) {
			return &daemonv1.Operation{}, nil
		},
		Cancel: func(context.Context, string, string, io.Writer) (*daemonv1.Operation, error) {
			return &daemonv1.Operation{}, nil
		},
		Wait: func(context.Context, string, string, string, io.Writer) (*daemonv1.Operation, error) {
			return &daemonv1.Operation{}, nil
		},
		Getwd: func() (string, error) { return "/cwd", nil }, SystemdUnit: func() string { return "wb.service" }, Getuid: func() int { return 501 },
	}
}
func commandForTest(path string, runtime shared.Runtime, deps Dependencies) *cobra.Command {
	root := New(runtime, deps)
	command, _, err := root.Find(strings.Fields(path))
	if err != nil {
		panic(err)
	}
	command.Parent().RemoveCommand(command)
	return command
}
