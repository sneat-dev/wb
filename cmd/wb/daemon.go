package main

import (
	"context"
	"io"
	"net"
	"os"

	"github.com/sneat-dev/wb/internal/cli/cmddaemon"
	"github.com/sneat-dev/wb/internal/daemonhost"
	"github.com/sneat-dev/wb/internal/daemonoperation"
	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

func newDaemonCmd(inv *invocation) *cobra.Command {
	return newDaemonCmdWithDependencies(inv, defaultDaemonDependencies())
}
func newDaemonCmdWithDependencies(inv *invocation, deps daemonDependencies) *cobra.Command {
	return cmddaemon.New(newCLIRuntime(inv), daemonCommandDependencies(deps))
}
func daemonCommandDependencies(deps daemonDependencies) cmddaemon.Dependencies {
	service := daemonoperation.Service{RawPolicy: deps.RawPolicy, Client: func(ctx context.Context, root string, progress io.Writer) (daemonv1connect.DaemonServiceClient, error) {
		return daemonruntime.OperationClient(ctx, deps.Dependencies, root, progress)
	}}
	return cmddaemon.Dependencies{
		Serve: func(ctx context.Context, r cmddaemon.ServeRequest, out, errOut io.Writer) error {
			return newDaemonHost(deps).Serve(ctx, daemonhost.Request{ProjectsRoot: r.ProjectsRoot, Listen: r.Listen, LifecycleState: r.LifecycleState, Quiet: r.Quiet, ManagedStart: r.ManagedStart}, out, errOut)
		},
		Start: func(ctx context.Context, r cmddaemon.StartRequest, progress func(string)) (daemonruntime.Result, error) {
			return newDaemonController(deps, r.ProjectsRoot).WithReplaceOtherRoot(r.ReplaceOtherRoot).StartWithProgress(ctx, r.Listen, progress, r.ForceDetached)
		},
		Restart: func(ctx context.Context, r cmddaemon.RestartRequest, progress func(string)) (daemonruntime.Result, error) {
			return newDaemonController(deps, r.ProjectsRoot).WithReplaceOtherRoot(r.ReplaceOtherRoot).RestartWithProgress(ctx, r.IfRunning, progress, r.ForceDetached)
		},
		Status: func(ctx context.Context, root string) (daemonruntime.Result, error) {
			return newDaemonController(deps, root).Status(ctx)
		},
		Stop: func(ctx context.Context, root string) (daemonruntime.Result, error) {
			return newDaemonController(deps, root).Stop(ctx)
		},
		Recover: func(ctx context.Context, root string, apply bool) (daemonruntime.RecoveryResult, error) {
			return newDaemonController(deps, root).RecoverLifecycleLock(ctx, apply)
		},
		Submit: service.Submit, Get: service.Get, Wait: service.Wait, Cancel: service.Cancel, Getwd: os.Getwd,
		SystemdUnit: func() string { return daemonruntime.DaemonSystemdUnitName(os.Getenv) }, Getuid: os.Getuid,
	}
}

type daemonDependencies struct {
	daemonruntime.Dependencies
	listen    func(string, string) (net.Listener, error)
	hubTuning *daemonhost.Tuning
}

func defaultDaemonDependencies() daemonDependencies {
	return daemonDependencies{Dependencies: daemonruntime.DefaultDependencies(usageError)}
}
func newDaemonController(deps daemonDependencies, root string) daemonruntime.Controller {
	return daemonruntime.NewController(deps.Dependencies, root)
}

func newDaemonHost(deps daemonDependencies) *daemonhost.Host {
	return daemonhost.New(daemonhost.Dependencies{Runtime: deps.Dependencies, Listen: deps.listen, Tuning: deps.hubTuning, FleetOptions: cockpitFleetOptions})
}
