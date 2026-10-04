package main

import (
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/cmdremote"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remoterun"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// remoteDeps are the seams tests replace: config location, GitHub login,
// provider construction, and the clock.
type remoteDeps struct {
	configPath string
	login      func() (string, error)
	open       func(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error)
	now        func() time.Time
	// progressHeartbeat is a test seam. Production always uses the universal
	// ten-second progress contract.
	progressHeartbeat time.Duration
	// stderr is where a publish says what it must say whether or not it shows
	// progress (the one-time note that the snapshot now carries the machine's
	// hardware facts, a publish that had to leave the optional fields out); nil
	// discards.
	stderr io.Writer
}

func defaultRemoteDeps() remoteDeps {
	return remoteDeps{
		configPath:        wbconfig.DefaultPath(),
		login:             discover.AuthUser,
		open:              openRemote,
		now:               func() time.Time { return time.Now().UTC() },
		progressHeartbeat: universalProgressHeartbeat,
		stderr:            os.Stderr,
	}
}

func remoteProgressHeartbeat(deps remoteDeps) time.Duration {
	if deps.progressHeartbeat > 0 {
		return deps.progressHeartbeat
	}
	return universalProgressHeartbeat
}

// remoteStateCloneURL is the state repository's transport URL. It is always on
// GitHub, which is why the clone path below can place a missing mirror at the
// literal host level.

func openRemote(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error) {
	return remotepublish.Open(cfg, projectsRoot, newCLIErrorRuntime().ExitError)
}
func loadRemote(deps remoteDeps, projectsRoot string) (remotestate.Config, remotestate.Provider, error) {
	return remotepublish.Load(deps.configPath, projectsRoot, deps.open, newCLIErrorRuntime().ExitError)
}

func newRemoteCmd(inv *invocation) *cobra.Command {
	service := remoterun.New(remoterun.DefaultDependencies(newCLIErrorRuntime().ExitError))
	enroll := remoterun.NewEnroll(remoterun.DefaultEnrollDependencies(), newCLIErrorRuntime().ExitError)
	return cmdremote.New(newCLIRuntime(inv), cmdremote.Operations{Claim: service.Claim, Release: service.Release, Machines: service.Machines, Claims: service.Claims, Status: service.Status, Enroll: enroll.Enroll, Heartbeat: universalProgressHeartbeat,
		Publish: func(req remotepublish.Request, progress remotepublish.Progress, notes io.Writer) (remotepublish.Result, error) {
			return remotepublish.New(remotePublishDependencies(defaultRemoteDeps())).Publish(req, progress, notes)
		}})
}
func remoteService(deps remoteDeps) *remoterun.Service {
	return remoterun.New(remoterun.Dependencies{ConfigPath: func() string { return deps.configPath }, Login: deps.login, Open: deps.open, Now: deps.now, ExitError: newCLIErrorRuntime().ExitError})
}
