package cmddaemon

import (
	"context"
	"io"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/daemonoperation"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/spf13/cobra"
)

type StartRequest struct {
	ProjectsRoot, Listen            string
	ForceDetached, ReplaceOtherRoot bool
}
type RestartRequest struct {
	ProjectsRoot                               string
	IfRunning, ForceDetached, ReplaceOtherRoot bool
}
type ServeRequest struct {
	ProjectsRoot, Listen, LifecycleState string
	Quiet, ManagedStart                  bool
}
type Dependencies struct {
	Serve        func(context.Context, ServeRequest, io.Writer, io.Writer) error
	Start        func(context.Context, StartRequest, func(string)) (daemonruntime.Result, error)
	Restart      func(context.Context, RestartRequest, func(string)) (daemonruntime.Result, error)
	Status, Stop func(context.Context, string) (daemonruntime.Result, error)
	Recover      func(context.Context, string, bool) (daemonruntime.RecoveryResult, error)
	Submit       func(context.Context, daemonoperation.SubmitRequest, io.Writer) (*daemonv1.Operation, error)
	Get, Cancel  func(context.Context, string, string, io.Writer) (*daemonv1.Operation, error)
	Wait         func(context.Context, string, string, string, io.Writer) (*daemonv1.Operation, error)
	Getwd        func() (string, error)
	SystemdUnit  func() string
	Getuid       func() int
}

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "daemon", Short: "Operate WB's local loopback dashboard and scheduler lifecycle"}
	command.AddCommand(newServe(runtime, deps), newStart(runtime, deps), newStatus(runtime, deps), newStop(runtime, deps), newRestart(runtime, deps), newRecover(runtime, deps), newOperation(runtime, deps))
	return command
}
